// Package ambiguous exercises homonym methods on different types, an interface
// with implementations, and a call cycle — cases the identity algorithm and the
// graph expansion must keep distinct. Small, original, MIT-style fixture.
package ambiguous

// Processor is implemented by both Fast and Slow below.
type Processor interface {
	Process(input string) string
}

// Fast and Slow define a homonym method Process on different types: they must get
// distinct SymbolIDs even though the name matches.
type Fast struct{}

func (Fast) Process(input string) string { return input }

type Slow struct{}

func (Slow) Process(input string) string { return decorate(input) }

func decorate(input string) string { return "[" + input + "]" }

// ping and pong form a deliberate call cycle.
func ping(n int) int {
	if n <= 0 {
		return 0
	}
	return pong(n - 1)
}

func pong(n int) int {
	if n <= 0 {
		return 0
	}
	return ping(n - 1)
}
