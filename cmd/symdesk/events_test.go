package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/service"
	"github.com/danieljustus/symaira-desktop/internal/sidecar"
	"github.com/fsnotify/fsnotify"
)

// newTestEventService creates a minimal service backed by a temp vault and DB.
func newTestEventService(t *testing.T) *service.Service {
	t.Helper()
	vaultPath := t.TempDir()
	db, err := sidecar.Open(filepath.Join(vaultPath, "sidecar.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return service.New(vaultPath, db)
}

// writeTestMd creates a markdown file with minimal frontmatter in dir.
func writeTestMd(t *testing.T, dir, name, title string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	content := "---\ntitle: " + title + "\n---\n\nBody content\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

// parseNDJSON parses a single NDJSON line from raw bytes.
func parseNDJSON(t *testing.T, raw []byte) map[string]interface{} {
	t.Helper()
	var evt map[string]interface{}
	if err := json.Unmarshal(bytes.TrimSpace(raw), &evt); err != nil {
		t.Fatalf("failed to parse NDJSON: %v\nraw: %s", err, raw)
	}
	return evt
}

// --- processEvent tests ---

func TestProcessEvent_CreateMd(t *testing.T) {
	svc := newTestEventService(t)
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	defer watcher.Close()

	dir := t.TempDir()
	mdPath := writeTestMd(t, dir, "note.md", "Test Note")

	var buf bytes.Buffer
	ev := &DebouncedEvent{Op: fsnotify.Create, Ts: time.Now()}
	processEvent(mdPath, ev, watcher, svc, &buf)

	out := bytes.TrimSpace(buf.Bytes())
	if len(out) == 0 {
		t.Fatal("expected NDJSON output for .md create event, got none")
	}

	evt := parseNDJSON(t, out)
	if evt["event"] != "index_updated" {
		t.Errorf("expected event= index_updated, got %v", evt["event"])
	}
	if evt["path"] != mdPath {
		t.Errorf("expected path=%s, got %v", mdPath, evt["path"])
	}
	if _, ok := evt["ts"]; !ok {
		t.Error("expected ts field in NDJSON output")
	}
}

func TestProcessEvent_WriteMd(t *testing.T) {
	svc := newTestEventService(t)
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	defer watcher.Close()

	dir := t.TempDir()
	mdPath := writeTestMd(t, dir, "note.md", "Original")

	// Write again to simulate a change
	if err := os.WriteFile(mdPath, []byte("---\ntitle: Updated\n---\n\nUpdated body\n"), 0644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	ev := &DebouncedEvent{Op: fsnotify.Write, Ts: time.Now()}
	processEvent(mdPath, ev, watcher, svc, &buf)

	out := bytes.TrimSpace(buf.Bytes())
	if len(out) == 0 {
		t.Fatal("expected NDJSON output for .md write event, got none")
	}

	evt := parseNDJSON(t, out)
	if evt["event"] != "index_updated" {
		t.Errorf("expected event=index_updated, got %v", evt["event"])
	}
}

func TestProcessEvent_RemoveMd(t *testing.T) {
	svc := newTestEventService(t)
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	defer watcher.Close()

	dir := t.TempDir()
	mdPath := writeTestMd(t, dir, "note.md", "To Remove")

	// Create then remove the file to exercise the delete path
	_ = os.Remove(mdPath)

	var buf bytes.Buffer
	ev := &DebouncedEvent{Op: fsnotify.Remove, Ts: time.Now()}
	processEvent(mdPath, ev, watcher, svc, &buf)

	out := bytes.TrimSpace(buf.Bytes())
	if len(out) == 0 {
		t.Fatal("expected NDJSON output for .md remove event, got none")
	}

	evt := parseNDJSON(t, out)
	if evt["event"] != "file_removed" {
		t.Errorf("expected event=file_removed, got %v", evt["event"])
	}
}

func TestProcessEvent_RenameMd(t *testing.T) {
	svc := newTestEventService(t)
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	defer watcher.Close()

	dir := t.TempDir()
	mdPath := writeTestMd(t, dir, "note.md", "To Rename")

	var buf bytes.Buffer
	ev := &DebouncedEvent{Op: fsnotify.Rename, Ts: time.Now()}
	processEvent(mdPath, ev, watcher, svc, &buf)

	out := bytes.TrimSpace(buf.Bytes())
	if len(out) == 0 {
		t.Fatal("expected NDJSON output for .md rename event, got none")
	}

	evt := parseNDJSON(t, out)
	if evt["event"] != "file_removed" {
		t.Errorf("expected event=file_removed, got %v", evt["event"])
	}
}

func TestProcessEvent_NonMdFile(t *testing.T) {
	svc := newTestEventService(t)
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	defer watcher.Close()

	dir := t.TempDir()
	txtPath := filepath.Join(dir, "readme.txt")
	if err := os.WriteFile(txtPath, []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	ev := &DebouncedEvent{Op: fsnotify.Create, Ts: time.Now()}
	processEvent(txtPath, ev, watcher, svc, &buf)

	if buf.Len() > 0 {
		t.Errorf("expected no output for non-.md file, got: %s", buf.Bytes())
	}
}

func TestProcessEvent_CreateDir(t *testing.T) {
	svc := newTestEventService(t)
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	defer watcher.Close()

	dir := t.TempDir()
	subDir := filepath.Join(dir, "subdir")
	if err := os.Mkdir(subDir, 0755); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	ev := &DebouncedEvent{Op: fsnotify.Create, Ts: time.Now()}
	processEvent(subDir, ev, watcher, svc, &buf)

	// Directory create should not produce NDJSON (no .md extension)
	if buf.Len() > 0 {
		t.Errorf("expected no output for directory create, got: %s", buf.Bytes())
	}
}

// --- flushDebounce tests ---

func TestFlushDebounce_MaturedEvents(t *testing.T) {
	svc := newTestEventService(t)
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	defer watcher.Close()

	dir := t.TempDir()
	mdPath := writeTestMd(t, dir, "note.md", "Flushed")

	debounceMap := map[string]*DebouncedEvent{
		mdPath: {Op: fsnotify.Create, Ts: time.Now().Add(-1 * time.Second)}, // matured
	}
	var mu sync.Mutex

	var buf bytes.Buffer
	flushDebounce(debounceMap, &mu, watcher, svc, &buf)

	out := bytes.TrimSpace(buf.Bytes())
	if len(out) == 0 {
		t.Fatal("expected NDJSON output for matured event, got none")
	}

	evt := parseNDJSON(t, out)
	if evt["event"] != "index_updated" {
		t.Errorf("expected event=index_updated, got %v", evt["event"])
	}

	// Map should be cleaned up
	mu.Lock()
	if len(debounceMap) != 0 {
		t.Errorf("expected debounce map to be empty after flush, got %d entries", len(debounceMap))
	}
	mu.Unlock()
}

func TestFlushDebounce_ImmatureEvents(t *testing.T) {
	svc := newTestEventService(t)
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	defer watcher.Close()

	dir := t.TempDir()
	mdPath := writeTestMd(t, dir, "note.md", "Too Fresh")

	debounceMap := map[string]*DebouncedEvent{
		mdPath: {Op: fsnotify.Create, Ts: time.Now()}, // not matured yet
	}
	var mu sync.Mutex

	var buf bytes.Buffer
	flushDebounce(debounceMap, &mu, watcher, svc, &buf)

	if buf.Len() > 0 {
		t.Errorf("expected no output for immature event, got: %s", buf.Bytes())
	}

	// Map should still have the entry
	mu.Lock()
	if len(debounceMap) != 1 {
		t.Errorf("expected debounce map to keep immature entry, got %d entries", len(debounceMap))
	}
	mu.Unlock()
}

func TestFlushDebounce_MixedEvents(t *testing.T) {
	svc := newTestEventService(t)
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	defer watcher.Close()

	dir := t.TempDir()
	maturePath := writeTestMd(t, dir, "mature.md", "Mature")
	freshPath := writeTestMd(t, dir, "fresh.md", "Fresh")

	debounceMap := map[string]*DebouncedEvent{
		maturePath: {Op: fsnotify.Write, Ts: time.Now().Add(-2 * time.Second)}, // matured
		freshPath:  {Op: fsnotify.Write, Ts: time.Now()},                       // immature
	}
	var mu sync.Mutex

	var buf bytes.Buffer
	flushDebounce(debounceMap, &mu, watcher, svc, &buf)

	out := bytes.TrimSpace(buf.Bytes())
	if len(out) == 0 {
		t.Fatal("expected NDJSON output for matured event, got none")
	}

	evt := parseNDJSON(t, out)
	if evt["event"] != "index_updated" {
		t.Errorf("expected event=index_updated, got %v", evt["event"])
	}

	// Only the matured entry should be removed
	mu.Lock()
	if len(debounceMap) != 1 {
		t.Errorf("expected 1 entry remaining, got %d", len(debounceMap))
	}
	if _, ok := debounceMap[freshPath]; !ok {
		t.Error("expected fresh entry to remain in debounce map")
	}
	mu.Unlock()
}

func TestFlushDebounce_EmptyMap(t *testing.T) {
	svc := newTestEventService(t)
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	defer watcher.Close()

	debounceMap := map[string]*DebouncedEvent{}
	var mu sync.Mutex

	var buf bytes.Buffer
	flushDebounce(debounceMap, &mu, watcher, svc, &buf)

	if buf.Len() > 0 {
		t.Errorf("expected no output for empty map, got: %s", buf.Bytes())
	}
}

func TestFlushDebounce_RemoveEvent(t *testing.T) {
	svc := newTestEventService(t)
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	defer watcher.Close()

	dir := t.TempDir()
	mdPath := writeTestMd(t, dir, "gone.md", "Deleted")

	debounceMap := map[string]*DebouncedEvent{
		mdPath: {Op: fsnotify.Remove, Ts: time.Now().Add(-1 * time.Second)},
	}
	var mu sync.Mutex

	var buf bytes.Buffer
	flushDebounce(debounceMap, &mu, watcher, svc, &buf)

	out := bytes.TrimSpace(buf.Bytes())
	if len(out) == 0 {
		t.Fatal("expected NDJSON output for remove event, got none")
	}

	evt := parseNDJSON(t, out)
	if evt["event"] != "file_removed" {
		t.Errorf("expected event=file_removed, got %v", evt["event"])
	}
}

// --- Integration test: full flow with real watcher ---

func TestEventLoop_Integration(t *testing.T) {
	svc := newTestEventService(t)
	dir := t.TempDir()

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	defer watcher.Close()

	if err := watcher.Add(dir); err != nil {
		t.Fatal(err)
	}

	debounceMap := make(map[string]*DebouncedEvent)
	var mu sync.Mutex
	var buf bytes.Buffer

	// Start the debounce processor in background
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 10; i++ {
			time.Sleep(200 * time.Millisecond)
			flushDebounce(debounceMap, &mu, watcher, svc, &buf)
		}
	}()

	// Write a file to trigger events
	mdPath := writeTestMd(t, dir, "live.md", "Live Test")

	// Feed the event into the debounce map (simulating the main loop)
	mu.Lock()
	debounceMap[mdPath] = &DebouncedEvent{
		Op: fsnotify.Create,
		Ts: time.Now(),
	}
	mu.Unlock()

	// Wait for debounce to flush (500ms debounce + some margin)
	time.Sleep(800 * time.Millisecond)

	// Also write a second file to test multiple events
	mdPath2 := writeTestMd(t, dir, "live2.md", "Live Test 2")
	mu.Lock()
	debounceMap[mdPath2] = &DebouncedEvent{
		Op: fsnotify.Write,
		Ts: time.Now(),
	}
	mu.Unlock()

	time.Sleep(800 * time.Millisecond)

	// Signal the background goroutine to stop
	<-done

	out := bytes.TrimSpace(buf.Bytes())
	if len(out) == 0 {
		t.Fatal("expected NDJSON output from integration test, got none")
	}

	// Parse multiple NDJSON lines
	lines := strings.Split(string(out), "\n")
	if len(lines) < 1 {
		t.Fatalf("expected at least 1 NDJSON line, got %d", len(lines))
	}

	// Verify first event
	evt1 := parseNDJSON(t, []byte(lines[0]))
	if evt1["event"] != "index_updated" {
		t.Errorf("first event: expected event=index_updated, got %v", evt1["event"])
	}

	// Verify second event if present
	if len(lines) >= 2 {
		evt2 := parseNDJSON(t, []byte(lines[1]))
		if evt2["event"] != "index_updated" {
			t.Errorf("second event: expected event=index_updated, got %v", evt2["event"])
		}
	}
}

func TestProcessEvent_MdWithoutFrontmatter(t *testing.T) {
	svc := newTestEventService(t)
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	defer watcher.Close()

	dir := t.TempDir()
	// Create a .md file without frontmatter — ParseFile still succeeds (empty frontmatter)
	mdPath := filepath.Join(dir, "plain.md")
	if err := os.WriteFile(mdPath, []byte("Just plain text, no frontmatter\n"), 0644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	ev := &DebouncedEvent{Op: fsnotify.Create, Ts: time.Now()}
	processEvent(mdPath, ev, watcher, svc, &buf)

	out := bytes.TrimSpace(buf.Bytes())
	if len(out) == 0 {
		t.Fatal("expected NDJSON output for .md file without frontmatter, got none")
	}

	evt := parseNDJSON(t, out)
	// ParseFile succeeds even without frontmatter, so event is index_updated
	if evt["event"] != "index_updated" {
		t.Errorf("expected event=index_updated, got %v", evt["event"])
	}
	if evt["path"] != mdPath {
		t.Errorf("expected path=%s, got %v", mdPath, evt["path"])
	}
}

// --- accumulateDebounce tests (issue #647) ---

func TestAccumulateDebounce_CreateThenWriteStaysCreate(t *testing.T) {
	m := make(map[string]*DebouncedEvent)
	accumulateDebounce(m, "/vault/note.md", fsnotify.Create)
	accumulateDebounce(m, "/vault/note.md", fsnotify.Write)
	accumulateDebounce(m, "/vault/note.md", fsnotify.Chmod)

	ev := m["/vault/note.md"]
	if ev == nil {
		t.Fatal("expected debounce entry")
	}
	if ev.Op&fsnotify.Create == 0 {
		t.Errorf("op = %v, want Create bit preserved after Write/Chmod", ev.Op)
	}
}

func TestAccumulateDebounce_NewEntryKeepsFreshTimestamp(t *testing.T) {
	m := make(map[string]*DebouncedEvent)
	before := time.Now()
	accumulateDebounce(m, "/vault/a.md", fsnotify.Write)
	if m["/vault/a.md"].Ts.Before(before) {
		t.Error("fresh entry timestamp predates the call")
	}
}

// TestEventsAccumulatedCreateEmitsNDJSON covers the full debounce path
// (issue #647): a Create followed by Write/Chmod on a new .md file must
// still flush as one event (index_updated for a new file), not a change
// event with no prior index entry.
func TestEventsAccumulatedCreateEmitsNDJSON(t *testing.T) {
	svc := newTestEventService(t)
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = watcher.Close() }()

	dir := t.TempDir()
	mdPath := writeTestMd(t, dir, "repro-notiz.md", "Repro Notiz")

	m := make(map[string]*DebouncedEvent)
	// The usual event burst an editor produces for one atomic save.
	accumulateDebounce(m, mdPath, fsnotify.Create)
	accumulateDebounce(m, mdPath, fsnotify.Write)
	accumulateDebounce(m, mdPath, fsnotify.Chmod)
	// Backdate past the 500ms debounce window so flushDebounce emits.
	m[mdPath].Ts = time.Now().Add(-600 * time.Millisecond)

	var buf bytes.Buffer
	flushDebounce(m, &sync.Mutex{}, watcher, svc, &buf)

	out := bytes.TrimSpace(buf.Bytes())
	if len(out) == 0 {
		t.Fatal("expected NDJSON output for the accumulated create burst, got none")
	}
	evt := parseNDJSON(t, out)
	if evt["event"] != "index_updated" {
		t.Errorf("event = %v, want index_updated (create preserved through Write/Chmod)", evt["event"])
	}
}
