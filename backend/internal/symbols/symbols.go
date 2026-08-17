// Package symbols implements the versioned SymbolIdentity algorithm (v1) and the
// occurrence model. Identity is derived only from logical fields — never line,
// range, body, summary, timestamp, discovery order or SnapshotID — so it is
// stable across line shifts and body edits. The OccurrenceID is computed
// separately and may include the range, because it represents location, not
// logical identity. Parser output and both stores use this model.
package symbols

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
)

// IdentityAlgorithmVersion is the version of the identity-key definition. It is
// recorded in the snapshot; any change requires an explicit migration/rebuild.
const IdentityAlgorithmVersion = "v1"

// OccurrenceAlgorithmVersion changes when occurrence textual evidence affects
// physical identity. v2 adds the raw doc-comment hash.
const OccurrenceAlgorithmVersion = "v2"

const (
	symbolIDPrefix     = "sym:" + IdentityAlgorithmVersion + ":"
	occurrenceIDPrefix = "occ:" + OccurrenceAlgorithmVersion + ":"
	anonOccurrencePref = "occ:anon:" + OccurrenceAlgorithmVersion + ":"
)

// identityKey is the canonical, ordered set of logical fields hashed into a
// SymbolID. Two symbols with an identical key are the same logical identity.
type identityKey struct {
	algorithm     string
	language      string
	namespace     string
	parentID      domain.SymbolID
	kind          string
	name          string
	discriminator string // overload discriminator; empty unless a real collision needs it
}

func (k identityKey) canonical() string {
	return strings.Join([]string{
		k.algorithm, k.language, k.namespace, string(k.parentID), k.kind, k.name, k.discriminator,
	}, "\x00")
}

func (k identityKey) id() domain.SymbolID {
	digest := sha256.Sum256([]byte(k.canonical()))
	return domain.SymbolID(symbolIDPrefix + hex.EncodeToString(digest[:16]))
}

// namespace derives the logical namespace from the path. A file symbol is
// path-specific; a Go declaration belongs to its package directory (so a
// same-package file move preserves identity); a JS/TS symbol belongs to its
// module (the file without extension).
func namespace(language, kind, path string) string {
	clean := filepath.ToSlash(path)
	if kind == domain.KindFile {
		return clean
	}
	if language == "go" {
		dir := filepath.ToSlash(filepath.Dir(clean))
		if dir == "." {
			dir = ""
		}
		return dir
	}
	return strings.TrimSuffix(clean, filepath.Ext(clean))
}

// normalizeName strips a pointer receiver and surrounding whitespace so pointer
// and value receiver methods share one identity.
func normalizeName(qualifiedName, name string) string {
	source := qualifiedName
	if source == "" {
		source = name
	}
	return strings.TrimSpace(strings.ReplaceAll(source, "*", ""))
}

func baseKey(symbol domain.Symbol, parentID domain.SymbolID) identityKey {
	return identityKey{
		algorithm: IdentityAlgorithmVersion,
		language:  symbol.Language,
		namespace: namespace(symbol.Language, symbol.Kind, symbol.Path),
		parentID:  parentID,
		kind:      symbol.Kind,
		name:      normalizeName(symbol.QualifiedName, symbol.Name),
	}
}

var whitespace = regexp.MustCompile(`\s+`)

// SignatureFingerprint normalizes a displayed signature (collapsing whitespace,
// dropping the body) into a stable fingerprint. It is kept separate from the
// SymbolID and only feeds the overload discriminator when needed.
func SignatureFingerprint(signature string) string {
	if signature == "" {
		return ""
	}
	normalized := whitespace.ReplaceAllString(strings.TrimSpace(signature), " ")
	if brace := strings.IndexByte(normalized, '{'); brace >= 0 {
		normalized = strings.TrimSpace(normalized[:brace]) // drop the body
	}
	digest := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(digest[:8])
}

// BodyHash is the content hash of the occurrence body.
func BodyHash(code string) string {
	digest := sha256.Sum256([]byte(code))
	return hex.EncodeToString(digest[:])
}

// OccurrenceID is deterministic from non-circular data: the SymbolID, path,
// range, body hash, displayed signature and raw documentation text. It never
// embeds the SnapshotID.
func OccurrenceID(symbolID domain.SymbolID, path string, r domain.Range, bodyHash, signatureDisplay, docComment string) domain.OccurrenceID {
	docHash := BodyHash(docComment)
	payload := strings.Join([]string{string(symbolID), filepath.ToSlash(path), rangeKey(r), bodyHash, signatureDisplay, docHash}, "\x00")
	digest := sha256.Sum256([]byte(payload))
	prefix := occurrenceIDPrefix
	if symbolID == "" {
		prefix = anonOccurrencePref // an occurrence-only symbol's handle
	}
	return domain.OccurrenceID(prefix + hex.EncodeToString(digest[:12]))
}

// Input is one symbol to resolve, with its already-resolved parent identity and
// the hash of its containing file.
type Input struct {
	Symbol   domain.Symbol
	ParentID domain.SymbolID
	FileHash string
}

// Resolver assigns identities and detects hash collisions. A hash collision
// (different canonical keys producing the same SymbolID) is a fatal error, never
// an overwrite.
type Resolver struct {
	keyByID map[domain.SymbolID]string
}

func NewResolver() *Resolver {
	return &Resolver{keyByID: make(map[domain.SymbolID]string)}
}

// ResolveBatch resolves a file's symbols together, so overload siblings (same
// base key) get a deterministic discriminator. Order does not affect the result.
func (r *Resolver) ResolveBatch(inputs []Input) ([]domain.ResolvedSymbol, error) {
	// Group identity-bearing symbols by base key to find overload collisions.
	type member struct {
		index       int
		fingerprint string
	}
	groups := make(map[string][]member)
	keys := make([]identityKey, len(inputs))
	occurrenceOnly := make([]bool, len(inputs))
	for i, input := range inputs {
		occurrenceOnly[i] = domain.IsOccurrenceOnly(input.Symbol.Kind, input.Symbol.Name, input.Symbol.QualifiedName)
		if occurrenceOnly[i] {
			continue
		}
		keys[i] = baseKey(input.Symbol, input.ParentID)
		canonical := keys[i].canonical()
		groups[canonical] = append(groups[canonical], member{index: i, fingerprint: SignatureFingerprint(input.Symbol.Signature)})
	}

	// A group with more than one distinct signature is an overload set; each
	// distinct signature becomes a discriminator. Identical signatures (duplicate
	// declaration / interface merge) keep one shared identity.
	for _, members := range groups {
		distinct := make(map[string]struct{})
		for _, m := range members {
			distinct[m.fingerprint] = struct{}{}
		}
		if len(distinct) <= 1 {
			continue
		}
		for _, m := range members {
			keys[m.index].discriminator = m.fingerprint
		}
	}

	resolved := make([]domain.ResolvedSymbol, len(inputs))
	for i, input := range inputs {
		var identity domain.SymbolIdentity
		var symbolID domain.SymbolID
		if occurrenceOnly[i] {
			// A display label only (no logical id): keeps the symbol nameable in
			// flat DTOs while the empty SymbolID marks it occurrence-only.
			identity = domain.SymbolIdentity{
				Language: input.Symbol.Language, Kind: input.Symbol.Kind,
				Name: input.Symbol.Name, QualifiedName: input.Symbol.QualifiedName,
			}
		} else {
			symbolID = keys[i].id()
			if err := r.register(symbolID, keys[i].canonical()); err != nil {
				return nil, err
			}
			identity = domain.SymbolIdentity{
				ID: symbolID, Language: input.Symbol.Language, Kind: input.Symbol.Kind,
				Name: input.Symbol.Name, QualifiedName: input.Symbol.QualifiedName,
				ParentID: input.ParentID, SignatureFingerprint: SignatureFingerprint(input.Symbol.Signature),
			}
		}
		bodyHash := BodyHash(input.Symbol.Code)
		resolved[i] = domain.ResolvedSymbol{
			Identity: identity,
			Occurrence: domain.SymbolOccurrence{
				ID:             OccurrenceID(symbolID, input.Symbol.Path, input.Symbol.Range, bodyHash, input.Symbol.Signature, input.Symbol.DocComment),
				SymbolID:       symbolID,
				OccurrenceOnly: occurrenceOnly[i],
				Path:           input.Symbol.Path,
				Range:          input.Symbol.Range,
				Signature:      input.Symbol.Signature,
				Code:           input.Symbol.Code,
				DocComment:     input.Symbol.DocComment,
				Summary:        input.Symbol.Summary,
				BodyHash:       bodyHash,
				FileHash:       input.FileHash,
			},
		}
	}
	return resolved, nil
}

// ResolveHierarchy resolves a flat parser batch in logical parent order and
// returns stable handles for remapping edges. Parser-local input handles may be
// arbitrary and never become part of SymbolIdentity.
func ResolveHierarchy(flat []domain.Symbol, edges []domain.Edge, fileHash string) ([]domain.ResolvedSymbol, map[string]string, error) {
	indexByHandle := make(map[string]int, len(flat))
	for index, symbol := range flat {
		if symbol.ID == "" {
			return nil, nil, fmt.Errorf("symbols: parser symbol %d has no local handle", index)
		}
		if _, exists := indexByHandle[symbol.ID]; exists {
			return nil, nil, fmt.Errorf("symbols: duplicate parser handle %q", symbol.ID)
		}
		indexByHandle[symbol.ID] = index
	}
	parentByHandle := make(map[string]string)
	for _, edge := range edges {
		if edge.Type != "contains" || edge.ToSymbolID == "" {
			continue
		}
		parentIndex, parentExists := indexByHandle[edge.FromSymbolID]
		if _, childExists := indexByHandle[edge.ToSymbolID]; parentExists && childExists && flat[parentIndex].Kind != domain.KindFile {
			parentByHandle[edge.ToSymbolID] = edge.FromSymbolID
		}
	}

	resolved := make([]domain.ResolvedSymbol, len(flat))
	resolvedIndex := make([]bool, len(flat))
	stableIDByHandle := make(map[string]domain.SymbolID, len(flat))
	handleByInput := make(map[string]string, len(flat))
	resolver := NewResolver()
	remaining := len(flat)
	for remaining > 0 {
		inputs := make([]Input, 0, remaining)
		indexes := make([]int, 0, remaining)
		for index, symbol := range flat {
			if resolvedIndex[index] {
				continue
			}
			parentHandle := parentByHandle[symbol.ID]
			parentID, parentResolved := stableIDByHandle[parentHandle]
			if parentHandle != "" && !parentResolved {
				continue
			}
			inputs = append(inputs, Input{Symbol: symbol, ParentID: parentID, FileHash: fileHash})
			indexes = append(indexes, index)
		}
		if len(inputs) == 0 {
			return nil, nil, errors.New("symbols: cyclic or unresolved parent hierarchy")
		}
		batch, err := resolver.ResolveBatch(inputs)
		if err != nil {
			return nil, nil, err
		}
		for offset, item := range batch {
			index := indexes[offset]
			inputHandle := flat[index].ID
			resolved[index] = item
			resolvedIndex[index] = true
			stableIDByHandle[inputHandle] = item.Identity.ID
			handleByInput[inputHandle] = Handle(item)
			remaining--
		}
	}
	return resolved, handleByInput, nil
}

// Handle is the stable flat/edge handle for a resolved symbol.
func Handle(resolved domain.ResolvedSymbol) string {
	if resolved.Occurrence.SymbolID != "" {
		return string(resolved.Occurrence.SymbolID)
	}
	return string(resolved.Occurrence.ID)
}

// IsCanonicalParserBatch reports whether every parser symbol carries the
// identity/occurrence handles emitted by the v1 parser boundary.
func IsCanonicalParserBatch(flat []domain.Symbol) bool {
	if len(flat) == 0 {
		return false
	}
	for _, symbol := range flat {
		if symbol.OccurrenceID == "" {
			return false
		}
		if !strings.HasPrefix(symbol.ID, symbolIDPrefix) && !strings.HasPrefix(symbol.ID, anonOccurrencePref) {
			return false
		}
	}
	return true
}

// ResolveCanonicalBatch reconstructs authoritative identities/occurrences from
// canonical flat parser output without using SymbolID as a unique occurrence
// key. Duplicate declarations therefore retain multiple occurrences.
func ResolveCanonicalBatch(flat []domain.Symbol, edges []domain.Edge, fileHash string) ([]domain.ResolvedSymbol, map[string]string, error) {
	kindByHandle := make(map[string]string, len(flat))
	for _, symbol := range flat {
		kindByHandle[symbol.ID] = symbol.Kind
	}
	parentByHandle := make(map[string]domain.SymbolID)
	for _, edge := range edges {
		parentKind, parentExists := kindByHandle[edge.FromSymbolID]
		if edge.Type == "contains" && edge.ToSymbolID != "" && parentExists && parentKind != domain.KindFile {
			parentByHandle[edge.ToSymbolID] = domain.SymbolID(edge.FromSymbolID)
		}
	}
	inputs := make([]Input, len(flat))
	for index, symbol := range flat {
		inputs[index] = Input{Symbol: symbol, ParentID: parentByHandle[symbol.ID], FileHash: fileHash}
	}
	resolved, err := NewResolver().ResolveBatch(inputs)
	if err != nil {
		return nil, nil, err
	}
	ordered := make([]domain.ResolvedSymbol, 0, len(resolved))
	inserted := make(map[domain.SymbolID]struct{}, len(resolved))
	used := make([]bool, len(resolved))
	for len(ordered) < len(resolved) {
		progress := false
		for index, item := range resolved {
			if used[index] {
				continue
			}
			parentID := item.Identity.ParentID
			if parentID != "" {
				if _, ready := inserted[parentID]; !ready {
					continue
				}
			}
			ordered = append(ordered, item)
			used[index] = true
			if item.Identity.ID != "" {
				inserted[item.Identity.ID] = struct{}{}
			}
			progress = true
		}
		if !progress {
			return nil, nil, errors.New("symbols: cyclic or missing canonical parent")
		}
	}
	handles := make(map[string]string, len(flat))
	for index, item := range resolved {
		handles[flat[index].ID] = Handle(item)
	}
	return ordered, handles, nil
}

// register records an id→key mapping, returning a fatal error on a hash collision
// (same id, different canonical key). A repeated identical key is fine (duplicate
// declaration / interface merge → one identity, many occurrences).
func (r *Resolver) register(id domain.SymbolID, key string) error {
	if existing, ok := r.keyByID[id]; ok {
		return validateNoCollision(existing, key)
	}
	r.keyByID[id] = key
	return nil
}

// validateNoCollision reports a fatal SymbolID collision when two different
// canonical keys hash to the same id.
func validateNoCollision(existingKey, newKey string) error {
	if existingKey != newKey {
		return &CollisionError{ExistingKey: existingKey, NewKey: newKey}
	}
	return nil
}

// CollisionError is the fatal error raised when two distinct canonical keys
// produce the same SymbolID. It is never resolved by overwriting.
type CollisionError struct {
	ExistingKey string
	NewKey      string
}

func (e *CollisionError) Error() string {
	return "SYMBOL_ID_COLLISION: distinct canonical keys hashed to the same SymbolID"
}

// Resolve resolves a single symbol with a fresh resolver. Use ResolveBatch for a
// whole file so overloads and collisions are handled across siblings.
func Resolve(symbol domain.Symbol, parentID domain.SymbolID, fileHash string) domain.ResolvedSymbol {
	resolved, _ := NewResolver().ResolveBatch([]Input{{Symbol: symbol, ParentID: parentID, FileHash: fileHash}})
	return resolved[0]
}

func rangeKey(r domain.Range) string {
	return strconv.Itoa(r.Start.Line) + ":" + strconv.Itoa(r.Start.Column) + "-" +
		strconv.Itoa(r.End.Line) + ":" + strconv.Itoa(r.End.Column)
}
