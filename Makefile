SHELL := /bin/sh
-include .env
export CODEATLAS_LLM_BASE_URL
export CODEATLAS_LLM_API_KEY
export CODEATLAS_LLM_MODEL
export CODEATLAS_LLM_REASONING_EFFORT
export CODEATLAS_LLM_TIMEOUT
export CODEATLAS_ENABLE_EMBEDDINGS
export CODEATLAS_EMBEDDING_MODEL
export CODEATLAS_EMBEDDING_BASE_URL
export CODEATLAS_EMBEDDINGS_API_KEY
export CODEATLAS_MAX_FILE_BYTES
export CODEATLAS_PROBE_TIMEOUT
export CODEATLAS_WATCH_MODE
export CODEATLAS_WATCH_DEBOUNCE
export CODEATLAS_WATCH_MAX_BATCH_DELAY
export CODEATLAS_WATCH_RECONCILE_INTERVAL
export CODEATLAS_POLL_INTERVAL
export CODEATLAS_GOPLS
export CODEATLAS_GOPLS_PATH
export CODEATLAS_TYPESCRIPT_LSP
export CODEATLAS_TYPESCRIPT_LSP_PATH
export CODEATLAS_TYPESCRIPT_SDK_PATH
export CODEATLAS_SWIFT_LSP
export CODEATLAS_SWIFT_LSP_PATH
export CODEATLAS_PYTHON_LSP
export CODEATLAS_PYTHON_LSP_PATH
export CODEATLAS_RUST_LSP
export CODEATLAS_RUST_LSP_PATH

ROOT := $(abspath .)
WORKSPACE ?= $(if $(strip $(CODEATLAS_WORKSPACE)),$(abspath $(CODEATLAS_WORKSPACE)),$(ROOT)/examples/tinycommerce)
LISTEN ?= $(if $(strip $(CODEATLAS_LISTEN)),$(CODEATLAS_LISTEN),127.0.0.1:8080)
BINARY_EXT := $(if $(filter Windows_NT,$(OS)),.exe,)
BINARY := $(ROOT)/dist/codeatlas$(BINARY_EXT)
FRONTEND_NODE_MODULES := $(ROOT)/frontend/node_modules

.PHONY: fmt test test-race check build run clean eval eval-judge eval-update verify-treesitter bootstrap-treesitter docs-manifest docs-checksums verify-docs frontend-install frontend-dev frontend-build frontend-check frontend-test require-frontend-install e2e-install e2e e2e-smoke-repeat e2e-budget spike-sqlite spike-editor-monaco spike-editor-codemirror spike-test benchmark-editors

fmt:
	@cd backend && go fmt ./...

check: frontend-check verify-treesitter verify-docs
	@cd backend && go vet -tags fts5 ./...

# verify-treesitter recomputes the vendored Tree-sitter content hash and checks it
# against the lockfile, offline. CI runs this; it never downloads dependencies.
verify-treesitter:
	@cd backend && go run ./cmd/treesitter-tool verify -dir internal/treesitter

# bootstrap-treesitter downloads the runtime/grammars from deps.lock.json (allowlisted
# URLs only), verifying each archive's SHA-256 before extracting. Requires network.
bootstrap-treesitter:
	@cd backend && go run ./cmd/treesitter-tool bootstrap -dir internal/treesitter

docs-manifest:
	@tmp=$$(mktemp); trap 'rm -f "$$tmp"' EXIT; \
	git ls-files --cached --others --exclude-standard docs | sed 's#^docs/##' | LC_ALL=C /usr/bin/sort > "$$tmp"; \
	mv "$$tmp" docs/MANIFEST.txt

docs-checksums: docs-manifest
	@tmp=$$(mktemp); trap 'rm -f "$$tmp"' EXIT; \
	git ls-files --cached --others --exclude-standard docs | grep -v '^docs/SHA256SUMS.txt$$' | LC_ALL=C /usr/bin/sort | \
		while IFS= read -r path; do \
			if ! git cat-file -e ":$$path" 2>/dev/null; then \
				echo "docs checksum input must be staged: $$path" >&2; exit 1; \
			fi; \
			if command -v sha256sum >/dev/null 2>&1; then \
				checksum=$$(git show ":$$path" | sha256sum | awk '{print $$1}'); \
			else \
				checksum=$$(git show ":$$path" | shasum -a 256 | awk '{print $$1}'); \
			fi; \
			printf '%s  ./%s\n' "$$checksum" "$${path#docs/}"; \
		done > "$$tmp"; \
		mv "$$tmp" docs/SHA256SUMS.txt

verify-docs:
	@manifest=$$(mktemp); sums=$$(mktemp); expected=$$(mktemp); \
		trap 'rm -f "$$manifest" "$$sums" "$$expected"' EXIT; \
	if ! git diff --quiet -- docs; then \
		echo 'docs contain unstaged content changes; stage them before verification' >&2; \
		git diff --name-only -- docs >&2; exit 1; \
	fi; \
	git ls-files --cached --others --exclude-standard docs | sed 's#^docs/##' | LC_ALL=C /usr/bin/sort > "$$manifest"; \
		if ! cmp -s "$$manifest" docs/MANIFEST.txt; then \
			echo 'docs/MANIFEST.txt is stale; run make docs-checksums' >&2; \
			diff -u docs/MANIFEST.txt "$$manifest" || true; exit 1; \
		fi; \
		grep -v '^SHA256SUMS.txt$$' "$$manifest" > "$$expected"; \
	awk '{name=$$2; sub(/^\.\//, "", name); print name}' docs/SHA256SUMS.txt | LC_ALL=C /usr/bin/sort > "$$sums"; \
		if ! cmp -s "$$expected" "$$sums"; then \
			echo 'docs/SHA256SUMS.txt file list is stale; run make docs-checksums' >&2; \
			diff -u "$$sums" "$$expected" || true; exit 1; \
		fi; \
	failed=0; while read -r checksum recorded_path; do \
		name=$${recorded_path#./}; path="docs/$$name"; \
		if ! git cat-file -e ":$$path" 2>/dev/null; then \
			echo "./$$name: FAILED (not staged)"; failed=1; continue; \
		fi; \
		if command -v sha256sum >/dev/null 2>&1; then \
			actual=$$(git show ":$$path" | sha256sum | awk '{print $$1}'); \
		else \
			actual=$$(git show ":$$path" | shasum -a 256 | awk '{print $$1}'); \
		fi; \
		if [ "$$actual" = "$$checksum" ]; then echo "./$$name: OK"; \
		else echo "./$$name: FAILED"; failed=1; fi; \
	done < docs/SHA256SUMS.txt; test "$$failed" -eq 0

test: frontend-build frontend-test
	@cd backend && go test -tags fts5 ./...

test-race: frontend-build
	@cd backend && go test -tags fts5 -race ./...

build: frontend-build
	@mkdir -p dist
	@cd backend && CGO_ENABLED=1 go build -tags fts5 -trimpath -o "$(BINARY)" ./cmd/codeatlas

run: frontend-build
	@cd backend && CGO_ENABLED=1 go run -tags fts5 ./cmd/codeatlas -workspace "$(WORKSPACE)" -listen "$(LISTEN)"

clean:
	@rm -rf dist backend/internal/webui/dist
	@mkdir -p backend/internal/webui/dist

frontend-install:
	@cd frontend && npm ci

require-frontend-install:
	@test -d "$(FRONTEND_NODE_MODULES)" || (echo "frontend dependencies missing; run 'make frontend-install'" >&2; exit 1)

frontend-dev: require-frontend-install
	@cd frontend && npm run dev

frontend-build: require-frontend-install
	@rm -rf backend/internal/webui/dist
	@mkdir -p backend/internal/webui/dist
	@cd frontend && npm run build

frontend-check: frontend-build
	@cd frontend && npm run check

frontend-test: frontend-build
	@cd frontend && npm test

e2e-install:
	@cd e2e && npm ci

e2e: build e2e-install
	@cd e2e && npm test

e2e-smoke-repeat: build e2e-install
	@cd e2e && npm run test:smoke

e2e-budget: frontend-build e2e-install
	@cd e2e && npm run budget

# spike-sqlite runs the ISOLATED SQLite/FTS5 driver evaluation (issue #51). It lives
# in its own nested module so production never depends on a SQLite driver. The fts5
# tag is required by the cgo mattn driver (ignored by modernc). Pass BENCH=. to also
# run benchmarks, e.g. make spike-sqlite BENCH='BenchmarkFTSSearch'.
BENCH ?=
spike-sqlite:
	@cd backend/internal/store/sqlite/spike && CGO_ENABLED=1 go test -tags fts5 -count=1 \
		$(if $(BENCH),-run x -bench '$(BENCH)' -benchmem -benchtime 200x,) ./...

spike-editor-monaco:
	@cd frontend/spikes && npm run dev:monaco

spike-editor-codemirror:
	@cd frontend/spikes && npm run dev:codemirror

spike-test:
	@cd frontend/spikes && npm run test && npm run typecheck

benchmark-editors:
	@cd frontend/spikes && npm run benchmark && npm run report

# eval runs retrieval gates plus the four-surface output-quality scorecard,
# writing eval/report.{json,md} (gitignored) and gating against eval/baseline.json.
# It is offline and network-free: a contract-aware fake provider exercises the
# real surfaces, while the optional live judge remains disabled.
eval: frontend-build
	@cd backend && CGO_ENABLED=1 go run -tags fts5 ./cmd/eval -root "$(ROOT)"

eval-judge: frontend-build
	@cd backend && CGO_ENABLED=1 go run -tags fts5 ./cmd/eval -root "$(ROOT)" -llm-judge

# eval-update regenerates the versioned baseline; use only on an intentional,
# reviewed policy/schema change and note it in the PR.
eval-update: frontend-build
	@cd backend && CGO_ENABLED=1 go run -tags fts5 ./cmd/eval -root "$(ROOT)" -update-baseline
