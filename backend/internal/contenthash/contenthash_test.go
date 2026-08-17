package contenthash

import (
	"strings"
	"testing"
)

func TestHashContentCanonicalSHA256(t *testing.T) {
	t.Parallel()

	const emptySHA256 = "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	if got := HashContent(nil); got != emptySHA256 {
		t.Fatalf("HashContent(nil) = %q, want %q", got, emptySHA256)
	}

	got := HashContent([]byte{0x00, 0xff, 'a'})
	if !strings.HasPrefix(got, "sha256:") || len(got) != len("sha256:")+64 {
		t.Fatalf("HashContent(binary) = %q, want sha256:<64 lowercase hex chars>", got)
	}
	if got != strings.ToLower(got) {
		t.Fatalf("HashContent(binary) = %q, want lowercase output", got)
	}
	if again := HashContent([]byte{0x00, 0xff, 'a'}); again != got {
		t.Fatalf("HashContent(binary) changed between calls: %q != %q", again, got)
	}
}
