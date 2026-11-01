package ingest

// Go oracle for the inbox directory watcher (RUST-010, INGEST-004): debounce,
// stability check, forced enqueue after the maximum pending age, move to the
// processing directory, duplicate recording, pre-queue failure handling with the
// error sidecar, ignore rules and the initial recursive scan. Deterministic
// scenarios drive the watcher with a fake clock; "live" scenarios run the real
// fsnotify loop with a short real stability window and only compare the eventual
// outcome (jobs, documents, file trees), never timings.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/ingest/internal/store"
	"github.com/danieljustus/symaira-desktop/scripts/rust-port/fixtureoracle"
)

type watchStep struct {
	Op      string `json:"op"`
	Path    string `json:"path,omitempty"`
	To      string `json:"to,omitempty"`
	Content string `json:"content,omitempty"`
	MS      int64  `json:"ms,omitempty"`
	N       int    `json:"n,omitempty"`
	Secs    int64  `json:"secs,omitempty"`
}

type watchOpts struct {
	StableForMS int64  `json:"stable_for_ms"`
	Processing  string `json:"processing,omitempty"`
	Processed   string `json:"processed,omitempty"`
	Failed      string `json:"failed,omitempty"`
}

type watchScenario struct {
	ID    string      `json:"id"`
	Live  bool        `json:"live,omitempty"`
	Opts  watchOpts   `json:"opts"`
	Steps []watchStep `json:"steps"`
}

type watchCase struct {
	Scenario watchScenario               `json:"scenario"`
	Results  []string                    `json:"results"`
	Files    []fsEntry                   `json:"files"`
	Tables   map[string][]map[string]any `json:"tables"`
	Logs     []string                    `json:"logs,omitempty"`
}

var reSidecarTime = regexp.MustCompile(`"failed_at": "[^"]*"`)

func runWatchScenario(t *testing.T, sc watchScenario) watchCase {
	root := t.TempDir()
	if r, err := filepath.EvalSymlinks(root); err == nil {
		root = r
	}
	inbox := filepath.Join(root, "inbox")
	if err := os.MkdirAll(inbox, 0o700); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(root, "db", "docs.db"))
	if err != nil {
		t.Fatal(err)
	}
	var logBuf bytes.Buffer
	var logMu sync.Mutex
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetFlags(0)
	log.SetOutput(writerFunc(func(p []byte) (int, error) {
		logMu.Lock()
		defer logMu.Unlock()
		return logBuf.Write(p)
	}))
	defer func() { log.SetOutput(prevOut); log.SetFlags(prevFlags) }()

	abs := func(rel string) string { return filepath.Join(root, rel) }
	opts := WatcherOptions{StableFor: time.Duration(sc.Opts.StableForMS) * time.Millisecond}
	if sc.Opts.Processing != "" {
		opts.ProcessingDir = abs(sc.Opts.Processing)
	}
	if sc.Opts.Processed != "" {
		opts.ProcessedDir = abs(sc.Opts.Processed)
	}
	if sc.Opts.Failed != "" {
		opts.FailedDir = abs(sc.Opts.Failed)
	}
	var clk *fakeClock
	if !sc.Live {
		clk = newFakeClock()
		opts.Clock = clk
	}
	w, err := NewWatcherWithOptions(st, inbox, opts)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	norm := func(s string) string {
		s = strings.ReplaceAll(s, root, "<ROOT>")
		return reSidecarTime.ReplaceAllString(s, `"failed_at": "<TS>"`)
	}
	db := func() *sql.DB {
		d, e := sql.Open("sqlite", "file:"+filepath.Join(root, "db", "docs.db")+"?mode=ro")
		if e != nil {
			t.Fatal(e)
		}
		return d
	}
	countJobs := func() int {
		d := db()
		defer func() { _ = d.Close() }()
		var n int
		_ = d.QueryRow("SELECT COUNT(*) FROM jobs").Scan(&n)
		return n
	}
	var results []string
	started := false
	for _, step := range sc.Steps {
		res := ""
		switch step.Op {
		case "write":
			p := abs(step.Path)
			if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(step.Content), 0o600); err != nil {
				t.Fatal(err)
			}
		case "append":
			f, err := os.OpenFile(abs(step.Path), os.O_APPEND|os.O_WRONLY, 0o600) //nolint:gosec // test scratch
			if err != nil {
				t.Fatal(err)
			}
			_, _ = f.WriteString(step.Content)
			_ = f.Close()
		case "mkdir":
			if err := os.MkdirAll(abs(step.Path), 0o700); err != nil {
				t.Fatal(err)
			}
		case "remove":
			if err := os.RemoveAll(abs(step.Path)); err != nil {
				t.Fatal(err)
			}
		case "rename":
			if err := os.Rename(abs(step.Path), abs(step.To)); err != nil {
				t.Fatal(err)
			}
		case "symlink":
			if err := os.Symlink(step.To, abs(step.Path)); err != nil {
				t.Fatal(err)
			}
		case "set-mtime":
			ts := time.Unix(step.Secs, 0)
			if err := os.Chtimes(abs(step.Path), ts, ts); err != nil {
				t.Fatal(err)
			}
		case "debounce":
			w.debounceFile(ctx, abs(step.Path))
		case "cancel":
			w.cancelDebounce(abs(step.Path))
		case "advance":
			clk.Advance(time.Duration(step.MS) * time.Millisecond)
		case "enqueue":
			if err := w.enqueueFile(ctx, abs(step.Path)); err != nil {
				res = norm(err.Error())
			}
		case "start":
			if err := w.Start(ctx); err != nil {
				res = norm(err.Error())
			} else {
				started = true
			}
		case "pending":
			w.mu.Lock()
			var keys []string
			for k, v := range w.pending {
				if fi, err := os.Lstat(k); err == nil && fi.IsDir() {
					keys = append(keys, fmt.Sprintf("%s dir", norm(k)))
					continue
				}
				keys = append(keys, fmt.Sprintf("%s size=%d", norm(k), v.lastSize))
			}
			w.mu.Unlock()
			sort.Strings(keys)
			res = strings.Join(keys, "; ")
		case "wait-jobs":
			deadline := time.Now().Add(30 * time.Second)
			for time.Now().Before(deadline) && countJobs() < step.N {
				time.Sleep(25 * time.Millisecond)
			}
			if countJobs() < step.N {
				t.Fatalf("%s: timed out waiting for %d jobs (have %d)", sc.ID, step.N, countJobs())
			}
		case "pause":
			time.Sleep(time.Duration(step.MS) * time.Millisecond)
		default:
			t.Fatalf("unknown step %q", step.Op)
		}
		results = append(results, res)
	}
	if started {
		cancel()
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			logMu.Lock()
			done := strings.Contains(logBuf.String(), "Shutting down event loop...")
			logMu.Unlock()
			if done {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	_ = w.Close()
	_ = st.Close()

	c := watchCase{Scenario: sc, Results: results, Tables: map[string][]map[string]any{}}
	if c.Results == nil {
		c.Results = []string{}
	}
	d := db()
	defer func() { _ = d.Close() }()
	for _, tbl := range []string{"documents", "jobs"} {
		c.Tables[tbl] = dumpTable(t, d, tbl, norm)
	}
	c.Files = []fsEntry{}
	for _, e := range snapshotProcessing(t, root, "", norm) {
		if e.Path == "." || strings.HasPrefix(e.Path, "db") {
			continue
		}
		if e.Type == "file" {
			if raw, err := base64.StdEncoding.DecodeString(e.B64); err == nil && strings.HasSuffix(e.Path, ".error.json") {
				e.B64 = base64.StdEncoding.EncodeToString([]byte(norm(string(raw))))
			}
		}
		c.Files = append(c.Files, e)
	}
	if !sc.Live {
		logMu.Lock()
		for _, l := range strings.Split(strings.TrimRight(logBuf.String(), "\n"), "\n") {
			if l != "" {
				c.Logs = append(c.Logs, norm(l))
			}
		}
		logMu.Unlock()
	}
	return c
}

func watcherScenarios() []watchScenario {
	w := func(path, content string) watchStep { return watchStep{Op: "write", Path: path, Content: content} }
	deb := func(path string) watchStep { return watchStep{Op: "debounce", Path: path} }
	adv := func(ms int64) watchStep { return watchStep{Op: "advance", MS: ms} }
	pend := watchStep{Op: "pending"}
	enq := func(path string) watchStep { return watchStep{Op: "enqueue", Path: path} }
	mt := func(path string, secs int64) watchStep { return watchStep{Op: "set-mtime", Path: path, Secs: secs} }
	base := watchOpts{StableForMS: 1000}
	withDirs := watchOpts{StableForMS: 1000, Processing: "work/processing", Failed: "work/failed", Processed: "work/processed"}
	var out []watchScenario
	add := func(sc watchScenario) { out = append(out, sc) }

	add(watchScenario{ID: "stable-file-enqueued", Opts: base, Steps: []watchStep{w("inbox/a.txt", "hello"), deb("inbox/a.txt"), pend, adv(999), adv(1), pend}})
	add(watchScenario{ID: "growing-file-reschedules", Opts: base, Steps: []watchStep{
		w("inbox/g.txt", "one"), mt("inbox/g.txt", 1700000000), deb("inbox/g.txt"),
		w("inbox/g.txt", "one two"), mt("inbox/g.txt", 1700000100), adv(1000), pend,
		adv(1000), pend}})
	add(watchScenario{ID: "same-size-new-mtime-reschedules", Opts: base, Steps: []watchStep{
		w("inbox/m.txt", "abc"), mt("inbox/m.txt", 1700000000), deb("inbox/m.txt"),
		w("inbox/m.txt", "xyz"), mt("inbox/m.txt", 1700000200), adv(1000), pend, adv(1000), pend}})
	add(watchScenario{ID: "debounce-resets-timer", Opts: base, Steps: []watchStep{
		w("inbox/r.txt", "r"), deb("inbox/r.txt"), adv(600), deb("inbox/r.txt"), adv(600), pend, adv(400), pend}})
	add(watchScenario{ID: "removed-before-stable", Opts: base, Steps: []watchStep{
		w("inbox/x.txt", "x"), deb("inbox/x.txt"), {Op: "remove", Path: "inbox/x.txt"}, adv(1000), pend}})
	add(watchScenario{ID: "debounce-missing-file-clears-pending", Opts: base, Steps: []watchStep{
		w("inbox/y.txt", "y"), deb("inbox/y.txt"), {Op: "remove", Path: "inbox/y.txt"}, deb("inbox/y.txt"), pend}})
	add(watchScenario{ID: "cancel-debounce", Opts: base, Steps: []watchStep{
		w("inbox/c.txt", "c"), deb("inbox/c.txt"), {Op: "cancel", Path: "inbox/c.txt"}, pend, adv(2000), pend}})
	add(watchScenario{ID: "directory-is-dropped-at-check", Opts: base, Steps: []watchStep{
		{Op: "mkdir", Path: "inbox/sub"}, deb("inbox/sub"), pend, adv(1000), pend}})
	add(watchScenario{ID: "symlink-to-directory-is-dropped", Opts: base, Steps: []watchStep{
		{Op: "mkdir", Path: "inbox/real"}, {Op: "symlink", Path: "inbox/link", To: "real"}, deb("inbox/link"), adv(1000), pend}})
	add(watchScenario{ID: "forced-enqueue-after-max-pending-age", Opts: watchOpts{StableForMS: 100000}, Steps: []watchStep{
		w("inbox/f.txt", "1"), mt("inbox/f.txt", 1700000000), deb("inbox/f.txt"),
		w("inbox/f.txt", "12"), mt("inbox/f.txt", 1700000010), adv(100000),
		w("inbox/f.txt", "123"), mt("inbox/f.txt", 1700000020), adv(100000),
		w("inbox/f.txt", "1234"), mt("inbox/f.txt", 1700000030), adv(100000),
		pend,
		w("inbox/f.txt", "12345"), mt("inbox/f.txt", 1700000040), adv(100000), pend}})
	add(watchScenario{ID: "moves-to-processing-dir", Opts: withDirs, Steps: []watchStep{
		w("inbox/p.txt", "payload"), deb("inbox/p.txt"), adv(1000)}})
	add(watchScenario{ID: "processing-dir-name-collision", Opts: withDirs, Steps: []watchStep{
		w("work/processing/dup.txt", "occupied"),
		w("inbox/dup.txt", "fresh"), deb("inbox/dup.txt"), adv(1000),
		w("inbox/dup.txt", "fresh again"), deb("inbox/dup.txt"), adv(1000),
		w("inbox/a.b.c", "dots"), w("work/processing/a.b.c", "x"), deb("inbox/a.b.c"), adv(1000)}})
	add(watchScenario{ID: "duplicate-content-records-skipped-job", Opts: withDirs, Steps: []watchStep{
		w("inbox/one.txt", "same bytes"), deb("inbox/one.txt"), adv(1000),
		w("inbox/two.txt", "same bytes"), deb("inbox/two.txt"), adv(1000)}})
	add(watchScenario{ID: "undetectable-file-goes-to-failed-with-sidecar", Opts: withDirs, Steps: []watchStep{
		w("inbox/bad.bin", "\x00\x01\x02 not a supported type"), deb("inbox/bad.bin"), adv(1000)}})
	add(watchScenario{ID: "undetectable-file-without-failed-dir", Opts: watchOpts{StableForMS: 1000, Processing: "work/processing"}, Steps: []watchStep{
		w("inbox/bad.bin", "\x00\x01\x02 not a supported type"), deb("inbox/bad.bin"), adv(1000)}})
	add(watchScenario{ID: "failed-name-collision-numbering", Opts: withDirs, Steps: []watchStep{
		w("work/failed/bad.bin", "old"),
		w("inbox/bad.bin", "\x00\x01\x02 not a supported type"), deb("inbox/bad.bin"), adv(1000)}})
	add(watchScenario{ID: "enqueue-direct-missing-file", Opts: withDirs, Steps: []watchStep{enq("inbox/ghost.txt")}})
	add(watchScenario{ID: "enqueue-direct-text", Opts: withDirs, Steps: []watchStep{w("inbox/d.txt", "direct"), enq("inbox/d.txt")}})
	add(watchScenario{ID: "enqueue-without-processing-dir-keeps-file-in-inbox", Opts: base, Steps: []watchStep{w("inbox/k.txt", "keep"), enq("inbox/k.txt")}})
	add(watchScenario{ID: "enqueue-same-file-twice-after-restore", Opts: base, Steps: []watchStep{
		w("inbox/t.txt", "twice"), enq("inbox/t.txt"), enq("inbox/t.txt")}})
	add(watchScenario{ID: "start-scans-existing-files-recursively", Opts: withDirs, Steps: []watchStep{
		w("inbox/top.txt", "top level"), w("inbox/nested/deeper/inner.txt", "inner file"), w("inbox/.hidden.txt", "hidden"),
		w("inbox/skip.swp", "swap"), w("inbox/skip.tmp", "tmp"), w("inbox/skip~", "backup"),
		w("work/processing/old.txt", "already processing"), w("work/failed/f.txt", "failed one"),
		{Op: "start"}, pend, adv(1000), adv(1000)}})
	add(watchScenario{ID: "start-ignores-hidden-directory", Opts: base, Steps: []watchStep{
		w("inbox/.cache/inside.txt", "inside hidden"), w("inbox/visible/ok.txt", "visible"), {Op: "start"}, pend, adv(1000)}})
	add(watchScenario{ID: "start-missing-inbox", Opts: base, Steps: []watchStep{{Op: "remove", Path: "inbox"}, {Op: "start"}}})
	add(watchScenario{ID: "start-processing-dir-inside-inbox-is-ignored", Opts: watchOpts{StableForMS: 1000, Processing: "inbox/_processing", Failed: "inbox/_failed", Processed: "inbox/_processed"}, Steps: []watchStep{
		w("inbox/in.txt", "in"), w("inbox/_processing/a.txt", "a"), w("inbox/_failed/b.txt", "b"), w("inbox/_processed/c.txt", "c"),
		{Op: "start"}, pend, adv(1000)}})
	add(watchScenario{ID: "relative-ignored-extensions", Opts: base, Steps: []watchStep{
		w("inbox/a.SWP", "upper"), w("inbox/a.swx", "swx"), w("inbox/real.txt", "real"), {Op: "start"}, pend}})

	// live fsnotify loop: eventual outcomes only
	// 3 s stability window: the growing-file case appends after 80 ms pauses,
	// which a saturated gate host stretched past a 250 ms window.
	live := watchOpts{StableForMS: 3000, Processing: "work/processing", Failed: "work/failed", Processed: "work/processed"}
	add(watchScenario{ID: "live-create-after-start", Live: true, Opts: live, Steps: []watchStep{
		{Op: "start"}, w("inbox/a.txt", "live file"), {Op: "wait-jobs", N: 1}}})
	add(watchScenario{ID: "live-growing-file-waits-until-stable", Live: true, Opts: live, Steps: []watchStep{
		{Op: "start"}, w("inbox/grow.txt", "part 1\n"),
		{Op: "pause", MS: 80}, {Op: "append", Path: "inbox/grow.txt", Content: "part 2\n"},
		{Op: "pause", MS: 80}, {Op: "append", Path: "inbox/grow.txt", Content: "part 3\n"},
		{Op: "wait-jobs", N: 1}}})
	add(watchScenario{ID: "live-new-subdirectory-is-watched", Live: true, Opts: live, Steps: []watchStep{
		{Op: "start"}, {Op: "mkdir", Path: "inbox/newdir"}, {Op: "pause", MS: 400},
		w("inbox/newdir/b.txt", "inside new dir"), {Op: "wait-jobs", N: 1}}})
	add(watchScenario{ID: "live-removed-before-stable-and-ignored-names", Live: true, Opts: live, Steps: []watchStep{
		{Op: "start"}, w("inbox/gone.txt", "gone"), {Op: "remove", Path: "inbox/gone.txt"},
		w("inbox/.dot", "dot"), w("inbox/x.swp", "swp"), w("inbox/x.tmp", "tmp"), w("inbox/x~", "tilde"),
		{Op: "pause", MS: 600}, w("inbox/zz-sentinel.txt", "sentinel"), {Op: "wait-jobs", N: 1}, {Op: "pause", MS: 600}}})
	add(watchScenario{ID: "live-duplicate-content-skipped", Live: true, Opts: live, Steps: []watchStep{
		{Op: "start"}, w("inbox/one.txt", "dup bytes"), {Op: "wait-jobs", N: 1},
		w("inbox/two.txt", "dup bytes"), {Op: "wait-jobs", N: 2}}})
	add(watchScenario{ID: "live-undetectable-file-fails-with-sidecar", Live: true, Opts: live, Steps: []watchStep{
		{Op: "start"}, w("inbox/bad.bin", "\x00\x01\x02 not supported"), {Op: "pause", MS: 1000}, w("inbox/ok.txt", "fine"), {Op: "wait-jobs", N: 1}}})
	add(watchScenario{ID: "live-processing-dir-inside-inbox", Live: true, Opts: watchOpts{StableForMS: 250, Processing: "inbox/_processing", Failed: "inbox/_failed", Processed: "inbox/_processed"}, Steps: []watchStep{
		{Op: "start"}, w("inbox/a.txt", "inside"), {Op: "wait-jobs", N: 1}, {Op: "pause", MS: 800}}})
	return out
}

func TestPortIngestWatcherContract(t *testing.T) {
	var cases []watchCase
	for _, sc := range watcherScenarios() {
		sc := sc
		t.Run(sc.ID, func(t *testing.T) {
			cases = append(cases, runWatchScenario(t, sc))
		})
	}
	if t.Failed() {
		return
	}
	jobs, failed, skipped := 0, 0, 0
	for _, c := range cases {
		jobs += len(c.Tables["jobs"])
		for _, j := range c.Tables["jobs"] {
			if j["status"] == "skipped" {
				skipped++
			}
		}
		for _, f := range c.Files {
			if strings.HasSuffix(f.Path, ".error.json") {
				failed++
			}
		}
	}
	if len(cases) < 30 || jobs < 20 || failed < 3 || skipped < 2 {
		t.Fatalf("oracle too thin: %d scenarios %d jobs %d sidecars %d skipped", len(cases), jobs, failed, skipped)
	}
	hashes := map[string]string{}
	_, file, _, _ := runtime.Caller(0)
	src := filepath.Dir(file)
	for _, f := range []string{"watcher.go", "clock.go", "watcher_port_test.go"} {
		b, e := os.ReadFile(filepath.Join(src, f)) //nolint:gosec // repository source
		if e != nil {
			t.Fatal(e)
		}
		hashes[f] = fmt.Sprintf("%x", sha256.Sum256(bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))))
	}
	content, e := json.MarshalIndent(map[string]any{
		"schema_version": 1,
		"oracle":         fixtureoracle.Current(),
		"source_hashes":  hashes,
		"cases":          cases,
		"normalizations": []string{"temp root <ROOT>, RFC3339 timestamps <TS>, sidecar failed_at <TS>; live scenarios omit logs"},
	}, "", " ")
	if e != nil {
		t.Fatal(e)
	}
	content = append(content, '\n')
	out := filepath.Join(src, "../../../../testdata/port/ingest/watcher.json")
	if os.Getenv("PORT_GENERATE") == "1" {
		if e = os.WriteFile(out, content, 0o600); e != nil {
			t.Fatal(e)
		}
		return
	}
	old, e := os.ReadFile(out) //nolint:gosec // repository fixture
	if e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(old, content) {
		t.Fatal("watcher fixture drift; regenerate deliberately")
	}
}
