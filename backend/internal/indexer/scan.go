package indexer

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/apperror"
	"github.com/ThalesMMS/CodeAtlas/internal/changeset"
	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	"github.com/ThalesMMS/CodeAtlas/internal/observability"
	codeparser "github.com/ThalesMMS/CodeAtlas/internal/parser"
	"github.com/ThalesMMS/CodeAtlas/internal/pathutil"
	"github.com/ThalesMMS/CodeAtlas/internal/repository"
)

// Scan runs a strict two-phase index update: collect → parse → embed → build
// ChangeSet → prepare → single atomic commit → publish events. No preparation
// phase mutates the active store; a failure in any required file leaves the
// store and the on-disk snapshot exactly at the previous version.
func (i *Indexer) Scan(ctx context.Context) error {
	return i.ReconcileFull(ctx, i.store.Version())
}

func (i *Indexer) CurrentRevision() uint64 { return i.store.Version() }

func (i *Indexer) ReconcileFull(ctx context.Context, expectedRevision uint64) error {
	return i.reconcile(ctx, expectedRevision, func(ctx context.Context) (reconcileBatch, error) {
		return i.collectBatch(ctx)
	})
}

func (i *Indexer) ReconcilePaths(ctx context.Context, paths []string, expectedRevision uint64) error {
	normalized, err := normalizeRequestedPaths(paths)
	if err != nil {
		return err
	}
	return i.reconcile(ctx, expectedRevision, func(ctx context.Context) (reconcileBatch, error) {
		return i.collectPaths(ctx, normalized)
	})
}

func (i *Indexer) ReconcileSubtree(ctx context.Context, root string, expectedRevision uint64) error {
	normalized, err := normalizeSubtreeRoot(root)
	if err != nil {
		return err
	}
	if normalized == "." {
		return i.ReconcileFull(ctx, expectedRevision)
	}
	return i.reconcile(ctx, expectedRevision, func(ctx context.Context) (reconcileBatch, error) {
		return i.collectSubtree(ctx, normalized)
	})
}

type reconcileBatch struct {
	candidates        []candidate
	deletions         []string
	deletionKinds     map[string]string
	selfWrites        []selfWriteConfirmation
	externalMutations []externalMutationConfirmation
}

type selfWriteConfirmation struct {
	path       string
	mutationID domain.InternalMutationID
}

type externalMutationConfirmation struct {
	path       string
	mutationID domain.InternalMutationID
}

func (i *Indexer) reconcile(ctx context.Context, expectedRevision uint64, collect func(context.Context) (reconcileBatch, error)) (err error) {
	if !i.runMu.TryLock() {
		return ErrIndexingInProgress // a scan is already running; do not overlap
	}
	defer i.runMu.Unlock()

	metadata, err := i.store.SnapshotMetadataContext(ctx)
	if err != nil {
		return err
	}
	baseVersion := uint64(metadata.Revision)
	if expectedRevision != baseVersion {
		return repository.ErrVersionConflict
	}
	i.setState(ScanPreparing)
	i.publish("index.prepare.started", "", "Checking workspace changes")
	baseKnown := make(map[string]struct{})
	baseHashes := make(map[string]string)
	for _, path := range i.store.KnownPaths() {
		baseKnown[path] = struct{}{}
		if hash, ok := i.store.FileHash(path); ok {
			baseHashes[path] = hash
		}
	}

	// One structured begin/end pair per scan (never per file), correlated by
	// operation id and change-set id. The deferred end records the result,
	// counts, version transition and duration exactly once on every return path.
	operationID := observability.NewID()
	start := i.clock.Now()
	i.metrics.IndexScanStarted()
	i.logger.Info("index scan started", "operationId", operationID, "baseVersion", baseVersion)
	obs := scanObservation{operationID: operationID, baseVersion: baseVersion, newVersion: baseVersion, step: "collect"}
	defer func() {
		duration := i.clock.Now().Sub(start)
		now := i.clock.Now()
		if err != nil {
			i.metrics.IndexScanFailed(duration, now)
			i.logger.Error("index scan failed", "operationId", operationID, "step", obs.step,
				"errorCode", scanErrorCode(err), "baseVersion", baseVersion, "durationMs", duration.Milliseconds())
			return
		}
		i.metrics.IndexScanSucceeded(duration, i.indexSize(), now)
		i.logger.Info("index scan completed", "operationId", operationID, "changeSetId", obs.changeSetID,
			"snapshotId", obs.snapshotID, "baseVersion", baseVersion, "newVersion", obs.newVersion,
			"filesChanged", obs.changed, "filesRemoved", obs.removed, "durationMs", duration.Milliseconds())
	}()

	batch, err := collect(ctx)
	if err != nil {
		return i.failPrepare(fmt.Errorf("coleta: %w", err))
	}
	obs.changed, obs.removed = len(batch.candidates), len(batch.deletions)
	if len(batch.candidates) == 0 && len(batch.deletions) == 0 {
		i.setState(ScanComplete)
		i.markExternalMutations(ctx, batch.externalMutations)
		if i.confirmAndPublishSelfWrites(ctx, batch.selfWrites, baseVersion) {
			return nil
		}
		i.publish("index.idle", "", "Index is already up to date")
		return nil
	}

	obs.step = "parse"
	parsed, err := i.parseBatch(ctx, batch.candidates)
	if err != nil {
		return i.failPrepare(err)
	}
	obs.changed = len(parsed)

	var embeddings map[string][]float64
	if i.retriever != nil {
		obs.step = "embed"
		changedSymbols := make([]domain.Symbol, 0)
		for _, file := range parsed {
			changedSymbols = append(changedSymbols, file.Symbols...)
		}
		embeddings, err = i.retriever.GenerateEmbeddings(ctx, changedSymbols)
		if err != nil {
			return i.failPrepare(fmt.Errorf("embeddings: %w", err))
		}
	}

	i.setState(ScanCommitting)
	obs.step = "changeset"
	change, err := buildScanChangeSet(baseVersion, parsed, batch.deletions, embeddings)
	if err != nil {
		return i.failCommit(fmt.Errorf("changeset: %w", err))
	}
	obs.changeSetID = change.ID()
	obs.step = "prepare"
	prepared, err := i.store.PrepareContext(ctx, change)
	if err != nil {
		return i.failCommit(fmt.Errorf("prepare: %w", err))
	}
	if prepared.IsNoop() {
		i.setState(ScanComplete)
		i.markExternalMutations(ctx, batch.externalMutations)
		if i.confirmAndPublishSelfWrites(ctx, batch.selfWrites, baseVersion) {
			return nil
		}
		i.publish("index.idle", "", "Index is already up to date")
		return nil
	}
	obs.step = "commit"
	result, err := i.store.CommitPreparedContext(ctx, prepared)
	if err != nil {
		i.metrics.Commit(false)
		return i.failCommit(fmt.Errorf("commit: %w", err))
	}
	i.metrics.Commit(true)
	obs.newVersion = uint64(result.Revision)

	// A single aggregated commit event is published only after the commit is
	// persisted and the in-memory state has advanced. It carries the published
	// SnapshotID/Revision so a consumer can pin provenance.
	i.setState(ScanComplete)
	event := batchEvent(change.ID(), uint64(result.Revision), parsed, batch.deletions, baseKnown)
	metadata = domain.SnapshotMetadata{ID: result.SnapshotID, Revision: result.Revision}
	event.OperationID = operationID
	event.SnapshotID = metadata.ID
	event.Revision = metadata.Revision
	obs.snapshotID = string(metadata.ID)
	// Apply post-commit consumers (notably open document overlays) before the
	// public event. Clients may react to workspace.files.changed immediately, so
	// every content-bearing read must already observe the committed change.
	i.publishWorkspaceChange(ctx, buildPublishedWorkspaceChange(operationID, metadata, parsed, batch.deletions, batch.deletionKinds, baseHashes))
	i.publishEvent(event)
	i.markExternalMutations(ctx, batch.externalMutations)
	i.confirmAndPublishSelfWrites(ctx, batch.selfWrites, uint64(result.Revision))
	return nil
}

// scanObservation accumulates the per-scan correlation fields for the deferred
// end-of-scan log line.
type scanObservation struct {
	operationID string
	changeSetID string
	snapshotID  string
	baseVersion uint64
	newVersion  uint64
	changed     int
	removed     int
	step        string
}

func (i *Indexer) confirmAndPublishSelfWrites(ctx context.Context, confirmations []selfWriteConfirmation, storeVersion uint64) bool {
	if len(confirmations) == 0 {
		return false
	}
	confirmed := make([]selfWriteConfirmation, 0, len(confirmations))
	for _, confirmation := range confirmations {
		if i.mutations == nil {
			continue
		}
		if err := i.mutations.MarkObserved(ctx, confirmation.mutationID, 0); err != nil {
			i.logger.Warn("internal self-write confirmation failed", "path", confirmation.path, "error", err)
			continue
		}
		confirmed = append(confirmed, confirmation)
	}
	if len(confirmed) == 0 {
		return false
	}
	i.publishSelfWriteEvent(confirmed, storeVersion)
	return true
}

func (i *Indexer) markExternalMutations(ctx context.Context, confirmations []externalMutationConfirmation) {
	if len(confirmations) == 0 || i.mutations == nil {
		return
	}
	seen := make(map[domain.InternalMutationID]struct{}, len(confirmations))
	for _, confirmation := range confirmations {
		if _, exists := seen[confirmation.mutationID]; exists {
			continue
		}
		seen[confirmation.mutationID] = struct{}{}
		if err := i.mutations.MarkExternal(ctx, confirmation.mutationID); err != nil {
			i.logger.Warn("internal mutation external classification failed", "path", confirmation.path, "error", err)
		}
	}
}

func (i *Indexer) publishSelfWriteEvent(confirmations []selfWriteConfirmation, storeVersion uint64) {
	paths := make([]string, 0, len(confirmations))
	for _, confirmation := range confirmations {
		paths = append(paths, confirmation.path)
	}
	sort.Strings(paths)
	i.publishEvent(domain.IndexEvent{
		Type:         "watch.self_write_confirmed",
		StoreVersion: storeVersion,
		SnapshotID:   i.store.SnapshotID(),
		Revision:     i.store.Revision(),
		Paths:        paths,
		Counts:       map[string]int{"selfWriteConfirmed": len(confirmations)},
		Message:      "Internal write confirmed",
	})
}

func (i *Indexer) publishWorkspaceChange(ctx context.Context, change domain.PublishedWorkspaceChange) {
	if i.changeSink == nil || len(change.Changes) == 0 {
		return
	}
	i.changeSink(ctx, change)
}

func (i *Indexer) indexSize() int {
	_, symbols, _, _, _ := i.store.Counts()
	return symbols
}

// scanErrorCode maps a scan failure to a stable code from its typed cause.
func scanErrorCode(err error) string {
	if appErr, ok := apperror.As(err); ok {
		return string(appErr.Code)
	}
	return "INDEX_SCAN_FAILED"
}

func buildPublishedWorkspaceChange(operationID string, metadata domain.SnapshotMetadata, parsed []domain.ParsedFile, deletions []string, deletionKinds map[string]string, baseHashes map[string]string) domain.PublishedWorkspaceChange {
	changes := make([]domain.PublishedFileChange, 0, len(parsed)+len(deletions))
	for _, file := range parsed {
		oldHash, known := baseHashes[file.File.Path]
		kind := domain.FileChangeAdded
		if known {
			kind = domain.FileChangeModified
		}
		changes = append(changes, domain.PublishedFileChange{
			Path:    file.File.Path,
			Kind:    kind,
			OldHash: oldHash,
			NewHash: file.File.Hash,
			Origin:  domain.FileChangeOriginExternal,
		})
	}
	for _, path := range deletions {
		kind := deletionKinds[path]
		if kind == "" {
			kind = domain.FileChangeRemoved
		}
		changes = append(changes, domain.PublishedFileChange{
			Path:    path,
			Kind:    kind,
			OldHash: baseHashes[path],
			Origin:  domain.FileChangeOriginExternal,
		})
	}
	sort.Slice(changes, func(a, b int) bool { return changes[a].Path < changes[b].Path })
	return domain.PublishedWorkspaceChange{
		OperationID: operationID,
		SnapshotID:  metadata.ID,
		Revision:    metadata.Revision,
		Changes:     changes,
	}
}

// batchEvent builds the aggregated workspace.files.changed envelope with sorted,
// truncated path lists and counts of added/changed/removed files.
func batchEvent(changeSetID string, storeVersion uint64, parsed []domain.ParsedFile, deletions []string, baseKnown map[string]struct{}) domain.IndexEvent {
	const maxPaths = 200
	added, changed := make([]string, 0), make([]string, 0)
	for _, file := range parsed {
		if _, known := baseKnown[file.File.Path]; known {
			changed = append(changed, file.File.Path)
		} else {
			added = append(added, file.File.Path)
		}
	}
	sort.Strings(added)
	sort.Strings(changed)

	union := make([]string, 0, len(added)+len(changed)+len(deletions))
	union = append(union, added...)
	union = append(union, changed...)
	union = append(union, deletions...)
	sort.Strings(union)
	truncated := false
	if len(union) > maxPaths {
		union = union[:maxPaths]
		truncated = true
	}
	counts := map[string]int{
		"added": len(added), "changed": len(changed), "removed": len(deletions),
		"total": len(added) + len(changed) + len(deletions),
	}
	if truncated {
		counts["truncated"] = 1
	}
	return domain.IndexEvent{
		Type: "workspace.files.changed", ChangeSetID: changeSetID, StoreVersion: storeVersion,
		Paths: union, Counts: counts, Message: "Index updated",
	}
}

// collectBatch walks the workspace in canonical order, reads/hashes changed
// files and computes deletions by diffing against the base snapshot. A walk,
// info, read or hash error on a file that should be indexed is fatal for the
// batch, except when the file disappeared during the walk. Those races are
// reconciled as the final on-disk state; no events are published here.
func (i *Indexer) collectBatch(ctx context.Context) (reconcileBatch, error) {
	matcher := loadIgnoreMatcher(i.root)
	currentPaths := make(map[string]struct{})
	candidates := make([]candidate, 0)
	selfWrites := make([]selfWriteConfirmation, 0)
	externalMutations := make([]externalMutationConfirmation, 0)

	walkErr := filepath.WalkDir(i.root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		relative, relErr := filepath.Rel(i.root, path)
		if relErr != nil {
			return relErr
		}
		if relative == "." {
			return nil
		}
		relative = filepath.ToSlash(relative)
		if entry.IsDir() {
			if _, ignored := defaultIgnoredDirectories[entry.Name()]; ignored || matcher.ignored(relative, true) {
				return filepath.SkipDir
			}
			return nil
		}
		if matcher.ignored(relative, false) {
			return nil
		}
		if !supportedPath(relative) {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if !info.Mode().IsRegular() || info.Size() > i.maxFileBytes {
			return nil
		}
		currentPaths[relative] = struct{}{}
		item, readable, err := i.readObservedCandidate(relative, path, info)
		if err != nil {
			if os.IsNotExist(err) {
				delete(currentPaths, relative)
				return nil
			}
			return fmt.Errorf("read %s: %w", relative, err)
		}
		if !readable {
			return nil
		}
		previous, exists := i.store.FileHash(relative)
		selfWrite, externalMutation, err := i.classifyPathObservation(ctx, relative, item.hash, previous, exists)
		if err != nil {
			return err
		}
		if selfWrite.mutationID != "" {
			selfWrites = append(selfWrites, selfWrite)
		}
		if externalMutation.mutationID != "" {
			externalMutations = append(externalMutations, externalMutation)
		}
		if !exists || previous != item.hash {
			candidates = append(candidates, item)
		}
		return nil
	})
	if walkErr != nil {
		return reconcileBatch{}, walkErr
	}

	sort.Slice(candidates, func(a, b int) bool { return candidates[a].relative < candidates[b].relative })

	deletions := make([]string, 0)
	deletionKinds := make(map[string]string)
	for _, known := range i.store.KnownPaths() {
		if _, exists := currentPaths[known]; !exists {
			externalMutation, err := i.classifyExternalObservation(ctx, known, false, "")
			if err != nil {
				return reconcileBatch{}, err
			}
			if externalMutation.mutationID != "" {
				externalMutations = append(externalMutations, externalMutation)
			}
			deletions = append(deletions, known)
			deletionKinds[known] = deletionKindForMissingPath(matcher, known)
		}
	}
	sort.Strings(deletions)
	sort.Slice(selfWrites, func(a, b int) bool { return selfWrites[a].path < selfWrites[b].path })
	sort.Slice(externalMutations, func(a, b int) bool { return externalMutations[a].path < externalMutations[b].path })
	return reconcileBatch{candidates: candidates, deletions: deletions, deletionKinds: deletionKinds, selfWrites: selfWrites, externalMutations: externalMutations}, nil
}

func (i *Indexer) collectPaths(ctx context.Context, paths []string) (reconcileBatch, error) {
	matcher := loadIgnoreMatcher(i.root)
	known := knownPathSet(i.store.KnownPaths())
	candidates := make([]candidate, 0)
	deletions := make([]string, 0)
	deletionKinds := make(map[string]string)
	selfWrites := make([]selfWriteConfirmation, 0)
	externalMutations := make([]externalMutationConfirmation, 0)
	for _, relative := range paths {
		if ctx.Err() != nil {
			return reconcileBatch{}, ctx.Err()
		}
		candidate, changed, deleteKnown, deletionKind, selfWrite, externalMutation, err := i.collectOnePath(ctx, relative, matcher, known)
		if err != nil {
			return reconcileBatch{}, err
		}
		if changed {
			candidates = append(candidates, candidate)
		}
		if deleteKnown {
			deletions = append(deletions, relative)
			deletionKinds[relative] = deletionKind
		}
		if selfWrite.mutationID != "" {
			selfWrites = append(selfWrites, selfWrite)
		}
		if externalMutation.mutationID != "" {
			externalMutations = append(externalMutations, externalMutation)
		}
	}
	sort.Slice(candidates, func(a, b int) bool { return candidates[a].relative < candidates[b].relative })
	sort.Strings(deletions)
	sort.Slice(selfWrites, func(a, b int) bool { return selfWrites[a].path < selfWrites[b].path })
	sort.Slice(externalMutations, func(a, b int) bool { return externalMutations[a].path < externalMutations[b].path })
	return reconcileBatch{candidates: candidates, deletions: deletions, deletionKinds: deletionKinds, selfWrites: selfWrites, externalMutations: externalMutations}, nil
}

func (i *Indexer) collectOnePath(ctx context.Context, relative string, matcher ignoreMatcher, known map[string]struct{}) (candidate, bool, bool, string, selfWriteConfirmation, externalMutationConfirmation, error) {
	if matcher.ignored(relative, false) || !supportedPath(relative) {
		_, exists := known[relative]
		return candidate{}, false, exists, deletionKindForMissingPath(matcher, relative), selfWriteConfirmation{}, externalMutationConfirmation{}, nil
	}
	absolute := filepath.Join(i.root, filepath.FromSlash(relative))
	info, err := os.Stat(absolute)
	if err != nil {
		if os.IsNotExist(err) {
			_, exists := known[relative]
			if exists {
				externalMutation, err := i.classifyExternalObservation(ctx, relative, false, "")
				if err != nil {
					return candidate{}, false, false, "", selfWriteConfirmation{}, externalMutationConfirmation{}, err
				}
				return candidate{}, false, exists, domain.FileChangeRemoved, selfWriteConfirmation{}, externalMutation, nil
			}
			return candidate{}, false, exists, "", selfWriteConfirmation{}, externalMutationConfirmation{}, nil
		}
		return candidate{}, false, false, "", selfWriteConfirmation{}, externalMutationConfirmation{}, err
	}
	if info.IsDir() || !info.Mode().IsRegular() || info.Size() > i.maxFileBytes {
		_, exists := known[relative]
		return candidate{}, false, exists, domain.FileChangeRemoved, selfWriteConfirmation{}, externalMutationConfirmation{}, nil
	}
	item, readable, err := i.readObservedCandidate(relative, absolute, info)
	if err != nil {
		if os.IsNotExist(err) {
			_, exists := known[relative]
			if exists {
				externalMutation, classifyErr := i.classifyExternalObservation(ctx, relative, false, "")
				if classifyErr != nil {
					return candidate{}, false, false, "", selfWriteConfirmation{}, externalMutationConfirmation{}, classifyErr
				}
				return candidate{}, false, true, domain.FileChangeRemoved, selfWriteConfirmation{}, externalMutation, nil
			}
			return candidate{}, false, false, "", selfWriteConfirmation{}, externalMutationConfirmation{}, nil
		}
		return candidate{}, false, false, "", selfWriteConfirmation{}, externalMutationConfirmation{}, err
	}
	if !readable {
		return candidate{}, false, false, "", selfWriteConfirmation{}, externalMutationConfirmation{}, nil
	}
	previous, exists := i.store.FileHash(relative)
	selfWrite, externalMutation, err := i.classifyPathObservation(ctx, relative, item.hash, previous, exists)
	if err != nil {
		return candidate{}, false, false, "", selfWriteConfirmation{}, externalMutationConfirmation{}, err
	}
	if exists && previous == item.hash {
		return candidate{}, false, false, "", selfWrite, externalMutation, nil
	}
	return item, true, false, "", selfWriteConfirmation{}, externalMutation, nil
}

func (i *Indexer) classifyPathObservation(ctx context.Context, relative, contentHash, storeHash string, known bool) (selfWriteConfirmation, externalMutationConfirmation, error) {
	if i.mutations == nil {
		return selfWriteConfirmation{}, externalMutationConfirmation{}, nil
	}
	observation := domain.FileObservation{
		Path:             relative,
		Exists:           true,
		ContentHash:      contentHash,
		StoreContentHash: storeHash,
		StoreRevision:    i.store.Revision(),
		ObservedAt:       i.clock.Now(),
	}
	if !known {
		observation.StoreContentHash = ""
	}
	match, err := i.mutations.Match(ctx, observation)
	if err != nil {
		return selfWriteConfirmation{}, externalMutationConfirmation{}, err
	}
	if match.Matched && match.Classification == domain.ObservationSelfWriteConfirmed {
		return selfWriteConfirmation{path: relative, mutationID: match.MutationID}, externalMutationConfirmation{}, nil
	}
	if match.MutationID != "" && (match.Classification == domain.ObservationExternalChange || match.Classification == domain.ObservationRemoved) {
		return selfWriteConfirmation{}, externalMutationConfirmation{path: relative, mutationID: match.MutationID}, nil
	}
	return selfWriteConfirmation{}, externalMutationConfirmation{}, nil
}

func (i *Indexer) classifyExternalObservation(ctx context.Context, relative string, exists bool, contentHash string) (externalMutationConfirmation, error) {
	if i.mutations == nil {
		return externalMutationConfirmation{}, nil
	}
	storeHash, _ := i.store.FileHash(relative)
	match, err := i.mutations.Match(ctx, domain.FileObservation{
		Path:             relative,
		Exists:           exists,
		ContentHash:      contentHash,
		StoreContentHash: storeHash,
		StoreRevision:    i.store.Revision(),
		ObservedAt:       i.clock.Now(),
	})
	if err != nil {
		return externalMutationConfirmation{}, err
	}
	if match.MutationID != "" && (match.Classification == domain.ObservationExternalChange || match.Classification == domain.ObservationRemoved) {
		return externalMutationConfirmation{path: relative, mutationID: match.MutationID}, nil
	}
	return externalMutationConfirmation{}, nil
}

func deletionKindForMissingPath(matcher ignoreMatcher, relative string) string {
	if matcher.ignored(relative, false) {
		return domain.FileChangeBecameIgnored
	}
	if !supportedPath(relative) {
		return domain.FileChangeBecameUnsupported
	}
	return domain.FileChangeRemoved
}

func (i *Indexer) collectSubtree(ctx context.Context, root string) (reconcileBatch, error) {
	matcher := loadIgnoreMatcher(i.root)
	known := knownPathSet(i.store.KnownPaths())
	currentPaths := make(map[string]struct{})
	candidates := make([]candidate, 0)
	deletionKinds := make(map[string]string)
	selfWrites := make([]selfWriteConfirmation, 0)
	externalMutations := make([]externalMutationConfirmation, 0)
	absoluteRoot := filepath.Join(i.root, filepath.FromSlash(root))

	info, err := os.Stat(absoluteRoot)
	if err != nil {
		if !os.IsNotExist(err) {
			return reconcileBatch{}, err
		}
	} else if info.Mode().IsRegular() {
		batch, err := i.collectPaths(ctx, []string{root})
		if err != nil {
			return reconcileBatch{}, err
		}
		for knownPath := range known {
			if knownPath != root && pathUnderRoot(knownPath, root) {
				externalMutation, err := i.classifyExternalObservation(ctx, knownPath, false, "")
				if err != nil {
					return reconcileBatch{}, err
				}
				if externalMutation.mutationID != "" {
					batch.externalMutations = append(batch.externalMutations, externalMutation)
				}
				batch.deletions = append(batch.deletions, knownPath)
				if batch.deletionKinds == nil {
					batch.deletionKinds = make(map[string]string)
				}
				batch.deletionKinds[knownPath] = domain.FileChangeRemoved
			}
		}
		sort.Strings(batch.deletions)
		sort.Slice(batch.externalMutations, func(a, b int) bool { return batch.externalMutations[a].path < batch.externalMutations[b].path })
		return batch, nil
	} else if info.IsDir() {
		walkErr := filepath.WalkDir(absoluteRoot, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			relative, relErr := filepath.Rel(i.root, path)
			if relErr != nil {
				return relErr
			}
			relative = filepath.ToSlash(relative)
			if entry.IsDir() {
				if relative != "." && (ignoredDirName(entry.Name()) || matcher.ignored(relative, true)) {
					return filepath.SkipDir
				}
				return nil
			}
			if matcher.ignored(relative, false) || !supportedPath(relative) {
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				if os.IsNotExist(err) {
					return nil
				}
				return err
			}
			if !info.Mode().IsRegular() || info.Size() > i.maxFileBytes {
				return nil
			}
			currentPaths[relative] = struct{}{}
			item, readable, err := i.readObservedCandidate(relative, path, info)
			if err != nil {
				if os.IsNotExist(err) {
					delete(currentPaths, relative)
					return nil
				}
				return fmt.Errorf("read %s: %w", relative, err)
			}
			if !readable {
				return nil
			}
			previous, exists := i.store.FileHash(relative)
			selfWrite, externalMutation, err := i.classifyPathObservation(ctx, relative, item.hash, previous, exists)
			if err != nil {
				return err
			}
			if selfWrite.mutationID != "" {
				selfWrites = append(selfWrites, selfWrite)
			}
			if externalMutation.mutationID != "" {
				externalMutations = append(externalMutations, externalMutation)
			}
			if !exists || previous != item.hash {
				candidates = append(candidates, item)
			}
			return nil
		})
		if walkErr != nil {
			return reconcileBatch{}, walkErr
		}
	}

	sort.Slice(candidates, func(a, b int) bool { return candidates[a].relative < candidates[b].relative })
	deletions := make([]string, 0)
	for knownPath := range known {
		if pathUnderRoot(knownPath, root) {
			if _, exists := currentPaths[knownPath]; !exists {
				externalMutation, err := i.classifyExternalObservation(ctx, knownPath, false, "")
				if err != nil {
					return reconcileBatch{}, err
				}
				if externalMutation.mutationID != "" {
					externalMutations = append(externalMutations, externalMutation)
				}
				deletions = append(deletions, knownPath)
				deletionKinds[knownPath] = deletionKindForMissingPath(matcher, knownPath)
			}
		}
	}
	sort.Strings(deletions)
	sort.Slice(selfWrites, func(a, b int) bool { return selfWrites[a].path < selfWrites[b].path })
	sort.Slice(externalMutations, func(a, b int) bool { return externalMutations[a].path < externalMutations[b].path })
	return reconcileBatch{candidates: candidates, deletions: deletions, deletionKinds: deletionKinds, selfWrites: selfWrites, externalMutations: externalMutations}, nil
}

func normalizeRequestedPaths(paths []string) ([]string, error) {
	seen := make(map[string]struct{}, len(paths))
	out := make([]string, 0, len(paths))
	for _, item := range paths {
		normalized, err := normalizeRelativePath(item)
		if err != nil {
			return nil, err
		}
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	sort.Strings(out)
	return out, nil
}

func normalizeSubtreeRoot(root string) (string, error) {
	if strings.TrimSpace(root) == "" || root == "." {
		return ".", nil
	}
	return normalizeRelativePath(root)
}

func normalizeRelativePath(value string) (string, error) {
	return pathutil.NormalizeWorkspaceRelative(value)
}

func knownPathSet(paths []string) map[string]struct{} {
	set := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		set[path] = struct{}{}
	}
	return set
}

func supportedPath(relative string) bool {
	_, _, supported := codeparser.DetectLanguage(relative)
	if supported {
		return true
	}
	_, supported = projectFileSpecFor(relative)
	return supported
}

func ignoredDirName(name string) bool {
	_, ignored := defaultIgnoredDirectories[name]
	return ignored
}

func pathUnderRoot(path, root string) bool {
	if root == "." {
		return true
	}
	return path == root || strings.HasPrefix(path, root+"/")
}

// parseBatch parses every candidate with bounded workers and returns the parsed
// files ordered by path. Per-file parse failures are quarantined: the prior
// indexed version remains available while other files continue to commit.
func (i *Indexer) parseBatch(ctx context.Context, candidates []candidate) ([]domain.ParsedFile, error) {
	if len(candidates) == 0 {
		return nil, nil
	}
	results := i.parseCandidates(ctx, candidates)
	parsed := make([]domain.ParsedFile, 0, len(candidates))
	for result := range results {
		if result.err != nil {
			message := sanitizeIndexError(result.err)
			i.logger.Warn("index file quarantined", "path", result.path, "error", message)
			i.publishEvent(domain.IndexEvent{
				Type: "index.file.quarantined", Path: result.path,
				Error: &domain.PublicError{Code: "SOURCE_PARSE_FAILED", Message: "file kept at the previously indexed version"},
			})
			continue
		}
		parsed = append(parsed, result.parsed)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sort.Slice(parsed, func(a, b int) bool { return parsed[a].File.Path < parsed[b].File.Path })
	return parsed, nil
}

// buildScanChangeSet assembles the batch into a validated ChangeSet. Removed
// symbols' vectors are dropped by the ChangeSet's own delete/replace semantics.
func buildScanChangeSet(baseVersion uint64, parsed []domain.ParsedFile, deletions []string, embeddings map[string][]float64) (*changeset.ChangeSet, error) {
	builder := changeset.NewBuilder().WithExpectedVersion(baseVersion).AllowEmpty()
	for _, file := range parsed {
		builder.Upsert(file)
	}
	for _, path := range deletions {
		builder.Delete(path)
	}
	for id, vector := range embeddings {
		builder.Embed(id, vector)
	}
	return builder.Build(time.Now().UTC())
}
