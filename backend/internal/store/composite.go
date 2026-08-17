package store

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
)

// ErrDocumentBaseStale means the active snapshot advanced AND the overlay's file
// itself changed on disk since the overlay was opened, so the unsaved buffer must
// not be composited with the new index. The caller maps it to a 409.
var ErrDocumentBaseStale = errors.New("store: overlay base is stale relative to the active snapshot")

// OverlayContext identifies one ephemeral, unsaved overlay being composited over
// a persisted snapshot.
type OverlayContext struct {
	DocumentID      string
	Path            string
	Version         int64
	ContentHash     string
	BaseContentHash string
	BaseSnapshotID  domain.SnapshotID
}

// CompositeReadView is a ReadView with exactly one file replaced by an unsaved
// overlay parse. It is never persisted and never becomes the store's current
// state. ViewHash identifies the combination deterministically; it is NOT a
// SnapshotID, because this view was never committed.
type CompositeReadView interface {
	ReadView
	ViewHash() string
	Overlay() OverlayContext
	Rebased() bool
}

type compositeView struct {
	*readView
	viewHash string
	overlay  OverlayContext
	rebased  bool
}

func (c *compositeView) ViewHash() string        { return c.viewHash }
func (c *compositeView) Overlay() OverlayContext { return c.overlay }
func (c *compositeView) Rebased() bool           { return c.rebased }

// CompositeView builds a non-persisted ReadView where the overlay's ephemeral
// ParsedFile replaces the persisted version of overlay.Path, while every other
// file stays at the active snapshot. The identity/occurrence/edge derivation runs
// through the same pipeline as a persisted ReplaceFile (on an isolated clone, so
// the Store is never mutated).
//
// If the active snapshot advanced past the overlay base, it rebases onto the
// active snapshot only when the path's persisted content hash still equals the
// overlay base hash (i.e. the change was to other files); otherwise it returns
// ErrDocumentBaseStale and never mixes the overlay with a changed index file.
func (s *Store) CompositeView(ephemeral domain.ParsedFile, overlay OverlayContext) (CompositeReadView, error) {
	s.mu.RLock()
	base := s.current.clone()
	meta := s.metadataLocked()
	persisted, exists := s.current.files[overlay.Path]
	s.mu.RUnlock()

	rebased := false
	if overlay.BaseSnapshotID != "" && overlay.BaseSnapshotID != meta.ID {
		// The global snapshot advanced since the overlay opened. Rebasing is only
		// safe if THIS file did not change in the index (its persisted hash still
		// matches the overlay base); otherwise the unsaved buffer is stale.
		if exists && persisted.Hash != "" && overlay.BaseContentHash != "" && persisted.Hash != overlay.BaseContentHash {
			return nil, ErrDocumentBaseStale
		}
		rebased = true
	}

	base.replaceFile(ephemeral)
	base.finalize()
	return &compositeView{
		readView: &readView{st: base, meta: meta},
		viewHash: computeViewHash(meta.ID, overlay),
		overlay:  overlay,
		rebased:  rebased,
	}, nil
}

// computeViewHash is deterministic over the base SnapshotID and the overlay's
// document id, path, version and content hash. It is intentionally NOT formatted
// as a SnapshotID.
func computeViewHash(snapshotID domain.SnapshotID, overlay OverlayContext) string {
	digest := sha256.New()
	fmt.Fprintf(digest, "%s\x00%s\x00%s\x00%d\x00%s", snapshotID, overlay.DocumentID, overlay.Path, overlay.Version, overlay.ContentHash)
	return "view:" + hex.EncodeToString(digest.Sum(nil))
}
