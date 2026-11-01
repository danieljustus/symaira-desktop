// Command pggen freezes the Go PostgreSQL self-host store (internal/storage
// PostgresBackend, contract row PG-001) as a Rust replay fixture. Every
// scenario runs against a real, disposable PostgreSQL server named by
// SYMDESK_PG_ADMIN_DSN (a superuser DSN; see
// scripts/rust-port/postgres-sidecar.sh) and records each step's observable
// result: values, key ordering, error classes with SQLSTATE and server text,
// the DDL the store creates, pooled and concurrent behavior against locks,
// and what an existing table of an older or foreign shape does to Open and
// to each operation.
//
// Modes:
//
//	pggen                    generate: needs SYMDESK_PG_ADMIN_DSN, writes the fixture
//	pggen --check            with SYMDESK_PG_ADMIN_DSN: re-run every scenario and require
//	                         byte-identical output; without it: validate the committed
//	                         fixture's structure and oracle identity only (the portgen
//	                         fixture gate, which has no server). `make postgres-integration`
//	                         always runs the live form.
//
// Scenario databases are named symdesk_pg_go_<scenario>; database names are
// normalized to {db} in recorded text. Role passwords are derived from the
// admin password at run time ({pw:ROLE}) so nothing secret is recorded.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/danieljustus/symaira-desktop/internal/storage"
	"github.com/danieljustus/symaira-desktop/scripts/rust-port/fixtureoracle"
	"github.com/danieljustus/symaira-desktop/scripts/rust-port/inventory"
)

const (
	defaultOutput  = "testdata/port/storage/postgres.json"
	adminDSNEnv    = "SYMDESK_PG_ADMIN_DSN"
	dbPrefix       = "symdesk_pg_go_"
	inlineValueMax = 4096
)

type document struct {
	SchemaVersion int              `json:"schema_version"`
	Oracle        inventory.Oracle `json:"oracle"`
	ServerMajor   int              `json:"server_major"`
	Scenarios     []scenario       `json:"scenarios"`
}

type scenario struct {
	ID          string   `json:"id"`
	Description string   `json:"description"`
	Roles       []string `json:"roles,omitempty"`
	Steps       []step   `json:"steps"`
	// SQLiteDiffs lists the steps whose outcome differs from the SQLite
	// backend run through the same operations (parity scenarios only).
	SQLiteDiffs *[]int `json:"sqlite_diffs,omitempty"`
	// Final is the store table after the last step as <key>=<hex value>
	// lines in byte order, or null when the table does not exist.
	Final *[]string `json:"final"`
}

type valueSpec struct {
	B64  string `json:"b64,omitempty"`
	Fill *int   `json:"fill,omitempty"`
	Len  int    `json:"len,omitempty"`
}

type dsnSpec struct {
	Form     string            `json:"form,omitempty"`     // url (default) | keyword
	User     string            `json:"user,omitempty"`     // default postgres
	Password string            `json:"password,omitempty"` // admin (default) | role | wrong
	Host     string            `json:"host,omitempty"`     // server (default) | closed
	Database string            `json:"database,omitempty"` // scenario (default) | missing | literal name
	Params   map[string]string `json:"params,omitempty"`
	Raw      *string           `json:"raw,omitempty"`
}

type step struct {
	Op        string     `json:"op"`
	As        string     `json:"as,omitempty"`
	On        string     `json:"on,omitempty"`
	Session   string     `json:"session,omitempty"`
	DB        string     `json:"db,omitempty"`
	SQL       string     `json:"sql,omitempty"`
	DSN       *dsnSpec   `json:"dsn,omitempty"`
	Key       *string    `json:"key,omitempty"`
	Prefix    *string    `json:"prefix,omitempty"`
	Value     *valueSpec `json:"value,omitempty"`
	Values    []string   `json:"values_b64,omitempty"`
	TimeoutMs int        `json:"timeout_ms,omitempty"`
	ID        string     `json:"id,omitempty"`
	Task      *step      `json:"task,omitempty"`
	Ms        int        `json:"ms,omitempty"`
	Count     int        `json:"count,omitempty"`
	Result    *result    `json:"result"`
}

type errInfo struct {
	Class    string `json:"class"`
	SQLState string `json:"sqlstate,omitempty"`
	Prefix   string `json:"prefix,omitempty"`
	Message  string `json:"message,omitempty"`
}

type valueInfo struct {
	Len    int    `json:"len"`
	B64    string `json:"b64,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
}

type entryInfo struct {
	Key   string    `json:"key"`
	Value valueInfo `json:"value"`
}

type result struct {
	Err     *errInfo        `json:"err,omitempty"`
	Value   *valueInfo      `json:"value,omitempty"`
	Entries *[]entryInfo    `json:"entries,omitempty"`
	Rows    *[][]*string    `json:"rows,omitempty"`
	Matched *bool           `json:"matched,omitempty"`
	Pending *bool           `json:"pending,omitempty"`
	Summary map[string]any  `json:"summary,omitempty"`
	Extra   json.RawMessage `json:"-"`
}

func main() {
	output := flag.String("output", defaultOutput, "fixture path")
	check := flag.Bool("check", false, "verify the fixture instead of writing it")
	commit := flag.String("oracle-commit", fixtureoracle.Defaults().Commit, "Go oracle commit")
	release := flag.String("oracle-release", fixtureoracle.Defaults().Release, "Go oracle release")
	flag.Parse()

	if *check {
		oracle, err := inventory.ResolveCheckOracle(inventory.Oracle{Commit: *commit, Release: *release}, flag.CommandLine, *output)
		if err != nil {
			fatal("resolve replay oracle: %v", err)
		}
		*commit, *release = oracle.Commit, oracle.Release
	}
	identityRoot, err := fixtureoracle.FindRepositoryRoot(".")
	if err != nil {
		fatal("resolve fixture source: %v", err)
	}
	if err := fixtureoracle.ValidateSource(identityRoot, inventory.Oracle{Commit: *commit, Release: *release}); err != nil {
		fatal("verify selected fixture source: %v", err)
	}
	oracle := inventory.Oracle{Commit: *commit, Release: *release}

	adminDSN := os.Getenv(adminDSNEnv)
	if *check && adminDSN == "" {
		existing, err := os.ReadFile(*output)
		if err != nil {
			fatal("read fixture: %v", err)
		}
		doc, err := validateFixture(existing, oracle)
		if err != nil {
			fatal("postgres fixture invalid: %v", err)
		}
		steps := 0
		for _, s := range doc.Scenarios {
			steps += len(s.Steps)
		}
		fmt.Printf("PASS postgres fixture structure (%d scenarios, %d steps; live Go replay: make postgres-integration)\n", len(doc.Scenarios), steps)
		return
	}
	if adminDSN == "" {
		fatal("%s is not set: start a disposable server with scripts/rust-port/postgres-sidecar.sh", adminDSNEnv)
	}

	major, err := serverMajor(adminDSN)
	if err != nil {
		fatal("probe server: %v", err)
	}
	doc := document{SchemaVersion: 1, Oracle: oracle, ServerMajor: major}
	for _, spec := range scenarioSpecs() {
		// PGGEN_ONLY narrows debugging runs; the result is never a valid fixture.
		if only := os.Getenv("PGGEN_ONLY"); only != "" && only != spec.ID {
			continue
		}
		sc, err := runScenario(adminDSN, spec)
		if err != nil {
			fatal("scenario %s: %v", spec.ID, err)
		}
		doc.Scenarios = append(doc.Scenarios, sc)
	}
	content, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		fatal("marshal: %v", err)
	}
	content = append(content, '\n')
	if *check {
		existing, err := os.ReadFile(*output)
		if err != nil {
			fatal("read fixture: %v", err)
		}
		if !bytes.Equal(existing, content) {
			fatal("postgres fixture drift; regenerate deliberately: %s", firstDifference(existing, content))
		}
		fmt.Printf("PASS postgres fixture live replay (%d scenarios against PostgreSQL %d)\n", len(doc.Scenarios), major)
		return
	}
	if err := os.MkdirAll(filepath.Dir(*output), 0o750); err != nil {
		fatal("create fixture directory: %v", err)
	}
	if err := os.WriteFile(*output, content, 0o600); err != nil {
		fatal("write fixture: %v", err)
	}
	steps := 0
	for _, s := range doc.Scenarios {
		steps += len(s.Steps)
	}
	fmt.Printf("PASS postgres fixture generated (%d scenarios, %d steps, PostgreSQL %d)\n", len(doc.Scenarios), steps, major)
}

func firstDifference(want, got []byte) string {
	wl, gl := strings.Split(string(want), "\n"), strings.Split(string(got), "\n")
	for i := 0; i < len(wl) && i < len(gl); i++ {
		if wl[i] != gl[i] {
			return fmt.Sprintf("line %d: committed %q, fresh %q", i+1, wl[i], gl[i])
		}
	}
	return fmt.Sprintf("length %d vs %d lines", len(wl), len(gl))
}

// validateFixture is the server-less structural gate used by portgen.
func validateFixture(data []byte, oracle inventory.Oracle) (document, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var doc document
	if err := dec.Decode(&doc); err != nil {
		return doc, err
	}
	if doc.SchemaVersion != 1 {
		return doc, fmt.Errorf("schema_version %d", doc.SchemaVersion)
	}
	if doc.Oracle != oracle {
		return doc, fmt.Errorf("oracle %+v does not match %+v", doc.Oracle, oracle)
	}
	if doc.ServerMajor <= 0 {
		return doc, errors.New("server_major missing")
	}
	seen := map[string]bool{}
	for _, s := range doc.Scenarios {
		if s.ID == "" || seen[s.ID] {
			return doc, fmt.Errorf("scenario id %q empty or duplicated", s.ID)
		}
		seen[s.ID] = true
		if len(s.Steps) == 0 {
			return doc, fmt.Errorf("scenario %s has no steps", s.ID)
		}
		for i := range s.Steps {
			if s.Steps[i].Result == nil {
				return doc, fmt.Errorf("scenario %s step %d has no result", s.ID, i)
			}
		}
	}
	if len(doc.Scenarios) < 20 {
		return doc, fmt.Errorf("only %d scenarios recorded", len(doc.Scenarios))
	}
	return doc, nil
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "FAIL "+strings.TrimSpace(format)+"\n", args...)
	os.Exit(1)
}

// ---------------------------------------------------------------------------
// Execution

type runner struct {
	adminDSN string
	admin    *url.URL
	adminPW  string
	db       string
	ctx      context.Context

	backends map[string]*storage.PostgresBackend
	sessions map[string]*pgx.Conn
	tasks    map[string]*task
	// sqlite mirrors the sequential backend operations of parity scenarios.
	sqlite     *storage.SQLiteBackend
	sqliteDiff []int
	parity     bool
}

type task struct {
	done chan struct{}
	res  *result
}

func runScenario(adminDSN string, spec scenario) (scenario, error) {
	admin, err := url.Parse(adminDSN)
	if err != nil {
		return spec, fmt.Errorf("parse admin DSN: %w", err)
	}
	pw, _ := admin.User.Password()
	r := &runner{
		adminDSN: adminDSN, admin: admin, adminPW: pw,
		db:       dbPrefix + spec.ID,
		ctx:      context.Background(),
		backends: map[string]*storage.PostgresBackend{},
		sessions: map[string]*pgx.Conn{},
		tasks:    map[string]*task{},
		parity:   spec.SQLiteDiffs != nil,
	}
	if r.parity {
		dir, err := os.MkdirTemp("", "pggen-sqlite-")
		if err != nil {
			return spec, err
		}
		defer func() { _ = os.RemoveAll(dir) }()
		if r.sqlite, err = storage.OpenSQLite(filepath.Join(dir, "state.db")); err != nil {
			return spec, err
		}
		defer func() { _ = r.sqlite.Close() }()
	}
	if err := r.createDatabase(spec.Roles); err != nil {
		return spec, err
	}
	defer r.cleanup(spec.Roles)

	out := spec
	out.Steps = make([]step, len(spec.Steps))
	for i := range spec.Steps {
		st := spec.Steps[i]
		st.Result = r.exec(&st, i)
		out.Steps[i] = st
	}
	r.closeAll()
	if r.parity {
		diffs := append([]int{}, r.sqliteDiff...)
		out.SQLiteDiffs = &diffs
	}
	final, err := r.finalState()
	if err != nil {
		return out, err
	}
	out.Final = final
	return out, nil
}

func (r *runner) adminConn(db string) (*pgx.Conn, error) {
	u := *r.admin
	u.Path = "/" + db
	return pgx.Connect(r.ctx, u.String())
}

func (r *runner) rolePassword(role string) string {
	sum := sha256.Sum256([]byte(r.adminPW + "\x00" + role))
	return "pw-" + hex.EncodeToString(sum[:8])
}

func (r *runner) createDatabase(roles []string) error {
	conn, err := r.adminConn("postgres")
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(r.ctx) }()
	for _, role := range roles {
		if _, err := conn.Exec(r.ctx, fmt.Sprintf(`DO $$BEGIN IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = '%s') THEN CREATE ROLE %s LOGIN; END IF; END$$`, role, role)); err != nil {
			return err
		}
		if _, err := conn.Exec(r.ctx, fmt.Sprintf(`ALTER ROLE %s LOGIN PASSWORD '%s'`, role, r.rolePassword(role))); err != nil {
			return err
		}
	}
	if _, err := conn.Exec(r.ctx, fmt.Sprintf(`DROP DATABASE IF EXISTS %q WITH (FORCE)`, r.db)); err != nil {
		return err
	}
	_, err = conn.Exec(r.ctx, fmt.Sprintf(`CREATE DATABASE %q`, r.db))
	return err
}

func (r *runner) cleanup(roles []string) {
	r.closeAll()
	conn, err := r.adminConn("postgres")
	if err != nil {
		return
	}
	defer func() { _ = conn.Close(r.ctx) }()
	_, _ = conn.Exec(r.ctx, fmt.Sprintf(`DROP DATABASE IF EXISTS %q WITH (FORCE)`, r.db))
	for _, role := range roles {
		_, _ = conn.Exec(r.ctx, fmt.Sprintf(`DROP ROLE IF EXISTS %s`, role))
	}
}

func (r *runner) closeAll() {
	for name, t := range r.tasks {
		<-t.done
		delete(r.tasks, name)
	}
	for name, b := range r.backends {
		_ = b.Close()
		delete(r.backends, name)
	}
	for name, s := range r.sessions {
		_ = s.Close(r.ctx)
		delete(r.sessions, name)
	}
}

func (r *runner) finalState() (*[]string, error) {
	conn, err := r.adminConn(r.db)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close(r.ctx) }()
	var exists bool
	if err := conn.QueryRow(r.ctx, `SELECT to_regclass('public.storage_entries') IS NOT NULL`).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, nil
	}
	rows, err := conn.Query(r.ctx, `SELECT storage_key, encode(value::bytea, 'hex') FROM storage_entries ORDER BY storage_key COLLATE "C"`)
	if err != nil {
		// A foreign-shaped table (renamed column ...) has no readable state.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			lines := []string{"unreadable:" + pgErr.Code}
			return &lines, nil
		}
		return nil, err
	}
	defer rows.Close()
	lines := []string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		lines = append(lines, k+"="+v)
	}
	return &lines, rows.Err()
}

func (r *runner) subst(s string) string {
	s = strings.ReplaceAll(s, "{db}", r.db)
	for {
		i := strings.Index(s, "{pw:")
		if i < 0 {
			return s
		}
		j := strings.Index(s[i:], "}")
		role := s[i+4 : i+j]
		s = s[:i] + r.rolePassword(role) + s[i+j+1:]
	}
}

func (r *runner) normalize(s string) string {
	return strings.ReplaceAll(s, r.db, "{db}")
}

func (r *runner) buildDSN(spec *dsnSpec) string {
	if spec == nil {
		spec = &dsnSpec{}
	}
	if spec.Raw != nil {
		return *spec.Raw
	}
	user := spec.User
	if user == "" {
		user = "postgres"
	}
	password := r.adminPW
	switch spec.Password {
	case "role":
		password = r.rolePassword(user)
	case "wrong":
		password = "wrong-" + r.adminPW
	}
	host, port := r.admin.Hostname(), r.admin.Port()
	if spec.Host == "closed" {
		port = "1"
	}
	database := r.db
	switch spec.Database {
	case "", "scenario":
	case "missing":
		database = "symdesk_pg_missing"
	default:
		database = spec.Database
	}
	params := map[string]string{"sslmode": "disable"}
	for k, v := range spec.Params {
		params[k] = v
	}
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if spec.Form == "keyword" {
		parts := []string{"host=" + host, "port=" + port, "user=" + user, "password=" + password, "dbname=" + database}
		for _, k := range keys {
			parts = append(parts, k+"="+params[k])
		}
		return strings.Join(parts, " ")
	}
	q := make([]string, 0, len(keys))
	for _, k := range keys {
		q = append(q, k+"="+strings.ReplaceAll(url.QueryEscape(params[k]), "+", "%20"))
	}
	return fmt.Sprintf("postgres://%s:%s@%s:%s/%s?%s", url.QueryEscape(user), url.QueryEscape(password), host, port, database, strings.Join(q, "&"))
}

func (r *runner) valueBytes(v *valueSpec) []byte {
	if v == nil {
		return nil
	}
	if v.Fill != nil {
		return bytes.Repeat([]byte{byte(*v.Fill)}, v.Len)
	}
	b, err := base64.StdEncoding.DecodeString(v.B64)
	if err != nil {
		panic(err)
	}
	if b == nil {
		b = []byte{}
	}
	return b
}

func info(b []byte) valueInfo {
	if len(b) <= inlineValueMax {
		return valueInfo{Len: len(b), B64: base64.StdEncoding.EncodeToString(b)}
	}
	sum := sha256.Sum256(b)
	return valueInfo{Len: len(b), SHA256: hex.EncodeToString(sum[:])}
}

func (r *runner) backend(name string) *storage.PostgresBackend {
	if name == "" {
		name = "b"
	}
	return r.backends[name]
}

func (r *runner) opContext(st *step) (context.Context, context.CancelFunc) {
	if st.TimeoutMs > 0 {
		return context.WithTimeout(r.ctx, time.Duration(st.TimeoutMs)*time.Millisecond)
	}
	return r.ctx, func() {}
}

func (r *runner) exec(st *step, index int) *result {
	switch st.Op {
	case "open":
		name := st.As
		if name == "" {
			name = "b"
		}
		ctx, cancel := context.WithTimeout(r.ctx, 20*time.Second)
		defer cancel()
		b, err := storage.OpenPostgres(ctx, r.buildDSN(st.DSN))
		if err != nil {
			if os.Getenv("PGGEN_DEBUG") != "" {
				fmt.Fprintf(os.Stderr, "pggen: %s open %s: %v\n", r.db, name, strings.ReplaceAll(err.Error(), r.adminPW, "***"))
			}
			return &result{Err: r.classify(err)}
		}
		r.backends[name] = b
		return &result{}
	case "open_many":
		return r.openMany(st)
	case "close":
		name := st.On
		if name == "" {
			name = "b"
		}
		if b := r.backends[name]; b != nil {
			if err := b.Close(); err != nil {
				return &result{Err: r.classify(err)}
			}
		}
		return &result{}
	case "get", "set", "remove", "query", "get_one_of", "query_digest", "bulk_set":
		res := r.storeOp(st)
		if r.parity && r.sqlite != nil {
			r.mirror(st, res, index)
		}
		return res
	case "sql":
		return r.sqlOp(st)
	case "spawn":
		t := &task{done: make(chan struct{})}
		r.tasks[st.ID] = t
		inner := *st.Task
		go func() {
			defer close(t.done)
			t.res = r.storeOp(&inner)
		}()
		return &result{}
	case "join":
		t := r.tasks[st.ID]
		<-t.done
		delete(r.tasks, st.ID)
		return t.res
	case "expect_pending":
		t := r.tasks[st.ID]
		pending := true
		select {
		case <-t.done:
			pending = false
		default:
		}
		return &result{Pending: &pending}
	case "sleep":
		time.Sleep(time.Duration(st.Ms) * time.Millisecond)
		return &result{}
	}
	panic("unknown op " + st.Op)
}

func (r *runner) openMany(st *step) *result {
	var wg sync.WaitGroup
	errs := make([]error, st.Count)
	opened := make([]*storage.PostgresBackend, st.Count)
	for i := 0; i < st.Count; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(r.ctx, 30*time.Second)
			defer cancel()
			opened[i], errs[i] = storage.OpenPostgres(ctx, r.buildDSN(st.DSN))
		}()
	}
	wg.Wait()
	okCount := 0
	unexpected := 0
	for i := range errs {
		if errs[i] == nil {
			okCount++
			_ = opened[i].Close()
			continue
		}
		info := r.classify(errs[i])
		// The CREATE TABLE IF NOT EXISTS race between concurrent Opens: the
		// loser sees the peer's catalog rows (23505 duplicate key, 42P07
		// relation exists, 42710 composite type exists).
		if info.SQLState != "23505" && info.SQLState != "42P07" && info.SQLState != "42710" {
			if os.Getenv("PGGEN_DEBUG") != "" {
				fmt.Fprintf(os.Stderr, "pggen: open_many unexpected: %v\n", errs[i])
			}
			unexpected++
		}
	}
	return &result{Summary: map[string]any{"at_least_one_opened": okCount >= 1, "unexpected_failures": unexpected}}
}

func (r *runner) storeOp(st *step) *result {
	b := r.backend(st.On)
	if b == nil {
		panic("backend " + st.On + " not open for " + st.Op + " in " + r.db)
	}
	ctx, cancel := r.opContext(st)
	defer cancel()
	key := ""
	if st.Key != nil {
		key = *st.Key
	}
	switch st.Op {
	case "get":
		v, err := b.Get(ctx, key)
		if err != nil {
			return &result{Err: r.classify(err)}
		}
		vi := info(v)
		return &result{Value: &vi}
	case "set":
		if err := b.Set(ctx, key, r.valueBytes(st.Value)); err != nil {
			return &result{Err: r.classify(err)}
		}
		return &result{}
	case "remove":
		if err := b.Remove(ctx, key); err != nil {
			return &result{Err: r.classify(err)}
		}
		return &result{}
	case "query":
		prefix := ""
		if st.Prefix != nil {
			prefix = *st.Prefix
		}
		entries, err := b.Query(ctx, prefix)
		if err != nil {
			return &result{Err: r.classify(err)}
		}
		out := make([]entryInfo, 0, len(entries))
		for _, e := range entries {
			out = append(out, entryInfo{Key: e.Key, Value: info(e.Value)})
		}
		return &result{Entries: &out}
	case "bulk_set":
		for i := 0; i < st.Count; i++ {
			k := fmt.Sprintf("%s%05d", *st.Prefix, i)
			sum := sha256.Sum256([]byte(k))
			if err := b.Set(ctx, k, sum[:8]); err != nil {
				return &result{Err: r.classify(err)}
			}
		}
		return &result{}
	case "query_digest":
		entries, err := b.Query(ctx, *st.Prefix)
		if err != nil {
			return &result{Err: r.classify(err)}
		}
		h := sha256.New()
		for _, e := range entries {
			_, _ = fmt.Fprintf(h, "%s\x00%s\n", e.Key, hex.EncodeToString(e.Value))
		}
		return &result{Summary: map[string]any{"count": len(entries), "sha256": hex.EncodeToString(h.Sum(nil))}}
	case "get_one_of":
		v, err := b.Get(ctx, key)
		if err != nil {
			return &result{Err: r.classify(err)}
		}
		matched := false
		for _, want := range st.Values {
			if want == base64.StdEncoding.EncodeToString(v) {
				matched = true
			}
		}
		return &result{Matched: &matched}
	}
	panic("unreachable")
}

func (r *runner) sqlOp(st *step) *result {
	name := st.Session
	if name == "" {
		name = "s"
	}
	conn := r.sessions[name]
	if conn == nil {
		db := r.db
		if st.DB == "admin" {
			db = "postgres"
		}
		var err error
		if conn, err = r.adminConn(db); err != nil {
			return &result{Err: r.classify(err)}
		}
		r.sessions[name] = conn
	}
	mrr := conn.PgConn().Exec(r.ctx, r.subst(st.SQL))
	results, err := mrr.ReadAll()
	if err != nil {
		return &result{Err: r.classify(err)}
	}
	rows := [][]*string{}
	for _, res := range results {
		for _, row := range res.Rows {
			cells := make([]*string, len(row))
			for i, cell := range row {
				if cell != nil {
					s := r.normalize(string(cell))
					cells[i] = &s
				}
			}
			rows = append(rows, cells)
		}
	}
	return &result{Rows: &rows}
}

// mirror replays a sequential operation against the SQLite backend and notes
// the step when the observable outcome differs from PostgreSQL's.
func (r *runner) mirror(st *step, pg *result, index int) {
	probe := &runner{ctx: r.ctx, db: r.db}
	var sq *result
	key := ""
	if st.Key != nil {
		key = *st.Key
	}
	switch st.Op {
	case "get":
		v, err := r.sqlite.Get(r.ctx, key)
		sq = probe.sqliteResult(err, func() *result { vi := info(v); return &result{Value: &vi} })
	case "set":
		sq = probe.sqliteResult(r.sqlite.Set(r.ctx, key, r.valueBytes(st.Value)), func() *result { return &result{} })
	case "remove":
		sq = probe.sqliteResult(r.sqlite.Remove(r.ctx, key), func() *result { return &result{} })
	case "query":
		prefix := ""
		if st.Prefix != nil {
			prefix = *st.Prefix
		}
		entries, err := r.sqlite.Query(r.ctx, prefix)
		sq = probe.sqliteResult(err, func() *result {
			out := make([]entryInfo, 0, len(entries))
			for _, e := range entries {
				out = append(out, entryInfo{Key: e.Key, Value: info(e.Value)})
			}
			return &result{Entries: &out}
		})
	default:
		return
	}
	if !sameOutcome(pg, sq) {
		if !strings.ContainsRune(key, 0) && !strings.Contains(prefixOf(st), "\x00") {
			panic(fmt.Sprintf("step %d (%s %q): PostgreSQL and SQLite differ outside the documented NUL-key case: %+v vs %+v", index, st.Op, key, pg, sq))
		}
		r.sqliteDiff = append(r.sqliteDiff, index)
	}
}

func prefixOf(st *step) string {
	if st.Prefix == nil {
		return ""
	}
	return *st.Prefix
}

func (r *runner) sqliteResult(err error, ok func() *result) *result {
	if err != nil {
		return &result{Err: r.classify(err)}
	}
	return ok()
}

func sameOutcome(a, b *result) bool {
	if (a.Err == nil) != (b.Err == nil) {
		return false
	}
	if a.Err != nil {
		return a.Err.Class == b.Err.Class
	}
	aj, _ := json.Marshal(struct {
		V *valueInfo
		E *[]entryInfo
	}{a.Value, a.Entries})
	bj, _ := json.Marshal(struct {
		V *valueInfo
		E *[]entryInfo
	}{b.Value, b.Entries})
	return bytes.Equal(aj, bj)
}

func (r *runner) classify(err error) *errInfo {
	text := err.Error()
	var pgErr *pgconn.PgError
	hasPg := errors.As(err, &pgErr)
	switch {
	case errors.Is(err, storage.ErrNotFound):
		return &errInfo{Class: "not_found", Message: r.normalize(text)}
	case errors.Is(err, storage.ErrInvalidKey):
		return &errInfo{Class: "invalid_key", Message: text}
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled):
		return &errInfo{Class: "timeout"}
	case strings.Contains(text, "closed pool"):
		return &errInfo{Class: "closed"}
	case text == "storage: PostgreSQL DSN must not be empty":
		return &errInfo{Class: "empty_dsn", Message: text}
	case strings.HasPrefix(text, "parse PostgreSQL DSN: "):
		return &errInfo{Class: "dsn", Prefix: "parse PostgreSQL DSN: "}
	case strings.HasPrefix(text, "connect to PostgreSQL: "):
		info := &errInfo{Class: "connect", Prefix: "connect to PostgreSQL: "}
		if hasPg {
			info.SQLState = pgErr.Code
			info.Message = r.normalize(pgErr.Message)
		}
		return info
	case hasPg:
		return &errInfo{Class: "server", SQLState: pgErr.Code, Message: r.normalize(text)}
	}
	panic("unclassified error: " + text)
}

func serverMajor(adminDSN string) (int, error) {
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, adminDSN)
	if err != nil {
		return 0, err
	}
	defer func() { _ = conn.Close(ctx) }()
	var num int
	if err := conn.QueryRow(ctx, `SELECT current_setting('server_version_num')::int`).Scan(&num); err != nil {
		return 0, err
	}
	return num / 10000, nil
}
