package indexer

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/ThalesMMS/CodeAtlas/internal/contenthash"
	"github.com/ThalesMMS/CodeAtlas/internal/domain"
	"github.com/ThalesMMS/CodeAtlas/internal/mutation"
	"github.com/ThalesMMS/CodeAtlas/internal/observability"
	"github.com/ThalesMMS/CodeAtlas/internal/repository"
	"github.com/ThalesMMS/CodeAtlas/internal/retrieval"
)

// Parser parses a file's source into symbols and edges. The concrete
// implementation is the tree-sitter engine; the interface lets tests inject
// failures.
type Parser interface {
	Parse(path string, source []byte) ([]domain.Symbol, []domain.Edge, string, error)
}

type Indexer struct {
	root         string
	maxFileBytes int64
	parser       Parser
	store        repository.Store
	retriever    *retrieval.Hybrid
	broker       *Broker
	logger       *slog.Logger
	metrics      *observability.Metrics
	clock        observability.Clock
	mutations    mutation.Registry
	changeSink   func(context.Context, domain.PublishedWorkspaceChange)
	readFile     func(string) ([]byte, error)

	runMu     sync.Mutex
	statusMu  sync.RWMutex
	scanState ScanState
	lastError string
}

type candidate struct {
	relative string
	absolute string
	info     fs.FileInfo
	hash     string
	source   []byte
}

type parsedCandidate struct {
	parsed domain.ParsedFile
	path   string
	err    error
}

func New(root string, maxFileBytes int64, parser Parser, store repository.Store, retriever *retrieval.Hybrid) *Indexer {
	return &Indexer{
		root: root, maxFileBytes: maxFileBytes, parser: parser, store: store,
		retriever: retriever, broker: NewBroker(), scanState: ScanIdle,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)), clock: observability.RealClock,
		readFile: os.ReadFile,
	}
}

// SetObservability injects the logger and metrics collector. A nil logger keeps
// the silent default; a nil metrics collector is a no-op.
func (i *Indexer) SetObservability(logger *slog.Logger, metrics *observability.Metrics) {
	if logger != nil {
		i.logger = logger
	}
	i.metrics = metrics
}

func (i *Indexer) Broker() *Broker { return i.broker }

// SetMutationRegistry installs the ephemeral internal-write correlation
// registry used by final-state path reconciliation.
func (i *Indexer) SetMutationRegistry(registry mutation.Registry) {
	i.mutations = registry
}

// SetWorkspaceChangeSink installs a post-commit consumer for published file
// changes. The sink is called only after a reconcile commit has advanced the
// store; raw filesystem observations never reach it.
func (i *Indexer) SetWorkspaceChangeSink(sink func(context.Context, domain.PublishedWorkspaceChange)) {
	i.changeSink = sink
}

func (i *Indexer) readObservedCandidate(relative, absolute string, info fs.FileInfo) (candidate, bool, error) {
	if !info.Mode().IsRegular() || info.Size() > i.maxFileBytes {
		return candidate{}, false, nil
	}
	source, err := i.readFile(absolute)
	if err != nil {
		return candidate{}, false, err
	}
	hash := contenthash.HashContent(source)
	return candidate{relative: relative, absolute: absolute, info: info, hash: hash, source: source}, true, nil
}

func (i *Indexer) parseCandidates(ctx context.Context, candidates []candidate) <-chan parsedCandidate {
	output := make(chan parsedCandidate)
	jobs := make(chan candidate)
	workers := runtime.GOMAXPROCS(0)
	if workers > 6 {
		workers = 6
	}
	if workers < 1 {
		workers = 1
	}
	var waitGroup sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			for item := range jobs {
				parsed, err := i.parseOne(item)
				select {
				case output <- parsedCandidate{parsed: parsed, path: item.relative, err: err}:
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, item := range candidates {
			select {
			case jobs <- item:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		waitGroup.Wait()
		close(output)
	}()
	return output
}

func (i *Indexer) parseOne(item candidate) (domain.ParsedFile, error) {
	if parsed, ok := parseProjectFile(item); ok {
		parsed.File.IndexedAt = time.Now().UTC()
		return parsed, nil
	}
	symbols, edges, language, err := i.parser.Parse(item.relative, item.source)
	if err != nil {
		return domain.ParsedFile{}, fmt.Errorf("parse %s: %w", item.relative, err)
	}
	file := domain.File{
		Path: item.relative, Language: language, Hash: item.hash, Size: item.info.Size(),
		ModifiedAt: item.info.ModTime().UTC(), IndexedAt: time.Now().UTC(),
	}
	if len(symbols) > 0 {
		file.Summary = symbols[0].Summary
	}
	return domain.ParsedFile{File: file, Symbols: symbols, Edges: edges}, nil
}

func (i *Indexer) publish(eventType, path, message string) {
	i.broker.Publish(domain.IndexEvent{Type: eventType, Path: path, Message: message})
}

// publishEvent emits a full event envelope; the broker stamps the ID and timestamp.
func (i *Indexer) publishEvent(event domain.IndexEvent) {
	i.broker.Publish(event)
}
