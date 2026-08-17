package spike

import (
	"database/sql"
	"encoding/binary"
	"fmt"
	"math"
	"math/rand"
)

// Profile sizes a synthetic dataset. Counts mirror the issue's small/medium/large
// table; EmbeddingDim is configurable (a realistic and a tiny value).
type Profile struct {
	Name         string
	Files        int
	Symbols      int
	Edges        int
	Embeddings   int
	EmbeddingDim int
}

var (
	ProfileSmall  = Profile{Name: "small", Files: 100, Symbols: 1000, Edges: 3000, Embeddings: 1000, EmbeddingDim: 64}
	ProfileMedium = Profile{Name: "medium", Files: 2000, Symbols: 25000, Edges: 100000, Embeddings: 25000, EmbeddingDim: 256}
	ProfileLarge  = Profile{Name: "large", Files: 10000, Symbols: 150000, Edges: 750000, Embeddings: 150000, EmbeddingDim: 768}
)

// Dataset is fully determined by (Profile, seed): same inputs → identical bytes.
type Dataset struct {
	Profile    Profile
	Files      []FileRow
	Symbols    []SymbolRow
	Edges      []EdgeRow
	Embeddings []EmbeddingRow
}

type FileRow struct {
	Path, Lang, Hash string
	Size             int
}
type SymbolRow struct {
	SymbolID, Name, Kind, Qualified, Doc string
	FileIndex                            int
	StartLine, StartCol                  int
}
type EdgeRow struct {
	Kind, Src, Dst, Provenance string
	Confidence                 float64
}
type EmbeddingRow struct {
	SymbolID string
	Vec      []float32
}

var langs = []string{"go", "typescript", "javascript", "tsx"}
var kinds = []string{"func", "method", "type", "var", "const", "interface"}
var edgeKinds = []string{"calls", "references", "implements", "imports"}
var nameParts = []string{"User", "Order", "Service", "Repo", "Handler", "Cache", "Token", "Parse", "Build", "Render", "Fetch", "Commit", "Index", "Query", "Resolve"}

// Generate builds a deterministic dataset from a fixed seed.
func Generate(profile Profile, seed int64) Dataset {
	rng := rand.New(rand.NewSource(seed))
	dataset := Dataset{Profile: profile}

	dataset.Files = make([]FileRow, profile.Files)
	for i := range dataset.Files {
		lang := langs[rng.Intn(len(langs))]
		dataset.Files[i] = FileRow{
			Path: fmt.Sprintf("pkg/mod%d/file_%d.%s", i%64, i, extFor(lang)),
			Lang: lang, Hash: fmt.Sprintf("%016x", rng.Uint64()), Size: 200 + rng.Intn(8000),
		}
	}

	dataset.Symbols = make([]SymbolRow, profile.Symbols)
	for i := range dataset.Symbols {
		name := nameParts[rng.Intn(len(nameParts))] + nameParts[rng.Intn(len(nameParts))]
		kind := kinds[rng.Intn(len(kinds))]
		fileIndex := rng.Intn(profile.Files)
		dataset.Symbols[i] = SymbolRow{
			SymbolID:  fmt.Sprintf("sym:%s:%d", name, i),
			Name:      name,
			Kind:      kind,
			Qualified: fmt.Sprintf("%s.%s", dataset.Files[fileIndex].Path, name),
			Doc:       fmt.Sprintf("%s %s handles %s in module %d", kind, name, nameParts[rng.Intn(len(nameParts))], fileIndex%64),
			FileIndex: fileIndex, StartLine: 1 + rng.Intn(400), StartCol: 1 + rng.Intn(40),
		}
	}

	dataset.Edges = make([]EdgeRow, profile.Edges)
	for i := range dataset.Edges {
		src := dataset.Symbols[rng.Intn(profile.Symbols)].SymbolID
		dst := dataset.Symbols[rng.Intn(profile.Symbols)].SymbolID
		dataset.Edges[i] = EdgeRow{
			Kind: edgeKinds[rng.Intn(len(edgeKinds))], Src: src, Dst: dst,
			Provenance: "ast", Confidence: 0.5 + rng.Float64()/2,
		}
	}

	embeddings := profile.Embeddings
	if embeddings > profile.Symbols {
		embeddings = profile.Symbols
	}
	dataset.Embeddings = make([]EmbeddingRow, embeddings)
	for i := range dataset.Embeddings {
		vec := make([]float32, profile.EmbeddingDim)
		for d := range vec {
			vec[d] = float32(rng.NormFloat64())
		}
		dataset.Embeddings[i] = EmbeddingRow{SymbolID: dataset.Symbols[i].SymbolID, Vec: normalize(vec)}
	}
	return dataset
}

func extFor(lang string) string {
	switch lang {
	case "typescript":
		return "ts"
	case "javascript":
		return "js"
	case "tsx":
		return "tsx"
	default:
		return "go"
	}
}

func normalize(vec []float32) []float32 {
	var sum float64
	for _, v := range vec {
		sum += float64(v) * float64(v)
	}
	norm := math.Sqrt(sum)
	if norm == 0 {
		return vec
	}
	for i := range vec {
		vec[i] = float32(float64(vec[i]) / norm)
	}
	return vec
}

// EncodeVector packs a float32 vector little-endian for the embeddings BLOB.
func EncodeVector(vec []float32) []byte {
	buffer := make([]byte, 4*len(vec))
	for i, v := range vec {
		binary.LittleEndian.PutUint32(buffer[i*4:], math.Float32bits(v))
	}
	return buffer
}

// DecodeVector reverses EncodeVector.
func DecodeVector(blob []byte) []float32 {
	vec := make([]float32, len(blob)/4)
	for i := range vec {
		vec[i] = math.Float32frombits(binary.LittleEndian.Uint32(blob[i*4:]))
	}
	return vec
}

// Load performs a full initial load inside a single IMMEDIATE transaction, the way
// production would build a snapshot atomically.
func Load(db *sql.DB, dataset Dataset) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	if _, err := tx.Exec("INSERT OR REPLACE INTO metadata(key,value) VALUES('schema_version','1'),('profile',?)", dataset.Profile.Name); err != nil {
		return err
	}
	if _, err := tx.Exec("INSERT INTO revisions(snapshot_id, created_at) VALUES(?, ?)", "snap-"+dataset.Profile.Name, 1); err != nil {
		return err
	}

	fileIDs := make([]int64, len(dataset.Files))
	fileStmt, err := tx.Prepare("INSERT INTO files(path,lang,hash,size) VALUES(?,?,?,?)")
	if err != nil {
		return err
	}
	for i, file := range dataset.Files {
		result, err := fileStmt.Exec(file.Path, file.Lang, file.Hash, file.Size)
		if err != nil {
			return err
		}
		fileIDs[i], _ = result.LastInsertId()
	}

	identityStmt, _ := tx.Prepare("INSERT INTO identities(symbol_id,name,kind,file_id) VALUES(?,?,?,?)")
	occStmt, _ := tx.Prepare("INSERT INTO occurrences(symbol_id,file_id,start_line,start_col,end_line,end_col,role) VALUES(?,?,?,?,?,?,?)")
	ftsStmt, _ := tx.Prepare("INSERT INTO fts_documents(symbol_id,name,qualified,doc) VALUES(?,?,?,?)")
	for _, symbol := range dataset.Symbols {
		fileID := fileIDs[symbol.FileIndex]
		if _, err := identityStmt.Exec(symbol.SymbolID, symbol.Name, symbol.Kind, fileID); err != nil {
			return err
		}
		if _, err := occStmt.Exec(symbol.SymbolID, fileID, symbol.StartLine, symbol.StartCol, symbol.StartLine, symbol.StartCol+len(symbol.Name), "definition"); err != nil {
			return err
		}
		if _, err := ftsStmt.Exec(symbol.SymbolID, symbol.Name, symbol.Qualified, symbol.Doc); err != nil {
			return err
		}
	}

	edgeStmt, _ := tx.Prepare("INSERT INTO edges(kind,src,dst,provenance,confidence) VALUES(?,?,?,?,?)")
	for _, edge := range dataset.Edges {
		if _, err := edgeStmt.Exec(edge.Kind, edge.Src, edge.Dst, edge.Provenance, edge.Confidence); err != nil {
			return err
		}
	}

	embStmt, _ := tx.Prepare("INSERT INTO embeddings(symbol_id,dim,model,vec) VALUES(?,?,?,?)")
	for _, embedding := range dataset.Embeddings {
		if _, err := embStmt.Exec(embedding.SymbolID, dataset.Profile.EmbeddingDim, "spike-model", EncodeVector(embedding.Vec)); err != nil {
			return err
		}
	}
	return tx.Commit()
}
