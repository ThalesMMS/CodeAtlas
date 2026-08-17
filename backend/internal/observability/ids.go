// Package observability provides the local-first observability primitives:
// compact correlation IDs, a thread-safe in-memory metrics collector, and a
// central redaction helper. It never logs prompts, responses, source content or
// credentials; callers pass already-sanitized fields built with the redact
// helpers. Loggers are injected at the edges (no implicit global), and a logging
// failure is never turned into a functional failure.
package observability

import (
	"crypto/rand"
	"encoding/base32"
	"regexp"
	"time"
)

// idEncoding is lowercase base32 without padding, URL-safe and case-stable.
var idEncoding = base32.NewEncoding("0123456789abcdefghijklmnopqrstuv").WithPadding(base32.NoPadding)

// NewID returns a compact, k-sortable-ish 20-char identifier from 12 random
// bytes. It never returns an error; on a (practically impossible) RNG failure it
// falls back to a fixed marker so a missing ID never breaks a request.
func NewID() string {
	var buffer [12]byte
	if _, err := rand.Read(buffer[:]); err != nil {
		return "00000000000000000000"
	}
	return idEncoding.EncodeToString(buffer[:])
}

// requestIDPattern bounds an accepted client-supplied request id to a short,
// safe character set so it cannot inject newlines or unbounded data into logs.
var requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{8,36}$`)

// AcceptRequestID returns the client id when it matches the restricted format,
// otherwise a freshly generated one. The effective id is always returned.
func AcceptRequestID(clientProvided string) string {
	if requestIDPattern.MatchString(clientProvided) {
		return clientProvided
	}
	return NewID()
}

// Clock is an injectable time source so duration-sensitive tests are deterministic.
type Clock interface {
	Now() time.Time
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

// RealClock is the production clock.
var RealClock Clock = realClock{}
