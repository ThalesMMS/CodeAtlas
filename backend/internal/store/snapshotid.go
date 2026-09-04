package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	"github.com/ThalesMMS/CodeAtlas/internal/symbols"
)

// snapshotSchema is the version of the canonical-hash definition. Bumping it (for
// a parser/schema/template change that alters the meaning of the index)
// invalidates every previously computed SnapshotID.
const snapshotSchema = 5

// canonicalSnapshot is the deterministic, ordered projection of the authoritative
// state that is hashed into a SnapshotID. It intentionally excludes volatile and
// derived data: timestamps, counters, map order, wiki/codemap artifacts,
// readiness, request/operation ids and edge identity (edge ids are allocated
// monotonically and are not content).
type canonicalSnapshot struct {
	Schema            int                   `json:"schema"`
	IdentityAlgorithm string                `json:"identityAlgorithm"`
	Files             []canonicalFile       `json:"files"`
	Identities        []canonicalIdentity   `json:"identities"`
	Occurrences       []canonicalOccurrence `json:"occurrences"`
	Edges             []canonicalEdge       `json:"edges"`
}

type canonicalFile struct {
	Path string `json:"path"`
	Hash string `json:"hash"`
}

type canonicalIdentity struct {
	ID                   string `json:"id"`
	Language             string `json:"language"`
	Kind                 string `json:"kind"`
	Name                 string `json:"name"`
	QualifiedName        string `json:"qualifiedName"`
	ParentID             string `json:"parentId"`
	SignatureFingerprint string `json:"signatureFingerprint"`
}

type canonicalOccurrence struct {
	ID          string `json:"id"`
	SymbolID    string `json:"symbolId"`
	Path        string `json:"path"`
	Range       string `json:"range"`
	Signature   string `json:"signature"`
	BodyHash    string `json:"bodyHash"`
	DocHash     string `json:"docHash"`
	SummaryHash string `json:"summaryHash"`
}

type canonicalEdge struct {
	Type         string `json:"type"`
	FromSymbolID string `json:"from"`
	ToSymbolID   string `json:"to"`
	ToName       string `json:"toName"`
	Path         string `json:"path"`
	Line         int    `json:"line"`
	Confidence   string `json:"confidence"`
}

// computeSnapshotID builds the canonical representation, serializes it with the
// standard JSON encoder (stable for ordered slices and these scalar types) and
// returns the SHA-256 as "sha256:<hex>".
func computeSnapshotID(st *state) domain.SnapshotID {
	canonical := canonicalSnapshot{Schema: snapshotSchema, IdentityAlgorithm: symbols.IdentityAlgorithmVersion}

	canonical.Files = make([]canonicalFile, 0, len(st.files))
	for _, file := range st.files {
		canonical.Files = append(canonical.Files, canonicalFile{Path: filepath.ToSlash(file.Path), Hash: file.Hash})
	}
	sort.Slice(canonical.Files, func(i, j int) bool { return canonical.Files[i].Path < canonical.Files[j].Path })

	canonical.Identities = make([]canonicalIdentity, 0, len(st.identities))
	for _, identity := range st.identities {
		canonical.Identities = append(canonical.Identities, canonicalIdentity{
			ID: string(identity.ID), Language: identity.Language, Kind: identity.Kind, Name: identity.Name,
			QualifiedName: identity.QualifiedName, ParentID: string(identity.ParentID), SignatureFingerprint: identity.SignatureFingerprint,
		})
	}
	sort.Slice(canonical.Identities, func(i, j int) bool { return canonical.Identities[i].ID < canonical.Identities[j].ID })

	canonical.Occurrences = make([]canonicalOccurrence, 0, len(st.occurrences))
	for _, occurrence := range st.occurrences {
		canonical.Occurrences = append(canonical.Occurrences, canonicalOccurrence{
			ID: string(occurrence.ID), SymbolID: string(occurrence.SymbolID), Path: filepath.ToSlash(occurrence.Path),
			Range: rangeKey(occurrence.Range), Signature: occurrence.Signature,
			BodyHash: occurrence.BodyHash, DocHash: contentHash(occurrence.DocComment), SummaryHash: contentHash(occurrence.Summary),
		})
	}
	sort.Slice(canonical.Occurrences, func(i, j int) bool { return canonical.Occurrences[i].ID < canonical.Occurrences[j].ID })

	canonical.Edges = make([]canonicalEdge, 0, len(st.edges))
	for _, edge := range st.edges {
		canonical.Edges = append(canonical.Edges, canonicalEdge{
			Type: edge.Type, FromSymbolID: edge.FromSymbolID, ToSymbolID: edge.ToSymbolID, ToName: edge.ToName,
			Path: filepath.ToSlash(edge.Path), Line: edge.Line, Confidence: strconv.FormatFloat(edge.Confidence, 'g', -1, 64),
		})
	}
	sort.Slice(canonical.Edges, func(i, j int) bool { return canonicalEdgeLess(canonical.Edges[i], canonical.Edges[j]) })

	encoded, err := json.Marshal(canonical)
	if err != nil {
		// canonicalSnapshot holds only marshalable scalars/slices; this never fails.
		return domain.SnapshotID("sha256:" + hex.EncodeToString(sha256.New().Sum(nil)))
	}
	digest := sha256.Sum256(encoded)
	return domain.SnapshotID("sha256:" + hex.EncodeToString(digest[:]))
}

func rangeKey(r domain.Range) string {
	return strconv.Itoa(r.Start.Line) + ":" + strconv.Itoa(r.Start.Column) + "-" +
		strconv.Itoa(r.End.Line) + ":" + strconv.Itoa(r.End.Column)
}

func contentHash(value string) string {
	if value == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func canonicalEdgeLess(a, b canonicalEdge) bool {
	if a.FromSymbolID != b.FromSymbolID {
		return a.FromSymbolID < b.FromSymbolID
	}
	if a.ToSymbolID != b.ToSymbolID {
		return a.ToSymbolID < b.ToSymbolID
	}
	if a.ToName != b.ToName {
		return a.ToName < b.ToName
	}
	if a.Type != b.Type {
		return a.Type < b.Type
	}
	if a.Path != b.Path {
		return a.Path < b.Path
	}
	return a.Line < b.Line
}
