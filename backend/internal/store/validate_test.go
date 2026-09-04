package store

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
)

func validatorTestRange() domain.Range {
	return domain.Range{Start: domain.Position{Line: 1, Column: 1}, End: domain.Position{Line: 5, Column: 1}}
}

// addIdentityOccurrence seeds the authoritative model with one identity and one
// occurrence (white-box helper), from which finalize derives the flat view.
func addIdentityOccurrence(st *state, identity domain.SymbolIdentity, occurrence domain.SymbolOccurrence) {
	st.identities[identity.ID] = identity
	st.occurrences[occurrence.ID] = occurrence
	st.occByFile[occurrence.Path] = append(st.occByFile[occurrence.Path], occurrence.ID)
	st.occBySymbol[identity.ID] = append(st.occBySymbol[identity.ID], occurrence.ID)
}

// validFinalizedState builds a fully coherent state that must produce zero
// violations of any kind — the no-false-positives baseline.
func validFinalizedState() *state {
	st := newState()
	now := time.Date(2026, 6, 24, 0, 0, 0, 0, time.UTC)
	hash := strings.Repeat("a", 64)
	st.files["pkg/svc.go"] = domain.File{Path: "pkg/svc.go", Language: "go", Hash: hash, Size: 42, ModifiedAt: now, IndexedAt: now}
	addIdentityOccurrence(st,
		domain.SymbolIdentity{ID: "id_file", Language: "go", Kind: "file", Name: "svc.go", QualifiedName: "pkg/svc.go"},
		domain.SymbolOccurrence{ID: "occ_file", SymbolID: "id_file", Path: "pkg/svc.go", Range: domain.Range{Start: domain.Position{Line: 1, Column: 1}, End: domain.Position{Line: 20, Column: 1}}, BodyHash: "h1"})
	addIdentityOccurrence(st,
		domain.SymbolIdentity{ID: "id_fn", Language: "go", Kind: "function", Name: "Submit", QualifiedName: "pkg/svc.go::Submit"},
		domain.SymbolOccurrence{ID: "occ_fn", SymbolID: "id_fn", Path: "pkg/svc.go", Range: domain.Range{Start: domain.Position{Line: 2, Column: 1}, End: domain.Position{Line: 5, Column: 1}}, Signature: "func Submit()", BodyHash: "h2"})
	st.edges = []domain.Edge{{ID: 1, FromSymbolID: "id_file", ToSymbolID: "id_fn", ToName: "Submit", Type: "contains", Path: "pkg/svc.go", Line: 2, Confidence: 0.95}}
	st.nextEdgeID = 2
	st.wiki["overview"] = domain.WikiPage{Slug: "overview", Title: "Overview", Markdown: "# Overview", SourceHash: hash, Provider: "test", UpdatedAt: now}
	st.finalize()
	return st
}

func codes(violations []Violation) map[string]int {
	out := make(map[string]int)
	for _, v := range violations {
		out[v.Code]++
	}
	return out
}

func hasCode(violations []Violation, code string) bool {
	for _, v := range violations {
		if v.Code == code {
			return true
		}
	}
	return false
}

func TestValidateStateValidHasNoViolations(t *testing.T) {
	t.Parallel()
	if violations := ValidateState(validFinalizedState()); len(violations) != 0 {
		t.Fatalf("valid state produced %d violations (want 0): %+v", len(violations), violations)
	}
}

func TestValidateFileInvariants(t *testing.T) {
	t.Parallel()
	st := validFinalizedState()
	// Key mismatch is authoritative; the rest are warnings.
	st.files["wrong-key"] = domain.File{Path: "other.go", Language: "go", Hash: strings.Repeat("a", 64), Size: 1, ModifiedAt: time.Now(), IndexedAt: time.Now()}
	st.files["vendor/x.go"] = domain.File{Path: "vendor/x.go", Language: "go", Hash: "not-a-hash", Size: -5}

	v := ValidateState(st)
	for _, code := range []string{ViolationFileKeyMismatch, ViolationFilePathForbidden, ViolationFileHashFormat, ViolationFileSizeNegative, ViolationFileTimestamp} {
		if !hasCode(v, code) {
			t.Errorf("missing expected file violation %s; got %v", code, codes(v))
		}
	}
	if !HasBlockingViolations(v) {
		t.Error("file key mismatch should be blocking")
	}
}

func TestValidateSymbolInvariants(t *testing.T) {
	t.Parallel()
	st := newState()
	st.files["a.go"] = domain.File{Path: "a.go", Language: "go", Hash: strings.Repeat("b", 64), Size: 1, ModifiedAt: time.Now(), IndexedAt: time.Now()}
	st.symbols["s_empty"] = domain.Symbol{ID: "", Path: "a.go", Name: "X", Kind: "function", Language: "go", Range: validatorTestRange()}
	st.symbols["s_orphan"] = domain.Symbol{ID: "s_orphan", Path: "ghost.go", Name: "Y", Kind: "function", Language: "go", Range: validatorTestRange()}
	st.symbols["s_lang"] = domain.Symbol{ID: "s_lang", Path: "a.go", Name: "Z", Kind: "function", Language: "python", Range: validatorTestRange()}
	st.symbols["s_range"] = domain.Symbol{ID: "s_range", Path: "a.go", Name: "W", Kind: "function", Language: "go", Range: domain.Range{Start: domain.Position{Line: 9, Column: 1}, End: domain.Position{Line: 3, Column: 1}}}
	st.rebuildLexical() // validate the directly-injected flat view (finalize would derive it away)

	v := ValidateState(st)
	if !hasCode(v, ViolationSymbolIDEmpty) || !hasCode(v, ViolationSymbolKeyMismatch) {
		t.Errorf("expected empty-ID + key-mismatch violations; got %v", codes(v))
	}
	if !hasCode(v, ViolationSymbolOrphan) {
		t.Errorf("expected orphan-symbol violation; got %v", codes(v))
	}
	if !hasCode(v, ViolationSymbolLanguage) || !hasCode(v, ViolationSymbolRange) {
		t.Errorf("expected language + range warnings; got %v", codes(v))
	}
	if !HasBlockingViolations(v) {
		t.Error("orphan symbol should be blocking")
	}
}

func TestValidateEdgeInvariants(t *testing.T) {
	t.Parallel()
	st := validFinalizedState()
	st.edges = append(st.edges,
		domain.Edge{ID: 2, FromSymbolID: "missing", ToName: "x", Type: "calls", Path: "pkg/svc.go", Confidence: 0.5},
		domain.Edge{ID: 3, FromSymbolID: "id_file", ToSymbolID: "", ToName: "", Type: "calls", Path: "pkg/svc.go", Confidence: 0.5},
		domain.Edge{ID: 4, FromSymbolID: "id_file", ToName: "y", Type: "weird", Path: "nowhere.go", Confidence: 0.5},
		domain.Edge{ID: 5, FromSymbolID: "id_file", ToName: "z", Type: "calls", Path: "pkg/svc.go", Confidence: math.NaN()},
		domain.Edge{ID: 6, FromSymbolID: "id_file", ToName: "w", Type: "calls", Path: "pkg/svc.go", Confidence: 1.5},
	)

	v := ValidateState(st)
	for _, code := range []string{ViolationEdgeFromMissing, ViolationEdgeUnresolved, ViolationEdgeType, ViolationEdgePathMissing, ViolationEdgeConfidenceInvalid} {
		if !hasCode(v, code) {
			t.Errorf("missing expected edge violation %s; got %v", code, codes(v))
		}
	}
	if !HasBlockingViolations(v) {
		t.Error("NaN/out-of-range confidence should be blocking")
	}
}

func TestValidateWikiInvariants(t *testing.T) {
	t.Parallel()
	st := validFinalizedState()
	st.wiki["mismatch"] = domain.WikiPage{Slug: "different", Title: "", Provider: "", UpdatedAt: time.Time{}}

	v := ValidateState(st)
	for _, code := range []string{ViolationWikiKeyMismatch, ViolationWikiTitleEmpty, ViolationWikiProvider, ViolationWikiTimestamp} {
		if !hasCode(v, code) {
			t.Errorf("missing expected wiki violation %s; got %v", code, codes(v))
		}
	}
	if !HasBlockingViolations(v) {
		t.Error("wiki key mismatch should be blocking")
	}
}

func TestValidateDerivedLexicalIsRepairable(t *testing.T) {
	t.Parallel()
	st := validFinalizedState()
	// Inject a stale posting and a stale docLen entry for a non-existent symbol.
	st.lexical.postings["submit"]["ghost_symbol"] = 1
	st.lexical.docLen["ghost_symbol"] = 3

	v := ValidateState(st)
	if !hasCode(v, ViolationLexicalOrphan) {
		t.Fatalf("expected lexical orphan violation; got %v", codes(v))
	}
	if HasBlockingViolations(v) {
		t.Fatal("lexical drift must be repairable, not blocking")
	}
	for _, viol := range v {
		if viol.Code == ViolationLexicalOrphan && !viol.Repairable {
			t.Fatal("lexical orphan must be marked repairable")
		}
	}
}

func TestValidateViolationCapIsRespected(t *testing.T) {
	t.Parallel()
	st := newState()
	const want = 1100
	for i := 0; i < want; i++ {
		id := fmt.Sprintf("s%d", i)
		st.symbols[id] = domain.Symbol{ID: id, Path: "ghost.go", Name: "X", Kind: "function", Language: "go", Range: validatorTestRange()}
	}
	st.rebuildLexical()

	v := ValidateState(st)
	if len(v) != maxViolations+1 {
		t.Fatalf("returned %d violations, want %d (cap + truncation marker)", len(v), maxViolations+1)
	}
	last := v[len(v)-1]
	if last.Code != ViolationTruncated || !strings.Contains(last.Message, fmt.Sprintf("%d", want)) {
		t.Fatalf("expected truncation marker reporting %d total, got %+v", want, last)
	}
}

func TestLoadBlocksOnAuthoritativeCorruption(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "index.json")
	snapshot := diskSnapshot{
		Version:        snapshotVersion,
		SnapshotSchema: snapshotSchema,
		StoreVersion:   1,
		Identities:     []domain.SymbolIdentity{{ID: "s1", Language: "go", Kind: "function", Name: "X", QualifiedName: "ghost.X"}},
		Occurrences:    []domain.SymbolOccurrence{{ID: "occ1", SymbolID: "s1", Path: "ghost.go", Range: validatorTestRange(), BodyHash: "h"}},
	}
	data, _ := json.MarshalIndent(snapshot, "", "  ")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Open(path)
	if err == nil {
		t.Fatal("Open accepted a snapshot with an orphan symbol")
	}
	if !strings.Contains(err.Error(), ViolationSymbolOrphan) {
		t.Fatalf("error = %q, want it to name %s", err.Error(), ViolationSymbolOrphan)
	}
}

func TestStageCommitRejectsInvalidCandidate(t *testing.T) {
	t.Parallel()
	repository, err := Open(filepath.Join(t.TempDir(), "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	corrupt := newState()
	corrupt.files["a.go"] = domain.File{Path: "a.go", Language: "go"}
	addIdentityOccurrence(corrupt,
		domain.SymbolIdentity{ID: "s1", Language: "go", Kind: "function", Name: "X", QualifiedName: "ghost.X"},
		domain.SymbolOccurrence{ID: "occ1", SymbolID: "s1", Path: "ghost.go", Range: validatorTestRange(), BodyHash: "h"})
	corrupt.finalize()
	corrupt.version = 1

	prepared := &PreparedCommit{ChangeSetID: "x", ExpectedVersion: 0, NextVersion: 1, candidate: corrupt}
	if _, err := repository.StageCommit(prepared); err == nil {
		t.Fatal("StageCommit accepted an authoritatively-invalid candidate")
	}
}

func TestValidateConcurrentWithMutations(t *testing.T) {
	t.Parallel()
	repository, err := Open(filepath.Join(t.TempDir(), "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for n := 0; n < 4; n++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for j := 0; j < 40; j++ {
				repository.ReplaceFile(parsed(fmt.Sprintf("pkg/f%d_%d.go", worker, j)))
				if j%3 == 0 {
					repository.DeleteFile(fmt.Sprintf("pkg/f%d_%d.go", worker, j))
				}
			}
		}(n)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for j := 0; j < 200; j++ {
			_ = repository.Validate()
		}
	}()
	wg.Wait()
}

func FuzzDecodeAndValidate(f *testing.F) {
	f.Add([]byte(`{"version":3}`))
	f.Add([]byte(`{"version":3,"symbols":[{"id":"s","path":"a.go"}],"edges":[{"id":1,"type":"calls"}]}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		var snapshot diskSnapshot
		if err := json.Unmarshal(data, &snapshot); err != nil {
			return
		}
		st := newState()
		for _, file := range snapshot.Files {
			st.files[file.Path] = file
		}
		for _, symbol := range snapshot.Symbols {
			st.symbols[symbol.ID] = symbol
		}
		st.edges = snapshot.Edges
		for _, page := range snapshot.Wiki {
			st.wiki[page.Slug] = page
		}
		st.rebuildLexical()

		violations := ValidateState(st)
		if len(violations) > maxViolations+1 {
			t.Fatalf("violations exceeded cap: %d", len(violations))
		}
	})
}
