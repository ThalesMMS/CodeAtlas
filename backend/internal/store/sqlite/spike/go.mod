// Isolated spike module (issue #51): evaluates SQLite/FTS5 drivers without adding
// any dependency to the production module github.com/ThalesMMS/CodeAtlas. The parent module
// excludes this subtree, so `go build/vet/test ./...` from backend never sees it.
module example.com/codeatlas-spike/sqlite

go 1.25.0

require (
	github.com/mattn/go-sqlite3 v1.14.45
	modernc.org/sqlite v1.53.0
)

require (
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	golang.org/x/sys v0.44.0 // indirect
	modernc.org/libc v1.73.4 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.11.0 // indirect
)
