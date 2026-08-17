package symbols

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ThalesMMS/CodeAtlas/internal/domain"
)

func sym(kind, name, qualified, path, code string, startLine int) domain.Symbol {
	return domain.Symbol{
		Language: "go", Kind: kind, Name: name, QualifiedName: qualified, Path: path, Code: code,
		Range: domain.Range{Start: domain.Position{Line: startLine, Column: 1}, End: domain.Position{Line: startLine + 2, Column: 1}},
	}
}

func TestIdentitySurvivesLineShiftAndBodyEdit(t *testing.T) {
	t.Parallel()
	original := Resolve(sym("function", "Submit", "svc.Submit", "svc.go", "func Submit() {}", 3), "", "filehashA")

	// Insert lines above (range shifts) and edit the body.
	shifted := Resolve(sym("function", "Submit", "svc.Submit", "svc.go", "func Submit() { log() }", 30), "", "filehashB")

	if original.Identity.ID != shifted.Identity.ID {
		t.Fatalf("identity changed across a line shift / body edit: %s vs %s", original.Identity.ID, shifted.Identity.ID)
	}
	if original.Occurrence.ID == shifted.Occurrence.ID {
		t.Fatal("occurrence id did not change after a range/body change")
	}
}

func TestRenameProducesNewIdentity(t *testing.T) {
	t.Parallel()
	a := Resolve(sym("function", "Submit", "svc.Submit", "svc.go", "x", 1), "", "")
	b := Resolve(sym("function", "Send", "svc.Send", "svc.go", "x", 1), "", "")
	if a.Identity.ID == b.Identity.ID {
		t.Fatal("rename did not produce a new identity")
	}
}

func TestSameNameDifferentParentsAreDistinct(t *testing.T) {
	t.Parallel()
	a := Resolve(sym("method", "foo", "A.foo", "a.go", "x", 1), "", "")
	b := Resolve(sym("method", "foo", "B.foo", "a.go", "x", 1), "", "")
	if a.Identity.ID == b.Identity.ID {
		t.Fatal("same method name under different parents collapsed to one identity")
	}
}

func TestPointerAndValueReceiverShareIdentity(t *testing.T) {
	t.Parallel()
	// Given a normalized qualified name, pointer and value receiver methods are
	// the same logical identity (receiver normalization itself lands in #24).
	pointerForm := Resolve(sym("method", "Submit", "Service.Submit", "svc.go", "func (s *Service) Submit() {}", 1), "", "")
	valueForm := Resolve(sym("method", "Submit", "Service.Submit", "svc.go", "func (s Service) Submit() {}", 5), "", "")
	if pointerForm.Identity.ID != valueForm.Identity.ID {
		t.Fatal("pointer and value receiver methods got different identities")
	}
}

func TestOccurrenceOnlySymbols(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		symbol domain.Symbol
		only   bool
	}{
		{"import", sym("import", "fmt", "fmt", "a.go", "import \"fmt\"", 1), true},
		{"arrow", sym("function", "(arrow)", "", "a.js", "() => {}", 1), true},
		{"unnamed", sym("function", "", "", "a.go", "x", 1), true},
		{"file", sym("file", "svc.go", "svc.go", "svc.go", "", 1), false},
		{"named function", sym("function", "Submit", "svc.Submit", "svc.go", "x", 1), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resolved := Resolve(tc.symbol, "", "")
			if resolved.Occurrence.OccurrenceOnly != tc.only {
				t.Fatalf("OccurrenceOnly = %v, want %v", resolved.Occurrence.OccurrenceOnly, tc.only)
			}
			if tc.only {
				if resolved.Occurrence.SymbolID != "" || resolved.Identity.ID != "" {
					t.Fatalf("occurrence-only symbol carries an identity: %+v", resolved)
				}
			} else {
				if resolved.Occurrence.SymbolID == "" || resolved.Occurrence.SymbolID != resolved.Identity.ID {
					t.Fatalf("occurrence does not reference its identity: %+v", resolved)
				}
			}
		})
	}
}

func TestOccurrenceIDIsIndependentOfSnapshot(t *testing.T) {
	t.Parallel()
	// The OccurrenceID is computed only from SymbolID+path+range+bodyHash, so the
	// same occurrence data yields the same id regardless of which snapshot it is
	// observed in — no circular dependency with SnapshotID.
	first := Resolve(sym("function", "Submit", "svc.Submit", "svc.go", "x", 1), "", "fileHashSnapshot1")
	second := Resolve(sym("function", "Submit", "svc.Submit", "svc.go", "x", 1), "", "fileHashSnapshot2")
	if first.Occurrence.ID != second.Occurrence.ID {
		t.Fatal("OccurrenceID depended on data outside SymbolID+path+range+bodyHash")
	}
}

func TestIdentityCarriesNoLocationOrBody(t *testing.T) {
	t.Parallel()
	encoded, err := json.Marshal(Resolve(sym("function", "Submit", "svc.Submit", "svc.go", "secret body", 7), "", "fh").Identity)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"path", "range", "code", "\"line\"", "secret body", "svc.go"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("identity JSON leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestResolvedSymbolJSONRoundTrip(t *testing.T) {
	t.Parallel()
	resolved := Resolve(sym("method", "Submit", "Service.Submit", "svc.go", "func (s *Service) Submit() {}", 4), "sym:parent", "filehash")
	encoded, err := json.Marshal(resolved)
	if err != nil {
		t.Fatal(err)
	}
	var decoded domain.ResolvedSymbol
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Identity != resolved.Identity || decoded.Occurrence != resolved.Occurrence {
		t.Fatalf("round trip mismatch:\n got %+v\nwant %+v", decoded, resolved)
	}
}

func TestToSymbolFlattens(t *testing.T) {
	t.Parallel()
	resolved := Resolve(sym("function", "Submit", "svc.Submit", "svc.go", "func Submit() {}", 3), "", "fh")
	flat := resolved.ToSymbol()
	if flat.ID != string(resolved.Identity.ID) || flat.Name != "Submit" || flat.QualifiedName != "svc.Submit" ||
		flat.Path != "svc.go" || flat.Kind != "function" || flat.Range != resolved.Occurrence.Range {
		t.Fatalf("flatten mismatch: %+v", flat)
	}

	// An occurrence-only symbol flattens using the occurrence id as a stable handle.
	only := Resolve(sym("import", "fmt", "fmt", "a.go", "import \"fmt\"", 1), "", "fh")
	if only.ToSymbol().ID != string(only.Occurrence.ID) {
		t.Fatalf("occurrence-only flatten id = %q, want occurrence id", only.ToSymbol().ID)
	}
}
