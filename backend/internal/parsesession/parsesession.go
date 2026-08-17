// Package parsesession keeps one incremental Tree-sitter session per active
// document, tied to the OverlayStore lifecycle. Each session owns a reusable
// parser and the current tree/source/version; updates are serialized per document
// and reuse the old tree, with a safe fall back to a full parse. No C Node escapes
// a session without a guarded lifetime (reads run under WithTree while the session
// holds its lock, so the tree is never freed mid-read).
package parsesession

import (
	"errors"
	"fmt"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/ThalesMMS/CodeAtlas/internal/treesitter"
)

var (
	// ErrSessionNotFound is returned for an unknown or closed document session.
	ErrSessionNotFound = errors.New("parsesession: no session for document")
	// ErrVersionUnavailable identifies stale/mismatched version requests without
	// requiring callers to classify human-readable error text.
	ErrVersionUnavailable = errors.New("parsesession: version unavailable")
)

// FallbackReason explains why a full parse replaced an incremental one.
type FallbackReason string

const (
	FallbackNoOldTree      FallbackReason = "no_old_tree"
	FallbackLanguageChange FallbackReason = "language_changed"
	FallbackEditCompute    FallbackReason = "edit_compute_failed"
	FallbackIncremental    FallbackReason = "incremental_failed"
	FallbackVersionGap     FallbackReason = "version_gap"
)

// Snapshot is the published, immutable result of a parse. ChangedRanges is empty
// after a full parse.
type Snapshot struct {
	DocumentID    string
	Version       int64
	Language      treesitter.Language
	ContentHash   string
	TreeVersion   uint64
	ChangedRanges []treesitter.Range
	HasErrors     bool
	Incremental   bool
	ParsedAt      time.Time
}

// Metrics is a thread-safe parse counter set.
type Metrics struct {
	mu            sync.Mutex
	FullParses    int64
	Incremental   int64
	Fallbacks     map[FallbackReason]int64
	ChangedRanges int64
	OpenSessions  int64
}

func NewMetrics() *Metrics { return &Metrics{Fallbacks: map[FallbackReason]int64{}} }

func (m *Metrics) full() { m.mu.Lock(); m.FullParses++; m.mu.Unlock() }
func (m *Metrics) incremental(ranges int) {
	m.mu.Lock()
	m.Incremental++
	m.ChangedRanges += int64(ranges)
	m.mu.Unlock()
}
func (m *Metrics) fallback(reason FallbackReason) {
	m.mu.Lock()
	m.Fallbacks[reason]++
	m.mu.Unlock()
}
func (m *Metrics) sessions(delta int64) { m.mu.Lock(); m.OpenSessions += delta; m.mu.Unlock() }

// MetricsSnapshot is a lock-free copy of the counters for reporting.
type MetricsSnapshot struct {
	FullParses    int64
	Incremental   int64
	Fallbacks     map[FallbackReason]int64
	ChangedRanges int64
	OpenSessions  int64
}

// Snapshot returns a copy of the metric counters.
func (m *Metrics) Snapshot() MetricsSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	clone := MetricsSnapshot{FullParses: m.FullParses, Incremental: m.Incremental, ChangedRanges: m.ChangedRanges, OpenSessions: m.OpenSessions, Fallbacks: map[FallbackReason]int64{}}
	for reason, count := range m.Fallbacks {
		clone.Fallbacks[reason] = count
	}
	return clone
}

type session struct {
	mu          sync.Mutex
	parser      *treesitter.Parser
	tree        *treesitter.Tree
	source      []byte
	version     int64
	language    treesitter.Language
	treeVersion uint64
	snapshot    Snapshot
	closed      bool
}

func (s *session) close() {
	if s.closed {
		return
	}
	s.closed = true
	if s.tree != nil {
		s.tree.Close()
		s.tree = nil
	}
	if s.parser != nil {
		_ = s.parser.Close()
		s.parser = nil
	}
}

// Manager owns all sessions. A global semaphore bounds concurrent parses; the
// per-session mutex serializes a single document's updates and reads.
type Manager struct {
	mu       sync.Mutex
	sessions map[string]*session
	sem      chan struct{}
	metrics  *Metrics
	hashFn   func([]byte) string
}

// NewManager builds a manager. maxConcurrent bounds parallel parses (defaults to 8).
func NewManager(maxConcurrent int, hashFn func([]byte) string, metrics *Metrics) *Manager {
	if maxConcurrent <= 0 {
		maxConcurrent = 8
	}
	if metrics == nil {
		metrics = NewMetrics()
	}
	return &Manager{
		sessions: make(map[string]*session),
		sem:      make(chan struct{}, maxConcurrent),
		metrics:  metrics,
		hashFn:   hashFn,
	}
}

func (m *Manager) Metrics() *Metrics { return m.metrics }

func (m *Manager) acquire() { m.sem <- struct{}{} }
func (m *Manager) release() { <-m.sem }

// Open creates a session for a document and performs the initial full parse. If
// the parser cannot be created the document is not analyzable and Open fails — no
// editable-but-unparseable session is left behind.
func (m *Manager) Open(documentID string, version int64, language treesitter.Language, content []byte) (Snapshot, error) {
	parser, err := treesitter.NewParser(language)
	if err != nil {
		return Snapshot{}, fmt.Errorf("parsesession: cannot create parser: %w", err)
	}
	m.acquire()
	tree, err := parser.Parse(content, nil)
	m.release()
	if err != nil {
		_ = parser.Close()
		return Snapshot{}, fmt.Errorf("parsesession: initial parse failed: %w", err)
	}
	m.metrics.full()

	sess := &session{
		parser:      parser,
		tree:        tree,
		source:      append([]byte(nil), content...),
		version:     version,
		language:    language,
		treeVersion: 1,
	}
	sess.snapshot = m.buildSnapshot(documentID, sess, nil, false)

	m.mu.Lock()
	if existing, ok := m.sessions[documentID]; ok {
		existing.mu.Lock()
		existing.close()
		existing.mu.Unlock()
		m.metrics.sessions(-1)
	}
	m.sessions[documentID] = sess
	m.mu.Unlock()
	m.metrics.sessions(1)
	return sess.snapshot, nil
}

// Update advances the session to the successor version, parsing incrementally and
// falling back to a full parse on any failure. An out-of-order version is rejected
// without parsing.
func (m *Manager) Update(documentID string, version int64, newContent []byte) (Snapshot, error) {
	sess := m.lookup(documentID)
	if sess == nil {
		return Snapshot{}, ErrSessionNotFound
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if sess.closed {
		return Snapshot{}, ErrSessionNotFound
	}
	// A stale or duplicate update (<= current) is discarded so an old response
	// never overwrites a newer version.
	if version <= sess.version {
		return Snapshot{}, fmt.Errorf("%w: stale version %d <= current %d", ErrVersionUnavailable, version, sess.version)
	}
	// A forward gap (some intermediate update was lost or reordered) cannot reuse
	// the old tree against the right base, so the session self-heals with a full
	// parse to the requested version.
	if version != sess.version+1 {
		return m.fullParseLocked(documentID, sess, version, newContent, FallbackVersionGap)
	}

	edit, ok := computeEdit(sess.source, newContent)
	if !ok {
		return m.fullParseLocked(documentID, sess, version, newContent, FallbackEditCompute)
	}
	if err := sess.tree.Edit(edit); err != nil {
		return m.fullParseLocked(documentID, sess, version, newContent, FallbackEditCompute)
	}
	m.acquire()
	newTree, err := sess.parser.Parse(newContent, sess.tree)
	if err != nil || newTree == nil {
		m.release()
		return m.fullParseLocked(documentID, sess, version, newContent, FallbackIncremental)
	}
	changed, err := treesitter.ChangedRanges(sess.tree, newTree)
	m.release()
	if err != nil {
		newTree.Close()
		return m.fullParseLocked(documentID, sess, version, newContent, FallbackIncremental)
	}

	// Publish tree+source+version as a unit; the old tree has no readers (we hold
	// the session lock) so it is safe to free now.
	sess.tree.Close()
	sess.tree = newTree
	sess.source = append([]byte(nil), newContent...)
	sess.version = version
	sess.treeVersion++
	sess.snapshot = m.buildSnapshot(documentID, sess, changed, true)
	m.metrics.incremental(len(changed))
	return sess.snapshot, nil
}

// fullParseLocked replaces the session content with a fresh parse. Caller holds
// sess.mu.
func (m *Manager) fullParseLocked(documentID string, sess *session, version int64, content []byte, reason FallbackReason) (Snapshot, error) {
	m.metrics.fallback(reason)
	m.acquire()
	tree, err := sess.parser.Parse(content, nil)
	m.release()
	if err != nil || tree == nil {
		return Snapshot{}, fmt.Errorf("parsesession: full parse fallback (%s) failed: %w", reason, err)
	}
	if sess.tree != nil {
		sess.tree.Close()
	}
	sess.tree = tree
	sess.source = append([]byte(nil), content...)
	sess.version = version
	sess.treeVersion++
	sess.snapshot = m.buildSnapshot(documentID, sess, nil, false)
	m.metrics.full()
	return sess.snapshot, nil
}

func (m *Manager) buildSnapshot(documentID string, sess *session, changed []treesitter.Range, incremental bool) Snapshot {
	hash := ""
	if m.hashFn != nil {
		hash = m.hashFn(sess.source)
	}
	return Snapshot{
		DocumentID:    documentID,
		Version:       sess.version,
		Language:      sess.language,
		ContentHash:   hash,
		TreeVersion:   sess.treeVersion,
		ChangedRanges: append([]treesitter.Range(nil), changed...),
		HasErrors:     sess.tree.RootNode().HasError(),
		Incremental:   incremental,
		ParsedAt:      time.Now().UTC(),
	}
}

// Get returns the current snapshot, or the one for an exact version if requested.
func (m *Manager) Get(documentID string, version *int64) (Snapshot, error) {
	sess := m.lookup(documentID)
	if sess == nil {
		return Snapshot{}, ErrSessionNotFound
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if sess.closed {
		return Snapshot{}, ErrSessionNotFound
	}
	if version != nil && *version != sess.version {
		return Snapshot{}, fmt.Errorf("%w: requested %d, current %d", ErrVersionUnavailable, *version, sess.version)
	}
	return sess.snapshot, nil
}

// WithTree runs fn against the document's root node and source while the session
// is held, so the tree cannot be freed mid-read. It is the only sanctioned way to
// touch C Nodes. A version may be required to reject a stale read.
func (m *Manager) WithTree(documentID string, version *int64, fn func(root treesitter.Node, source []byte) error) error {
	sess := m.lookup(documentID)
	if sess == nil {
		return ErrSessionNotFound
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if sess.closed || sess.tree == nil {
		return ErrSessionNotFound
	}
	if version != nil && *version != sess.version {
		return fmt.Errorf("%w: requested %d, current %d", ErrVersionUnavailable, *version, sess.version)
	}
	return fn(sess.tree.RootNode(), sess.source)
}

// Close removes a document's session and frees its parser/tree.
func (m *Manager) Close(documentID string) {
	m.mu.Lock()
	sess, ok := m.sessions[documentID]
	if ok {
		delete(m.sessions, documentID)
	}
	m.mu.Unlock()
	if !ok {
		return
	}
	sess.mu.Lock()
	sess.close()
	sess.mu.Unlock()
	m.metrics.sessions(-1)
}

// CloseAll frees every session (shutdown).
func (m *Manager) CloseAll() {
	m.mu.Lock()
	sessions := m.sessions
	m.sessions = make(map[string]*session)
	m.mu.Unlock()
	for _, sess := range sessions {
		sess.mu.Lock()
		sess.close()
		sess.mu.Unlock()
	}
}

func (m *Manager) lookup(documentID string) *session {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessions[documentID]
}

// computeEdit derives the minimal contiguous InputEdit between old and new by the
// longest common prefix and suffix in bytes, backed off to UTF-8 boundaries. It
// returns false only on a degenerate case the caller should full-parse.
func computeEdit(old, next []byte) (treesitter.InputEdit, bool) {
	// Common prefix.
	prefix := 0
	for prefix < len(old) && prefix < len(next) && old[prefix] == next[prefix] {
		prefix++
	}
	prefix = utf8BoundaryBefore(old, prefix)

	// Common suffix that does not overlap the prefix in either buffer.
	suffix := 0
	for suffix < len(old)-prefix && suffix < len(next)-prefix && old[len(old)-1-suffix] == next[len(next)-1-suffix] {
		suffix++
	}
	oldEnd := len(old) - suffix
	newEnd := len(next) - suffix
	// Back the suffix boundary off a split rune in both buffers.
	oldEnd = utf8BoundaryForward(old, oldEnd, prefix)
	newEnd = utf8BoundaryForward(next, newEnd, prefix)

	edit := treesitter.InputEdit{
		StartByte:   uint32(prefix),
		OldEndByte:  uint32(oldEnd),
		NewEndByte:  uint32(newEnd),
		StartPoint:  pointAt(old, prefix),
		OldEndPoint: pointAt(old, oldEnd),
		NewEndPoint: pointAt(next, newEnd),
	}
	if edit.StartByte > edit.OldEndByte || edit.StartByte > edit.NewEndByte {
		return treesitter.InputEdit{}, false
	}
	return edit, true
}

// utf8BoundaryBefore moves offset back to the start of the rune it lands inside.
func utf8BoundaryBefore(buf []byte, offset int) int {
	for offset > 0 && offset < len(buf) && !utf8.RuneStart(buf[offset]) {
		offset--
	}
	return offset
}

// utf8BoundaryForward moves offset forward to the next rune boundary, never below
// floor.
func utf8BoundaryForward(buf []byte, offset, floor int) int {
	if offset < floor {
		return floor
	}
	for offset < len(buf) && !utf8.RuneStart(buf[offset]) {
		offset++
	}
	return offset
}

// pointAt returns the byte-column Point at a byte offset.
func pointAt(buf []byte, offset int) treesitter.Point {
	row, col := uint32(0), uint32(0)
	for i := 0; i < offset && i < len(buf); i++ {
		if buf[i] == '\n' {
			row++
			col = 0
		} else {
			col++
		}
	}
	return treesitter.Point{Row: row, Column: col}
}
