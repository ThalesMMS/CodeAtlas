package sqlite

import (
	"math"
	"testing"
)

func TestEncodeDecodeRoundTrips(t *testing.T) {
	t.Parallel()
	original := []float64{0.5, -0.25, 1.0, 0.125}
	blob, err := encodeVector(original)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if len(blob) != 4*len(original) {
		t.Fatalf("blob length = %d, want %d", len(blob), 4*len(original))
	}
	decoded, err := decodeVector(blob, len(original))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	for i := range original {
		if math.Abs(decoded[i]-original[i]) > 1e-6 {
			t.Fatalf("component %d round-trip drift: %v vs %v", i, decoded[i], original[i])
		}
	}
}

func TestEncodeIsDeterministic(t *testing.T) {
	t.Parallel()
	vector := []float64{0.1, 0.2, 0.3}
	a, _ := encodeVector(vector)
	b, _ := encodeVector(vector)
	if string(a) != string(b) {
		t.Fatal("encoding is not deterministic")
	}
}

func TestEncodeRejectsBadVectors(t *testing.T) {
	t.Parallel()
	cases := map[string][]float64{
		"empty":     {},
		"nan":       {1, math.NaN(), 2},
		"inf":       {1, math.Inf(1), 2},
		"zero-norm": {0, 0, 0},
	}
	for name, vector := range cases {
		if _, err := encodeVector(vector); err == nil {
			t.Errorf("%s: expected an encode error", name)
		}
	}
}

func TestDecodeRejectsMalformedBlobs(t *testing.T) {
	t.Parallel()
	valid, _ := encodeVector([]float64{1, 2})
	// Truncated (not a multiple of 4).
	if _, err := decodeVector(valid[:len(valid)-1], 2); err == nil {
		t.Error("expected error for truncated blob")
	}
	// Dimension mismatch (extra bytes).
	if _, err := decodeVector(valid, 3); err == nil {
		t.Error("expected error for dimension mismatch")
	}
	// Over-limit dimension is rejected before allocation.
	if _, err := decodeVector(valid, maxVectorDimension+1); err == nil {
		t.Error("expected error for over-limit dimension")
	}
}

func TestCosineSimilarity(t *testing.T) {
	t.Parallel()
	if got := cosineSimilarity([]float64{1, 0}, []float64{1, 0}); math.Abs(got-1) > 1e-9 {
		t.Fatalf("identical vectors cosine = %v, want 1", got)
	}
	if got := cosineSimilarity([]float64{1, 0}, []float64{0, 1}); math.Abs(got) > 1e-9 {
		t.Fatalf("orthogonal vectors cosine = %v, want 0", got)
	}
	if got := cosineSimilarity([]float64{1, 0}, []float64{-1, 0}); math.Abs(got+1) > 1e-9 {
		t.Fatalf("opposite vectors cosine = %v, want -1", got)
	}
}

func TestValidateQueryVector(t *testing.T) {
	t.Parallel()
	if err := validateQueryVector([]float64{1, 2, 3}); err != nil {
		t.Fatalf("valid vector rejected: %v", err)
	}
	if err := validateQueryVector(nil); err == nil {
		t.Fatal("nil query vector should be rejected")
	}
	if err := validateQueryVector([]float64{0, 0}); err == nil {
		t.Fatal("zero-norm query vector should be rejected")
	}
}
