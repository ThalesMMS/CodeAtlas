package sqlite

import (
	"encoding/binary"
	"fmt"
	"math"
)

// VectorEncodingVersion is the persisted embedding format decided in ADR-011:
// little-endian float32, one component per 4 bytes, no header. The version is
// stored in embedding_index_metadata; an incompatible change forces a rebuild.
const VectorEncodingVersion = "float32-le-v1"

// maxVectorDimension bounds allocation from an untrusted dimension/BLOB.
const maxVectorDimension = 8192

// encodeVector packs a float64 vector as little-endian float32. It rejects an
// empty vector, non-finite components and a zero-norm vector, and never aliases a
// caller buffer. Identical input always yields identical bytes.
func encodeVector(vector []float64) ([]byte, error) {
	if len(vector) == 0 {
		return nil, fmt.Errorf("vector is empty")
	}
	if len(vector) > maxVectorDimension {
		return nil, fmt.Errorf("vector dimension %d exceeds limit %d", len(vector), maxVectorDimension)
	}
	var sumSquares float64
	buffer := make([]byte, 4*len(vector))
	for i, value := range vector {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return nil, fmt.Errorf("vector component %d is not finite", i)
		}
		component := float32(value)
		binary.LittleEndian.PutUint32(buffer[i*4:], math.Float32bits(component))
		sumSquares += float64(component) * float64(component)
	}
	if sumSquares == 0 {
		return nil, fmt.Errorf("vector has zero norm")
	}
	return buffer, nil
}

// decodeVector reverses encodeVector, validating the byte length against the
// expected dimension and rejecting non-finite components. It allocates only after
// the length checks pass.
func decodeVector(blob []byte, expectedDimension int) ([]float64, error) {
	if expectedDimension <= 0 || expectedDimension > maxVectorDimension {
		return nil, fmt.Errorf("invalid expected dimension %d", expectedDimension)
	}
	if len(blob)%4 != 0 {
		return nil, fmt.Errorf("vector blob length %d is not a multiple of 4", len(blob))
	}
	components := len(blob) / 4
	if components != expectedDimension {
		return nil, fmt.Errorf("vector blob has %d components, expected %d", components, expectedDimension)
	}
	vector := make([]float64, components)
	for i := range vector {
		bits := binary.LittleEndian.Uint32(blob[i*4:])
		value := float64(math.Float32frombits(bits))
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return nil, fmt.Errorf("decoded component %d is not finite", i)
		}
		vector[i] = value
	}
	return vector, nil
}

// cosineSimilarity returns the cosine of the angle between two equal-length finite
// vectors. Callers guarantee non-zero norm (validated on encode and on the query).
func cosineSimilarity(a, b []float64) float64 {
	if len(a) != len(b) {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}

// validateQueryVector checks a query vector is non-empty, finite and non-zero.
func validateQueryVector(vector []float64) error {
	if len(vector) == 0 {
		return fmt.Errorf("query vector is empty")
	}
	if len(vector) > maxVectorDimension {
		return fmt.Errorf("query vector dimension %d exceeds limit %d", len(vector), maxVectorDimension)
	}
	var sumSquares float64
	for i, value := range vector {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return fmt.Errorf("query component %d is not finite", i)
		}
		sumSquares += value * value
	}
	if sumSquares == 0 {
		return fmt.Errorf("query vector has zero norm")
	}
	return nil
}
