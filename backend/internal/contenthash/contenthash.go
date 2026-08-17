// Package contenthash defines the canonical hash used for workspace file
// content throughout CodeAtlas.
package contenthash

import (
	"crypto/sha256"
	"encoding/hex"
)

// HashContent returns the canonical lowercase SHA-256 content hash.
func HashContent(content []byte) string {
	digest := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(digest[:])
}
