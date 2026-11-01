package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"math/rand/v2"
	"strings"
)

func p(v string) *string { return &v }

func intp(v int) *int { return &v }

func b64(s string) *valueSpec {
	return &valueSpec{B64: base64.StdEncoding.EncodeToString([]byte(s))}
}

func rawB64(b []byte) *valueSpec {
	return &valueSpec{B64: base64.StdEncoding.EncodeToString(b)}
}

func open(dsn *dsnSpec) step { return step{Op: "open", DSN: dsn} }

func openAs(name string, dsn *dsnSpec) step { return step{Op: "open", As: name, DSN: dsn} }

func closeOn(name string) step { return step{Op: "close", On: name} }

func set(key string, v *valueSpec) step { return step{Op: "set", Key: p(key), Value: v} }

func setStr(key, value string) step { return set(key, b64(value)) }

func get(key string) step { return step{Op: "get", Key: p(key)} }

func remove(key string) step { return step{Op: "remove", Key: p(key)} }

func query(prefix string) step { return step{Op: "query", Prefix: p(prefix)} }

func sqlOn(session, q string) step { return step{Op: "sql", Session: session, SQL: q} }

func spawn(id string, task step) step { return step{Op: "spawn", ID: id, Task: &task} }

func join(id string) step { return step{Op: "join", ID: id} }

func pending(id string) step { return step{Op: "expect_pending", ID: id} }

func sleep(ms int) step { return step{Op: "sleep", Ms: ms} }

func withTimeout(st step, ms int) step { st.TimeoutMs = ms; return st }

func withOn(st step, name string) step { st.On = name; return st }

func asRole(role string) *dsnSpec { return &dsnSpec{User: role, Password: "role"} }

func params(kv ...string) *dsnSpec {
	m := map[string]string{}
	for i := 0; i < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return &dsnSpec{Params: m}
}

func legacyTable(ddl string, inserts ...string) []step {
	out := []step{sqlOn("setup", ddl)}
	for _, q := range inserts {
		out = append(out, sqlOn("setup", q))
	}
	return out
}

func concat(parts ...[]step) []step {
	var out []step
	for _, part := range parts {
		out = append(out, part...)
	}
	return out
}

func scenarioSpecs() []scenario {
	noDiffs := &[]int{}
	return []scenario{
		schemaFresh(),
		{ID: "crud_basic", Description: "set, get, replace, remove, absent keys, empty/nil/binary values, Unicode keys", SQLiteDiffs: noDiffs, Steps: crudBasic()},
		{ID: "invalid_keys", Description: "empty key on every operation; NUL characters in keys and prefixes", SQLiteDiffs: noDiffs, Steps: invalidKeys()},
		{ID: "value_semantics", Description: "nil vs empty vs large values and replacement", SQLiteDiffs: noDiffs, Steps: valueSemantics()},
		{ID: "ordering_byte_collation", Description: "Query order is byte order (COLLATE \"C\") whatever the cluster locale", SQLiteDiffs: noDiffs, Steps: ordering()},
		{ID: "prefix_metacharacters", Description: "prefixes with LIKE metacharacters, longer than any key, multibyte", SQLiteDiffs: noDiffs, Steps: prefixes()},
		{ID: "random_mix", Description: "500 seeded random operations over an overlapping key alphabet", SQLiteDiffs: noDiffs, Steps: randomMix()},
		{ID: "reopen_state", Description: "state survives close and reopen; two stores on one database see each other", Steps: reopenState()},
		{ID: "close_semantics", Description: "repeated Close, operations after Close, reopen", Steps: closeSemantics()},
		{ID: "open_errors", Description: "empty/garbage DSN, wrong password, missing database, closed port, required TLS, keyword form", Steps: openErrors()},
		{ID: "role_privileges", Description: "read-only role and a role that may not create in the schema", Roles: []string{"symdesk_pg_ro", "symdesk_pg_nocreate"}, Steps: rolePrivileges()},
		{ID: "legacy_text_value", Description: "existing table with a text value column", Steps: legacyTextValue()},
		{ID: "legacy_extra_columns", Description: "existing table with extra nullable and defaulted columns and an extra index", Steps: legacyExtraColumns()},
		{ID: "legacy_extra_not_null", Description: "existing table with an extra NOT NULL column without default", Steps: legacyExtraNotNull()},
		{ID: "legacy_no_primary_key", Description: "existing table without a primary key", Steps: legacyNoPrimaryKey()},
		{ID: "legacy_renamed_column", Description: "existing table whose key column has another name", Steps: legacyRenamedColumn()},
		{ID: "search_path_schema", Description: "table in another schema selected through search_path", Steps: searchPathSchema()},
		{ID: "concurrent_same_key", Description: "32 concurrent upserts of one key", Steps: concurrentSameKey()},
		{ID: "concurrent_distinct_keys", Description: "64 concurrent upserts of distinct keys, then removals", Steps: concurrentDistinct()},
		{ID: "pool_single_connection", Description: "pool_max_conns=1: operations queue; a queued operation honors its deadline", Steps: poolSingle()},
		{ID: "row_lock_blocks_set", Description: "an upsert waits for another transaction's row lock and then wins", Steps: rowLock()},
		{ID: "insert_conflict_waits", Description: "an upsert waits for a concurrent uncommitted insert of the same key", Steps: insertConflict()},
		{ID: "table_lock_deadline", Description: "every operation honors its deadline behind an ACCESS EXCLUSIVE lock and the pool recovers", Steps: tableLock()},
		{ID: "read_committed_visibility", Description: "uncommitted, committed and rolled-back writes by another session", Steps: visibility()},
		{ID: "open_race", Description: "concurrent Open on a fresh database", Steps: openRace()},
		{ID: "key_size_limits", Description: "long keys: indexable, and over the btree row limit", Steps: keyLimits()},
		{ID: "bulk_query", Description: "2000 entries: counts, digests and prefix slices", Steps: bulk()},
	}
}

var schemaProbes = []string{
	`SELECT column_name, data_type, is_nullable, coalesce(collation_name, ''), coalesce(column_default, '') FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'storage_entries' ORDER BY ordinal_position`,
	`SELECT conname, contype, pg_get_constraintdef(oid) FROM pg_constraint WHERE conrelid = 'public.storage_entries'::regclass ORDER BY conname`,
	`SELECT indexname, indexdef FROM pg_indexes WHERE schemaname = 'public' AND tablename = 'storage_entries' ORDER BY indexname`,
	`SELECT relkind::text, relpersistence::text FROM pg_class WHERE oid = 'public.storage_entries'::regclass`,
	`SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = 'public'`,
}

func schemaFresh() scenario {
	steps := []step{
		sqlOn("probe", `SELECT to_regclass('public.storage_entries') IS NOT NULL`),
		open(nil),
	}
	for _, q := range schemaProbes {
		steps = append(steps, sqlOn("probe", q))
	}
	steps = append(steps,
		setStr("k", "v"),
		open(&dsnSpec{Params: map[string]string{"application_name": "second"}}),
		openAs("c", nil),
		withOn(get("k"), "c"),
		sqlOn("probe", schemaProbes[4]),
	)
	return scenario{ID: "schema_fresh", Description: "DDL created by Open (columns, collation, constraints, indexes, object count) and idempotent reopen", Steps: steps}
}

func crudBasic() []step {
	all := make([]byte, 256)
	for i := range all {
		all[i] = byte(i)
	}
	return []step{
		open(nil),
		get("missing"),
		remove("missing"),
		query(""),
		setStr("config:port", "8787"),
		setStr("config:host", "127.0.0.1"),
		setStr("session:abc", `{"user":"admin"}`),
		get("config:port"),
		setStr("config:port", "9000"),
		get("config:port"),
		query("config:"),
		query(""),
		remove("session:abc"),
		remove("session:abc"),
		get("session:abc"),
		set("empty", b64("")),
		get("empty"),
		set("nilvalue", nil),
		get("nilvalue"),
		set("all-bytes", rawB64(all)),
		get("all-bytes"),
		set("nul-in-value", rawB64([]byte{'a', 0, 'b', 0})),
		get("nul-in-value"),
		setStr("ünïcode-ключ-日本語-😀", "unicode value"),
		get("ünïcode-ключ-日本語-😀"),
		setStr(" leading and trailing ", "spaces"),
		setStr("\ttab\nnewline", "control"),
		get("\ttab\nnewline"),
		query(""),
	}
}

func invalidKeys() []step {
	return []step{
		open(nil),
		setStr("", "x"),
		get(""),
		remove(""),
		query(""),
		setStr("a\x00b", "nul key"),
		get("a\x00b"),
		remove("a\x00b"),
		query("a\x00"),
		setStr("ab", "after errors"),
		query(""),
	}
}

func valueSemantics() []step {
	return []step{
		open(nil),
		set("nil", nil),
		set("empty", b64("")),
		get("nil"),
		get("empty"),
		set("big", &valueSpec{Fill: intp(0xAB), Len: 4 * 1024 * 1024}),
		get("big"),
		set("big", b64("small")),
		get("big"),
		set("boundary", &valueSpec{Fill: intp(0x61), Len: 4096}),
		set("boundary+1", &valueSpec{Fill: intp(0x62), Len: 4097}),
		query(""),
	}
}

func ordering() []step {
	keys := []string{"b", "B", "a", "A", "_", "-", "~", "Z", "z", "ä", "Ä", "e", "é", "É", "ß", "日", "日本", "😀", "😀a", "a b", "a-b", "a_b", "a.b", "a/b", "10", "9", "100", "", "ab", "aB", "Ab", "AB"}
	steps := []step{open(nil)}
	for i, k := range keys {
		if k == "" {
			continue
		}
		steps = append(steps, set(k, b64(fmt.Sprintf("v%d", i))))
	}
	steps = append(steps, query(""), query("a"), query("A"), query("ä"), query("日"), query("😀"), query("1"), query("a "), query("zzz"))
	return steps
}

func prefixes() []step {
	keys := []string{"%", "%%", "a%", "a%b", "_", "__", "a_", "a_b", "ab", "\\", "\\\\", "a\\", "a\\b", "日本", "日本語", "日", "", "abc"}
	steps := []step{open(nil)}
	for _, k := range keys {
		if k != "" {
			steps = append(steps, setStr(k, "x:"+k))
		}
	}
	for _, pre := range []string{"%", "a%", "_", "a_", "\\", "a\\", "日", "日本", "日本語語", "abcd", "abc", "", "ab", "zz"} {
		steps = append(steps, query(pre))
	}
	return steps
}

func randomMix() []step {
	rng := rand.New(rand.NewPCG(20260909, 15))
	alphabet := []string{"a", "ab", "abc", "a/b", "a:b", "B", "Ba", "ä", "abä", "Ä", "日本", "日", "😀", "😀a", "%", "_", "a%", "a_", "\\", "a\\b", "  ", "\t", "k\n", "K", "a b", "ab%", "10", "9"}
	prefixes := []string{"", "a", "ab", "B", "ä", "日", "😀", "%", "_", "\\", "a/", "a:", "1", "zz", " ", "k"}
	steps := []step{open(nil)}
	for range 500 {
		switch n := rng.IntN(100); {
		case n < 40:
			value := make([]byte, rng.IntN(17))
			for i := range value {
				value[i] = byte(rng.IntN(256))
			}
			steps = append(steps, set(alphabet[rng.IntN(len(alphabet))], rawB64(value)))
		case n < 60:
			steps = append(steps, get(alphabet[rng.IntN(len(alphabet))]))
		case n < 75:
			steps = append(steps, remove(alphabet[rng.IntN(len(alphabet))]))
		default:
			steps = append(steps, query(prefixes[rng.IntN(len(prefixes))]))
		}
	}
	return append(steps, query(""))
}

func reopenState() []step {
	return []step{
		open(nil),
		setStr("a", "1"), setStr("b", "2"), setStr("c", "3"),
		closeOn("b"),
		open(nil),
		query(""),
		openAs("other", nil),
		withOn(setStr("from-other", "x"), "other"),
		get("from-other"),
		withOn(remove("a"), "other"),
		query(""),
		withOn(query(""), "other"),
	}
}

func closeSemantics() []step {
	return []step{
		open(nil),
		setStr("k", "v"),
		closeOn("b"),
		closeOn("b"),
		get("k"),
		setStr("k", "v2"),
		remove("k"),
		query(""),
		open(nil),
		get("k"),
	}
}

func openErrors() []step {
	garbage := "this is not a dsn"
	badPort := "postgres://postgres@127.0.0.1:notaport/db"
	badEscape := "postgres://%zz@127.0.0.1/db"
	return []step{
		open(&dsnSpec{Raw: p("")}),
		open(&dsnSpec{Raw: &garbage}),
		open(&dsnSpec{Raw: &badPort}),
		open(&dsnSpec{Raw: &badEscape}),
		open(&dsnSpec{Password: "wrong"}),
		open(&dsnSpec{User: "symdesk_pg_nobody", Password: "wrong"}),
		open(&dsnSpec{Database: "missing"}),
		open(&dsnSpec{Host: "closed"}),
		open(&dsnSpec{Params: map[string]string{"sslmode": "require"}}),
		open(&dsnSpec{Params: map[string]string{"sslmode": "verify-full"}}),
		open(&dsnSpec{Form: "keyword"}),
		setStr("kw", "keyword form works"),
		get("kw"),
		closeOn("b"),
		open(&dsnSpec{Params: map[string]string{"application_name": "pggen", "connect_timeout": "5"}}),
		get("kw"),
	}
}

func rolePrivileges() []step {
	return []step{
		// A fresh database: a role that cannot create in public cannot Open.
		open(asRole("symdesk_pg_nocreate")),
		open(nil),
		setStr("seed", "value"),
		sqlOn("s", `GRANT CONNECT ON DATABASE "{db}" TO symdesk_pg_ro, symdesk_pg_nocreate`),
		sqlOn("s", `GRANT USAGE ON SCHEMA public TO symdesk_pg_ro, symdesk_pg_nocreate`),
		sqlOn("s", `GRANT SELECT ON storage_entries TO symdesk_pg_ro`),
		sqlOn("s", `GRANT SELECT, INSERT, UPDATE, DELETE ON storage_entries TO symdesk_pg_nocreate`),
		// Open runs CREATE TABLE IF NOT EXISTS, which needs CREATE on the
		// schema even when the table exists.
		openAs("ro", asRole("symdesk_pg_ro")),
		openAs("nc", asRole("symdesk_pg_nocreate")),
		sqlOn("s", `GRANT CREATE ON SCHEMA public TO symdesk_pg_ro, symdesk_pg_nocreate`),
		openAs("ro", asRole("symdesk_pg_ro")),
		withOn(get("seed"), "ro"),
		withOn(query(""), "ro"),
		withOn(setStr("seed", "changed"), "ro"),
		withOn(setStr("new", "x"), "ro"),
		withOn(remove("seed"), "ro"),
		get("seed"),
		openAs("nc", asRole("symdesk_pg_nocreate")),
		withOn(setStr("seed", "by-nocreate"), "nc"),
		withOn(remove("missing"), "nc"),
		get("seed"),
	}
}

func legacyTextValue() []step {
	return concat(
		legacyTable(`CREATE TABLE storage_entries (storage_key TEXT PRIMARY KEY NOT NULL, value TEXT NOT NULL)`,
			`INSERT INTO storage_entries VALUES ('legacy', 'hello'), ('Legacy2', 'wörld')`),
		[]step{
			open(nil),
			get("legacy"),
			query(""),
			setStr("fresh", "plain text"),
			set("nul", rawB64([]byte{'a', 0, 'b'})),
			set("invalid-utf8", rawB64([]byte{0xff, 0xfe, 0xfd})),
			set("legacy", b64("replaced")),
			get("legacy"),
			query(""),
		})
}

func legacyExtraColumns() []step {
	return concat(
		legacyTable(`CREATE TABLE storage_entries (storage_key TEXT PRIMARY KEY NOT NULL, value BYTEA NOT NULL, updated_at TIMESTAMPTZ, version INTEGER NOT NULL DEFAULT 7)`,
			`CREATE INDEX storage_entries_updated_idx ON storage_entries (updated_at)`,
			`INSERT INTO storage_entries (storage_key, value) VALUES ('old', '\x6f6c64')`),
		[]step{
			open(nil),
			get("old"),
			setStr("old", "newer"),
			setStr("added", "row"),
			remove("added"),
			query(""),
			sqlOn("probe", `SELECT storage_key, version::text, updated_at IS NULL FROM storage_entries ORDER BY storage_key COLLATE "C"`),
		})
}

func legacyExtraNotNull() []step {
	return concat(
		legacyTable(`CREATE TABLE storage_entries (storage_key TEXT PRIMARY KEY NOT NULL, value BYTEA NOT NULL, owner TEXT NOT NULL DEFAULT 'x', tenant TEXT NOT NULL)`,
			`INSERT INTO storage_entries (storage_key, value, tenant) VALUES ('old', '\x6f6c64', 't1')`),
		[]step{
			open(nil),
			get("old"),
			query(""),
			setStr("old", "update keeps tenant"),
			setStr("brand-new", "insert needs tenant"),
			remove("old"),
			query(""),
		})
}

func legacyNoPrimaryKey() []step {
	return concat(
		legacyTable(`CREATE TABLE storage_entries (storage_key TEXT NOT NULL, value BYTEA NOT NULL)`,
			`INSERT INTO storage_entries VALUES ('b', '\x62'), ('a', '\x61')`),
		[]step{
			open(nil),
			get("a"),
			query(""),
			setStr("a", "upsert needs a unique constraint"),
			setStr("c", "also fails"),
			remove("a"),
			query(""),
		})
}

func legacyRenamedColumn() []step {
	return concat(
		legacyTable(`CREATE TABLE storage_entries ("key" TEXT PRIMARY KEY NOT NULL, value BYTEA NOT NULL)`,
			`INSERT INTO storage_entries VALUES ('a', '\x61')`),
		[]step{
			open(nil),
			get("a"),
			setStr("a", "x"),
			remove("a"),
			query(""),
			query("a"),
		})
}

func searchPathSchema() []step {
	return concat(
		legacyTable(`CREATE SCHEMA legacy`),
		[]step{
			sqlOn("setup", `CREATE TABLE legacy.storage_entries (storage_key TEXT PRIMARY KEY NOT NULL, value BYTEA NOT NULL)`),
			sqlOn("setup", `INSERT INTO legacy.storage_entries VALUES ('in-legacy', '\x6c')`),
			openAs("default", nil),
			withOn(get("in-legacy"), "default"),
			withOn(setStr("in-public", "p"), "default"),
			openAs("legacy", params("options", "-c search_path=legacy")),
			withOn(get("in-legacy"), "legacy"),
			withOn(get("in-public"), "legacy"),
			withOn(setStr("also-legacy", "l2"), "legacy"),
			withOn(query(""), "legacy"),
			withOn(query(""), "default"),
			sqlOn("setup", `SELECT (SELECT count(*) FROM legacy.storage_entries)::text, (SELECT count(*) FROM public.storage_entries)::text`),
		})
}

func concurrentSameKey() []step {
	steps := []step{open(nil)}
	values := make([]string, 32)
	for i := range 32 {
		v := fmt.Sprintf("value-%02d", i)
		values[i] = base64.StdEncoding.EncodeToString([]byte(v))
		steps = append(steps, spawn(fmt.Sprintf("t%02d", i), setStr("hot", v)))
	}
	for i := range 32 {
		steps = append(steps, join(fmt.Sprintf("t%02d", i)))
	}
	one := step{Op: "get_one_of", Key: p("hot"), Values: values}
	// The surviving value is whichever upsert committed last; settle it so the
	// recorded table state is deterministic.
	return append(steps, one, setStr("hot", "settled"), query(""))
}

func concurrentDistinct() []step {
	steps := []step{open(nil)}
	for i := range 64 {
		steps = append(steps, spawn(fmt.Sprintf("s%02d", i), setStr(fmt.Sprintf("key-%02d", i), fmt.Sprintf("v%d", i))))
	}
	for i := range 64 {
		steps = append(steps, join(fmt.Sprintf("s%02d", i)))
	}
	steps = append(steps, step{Op: "query_digest", Prefix: p("")})
	for i := 0; i < 64; i += 2 {
		steps = append(steps, spawn(fmt.Sprintf("r%02d", i), remove(fmt.Sprintf("key-%02d", i))))
	}
	for i := 0; i < 64; i += 2 {
		steps = append(steps, join(fmt.Sprintf("r%02d", i)))
	}
	return append(steps, step{Op: "query_digest", Prefix: p("")}, query("key-0"))
}

func poolSingle() []step {
	steps := []step{open(params("pool_max_conns", "1"))}
	for i := range 16 {
		steps = append(steps, spawn(fmt.Sprintf("q%02d", i), setStr(fmt.Sprintf("k%02d", i), fmt.Sprintf("v%d", i))))
	}
	for i := range 16 {
		steps = append(steps, join(fmt.Sprintf("q%02d", i)))
	}
	steps = append(steps,
		step{Op: "query_digest", Prefix: p("")},
		// Occupy the only connection with an upsert blocked on a row lock.
		sqlOn("s1", `BEGIN`),
		sqlOn("s1", `UPDATE storage_entries SET value = '\x00' WHERE storage_key = 'k00'`),
		spawn("holder", setStr("k00", "holder")),
		sleep(300),
		pending("holder"),
		// The queued read cannot get a connection before its deadline.
		withTimeout(get("k01"), 250),
		sqlOn("s1", `COMMIT`),
		join("holder"),
		get("k00"),
		get("k01"),
	)
	return steps
}

func rowLock() []step {
	return []step{
		open(nil),
		setStr("k", "initial"),
		sqlOn("s1", `BEGIN`),
		sqlOn("s1", `UPDATE storage_entries SET value = '\x73' WHERE storage_key = 'k'`),
		spawn("w", setStr("k", "writer")),
		sleep(300),
		pending("w"),
		get("k"),
		sqlOn("s1", `COMMIT`),
		join("w"),
		get("k"),
		sqlOn("s2", `BEGIN`),
		sqlOn("s2", `UPDATE storage_entries SET value = '\x74' WHERE storage_key = 'k'`),
		spawn("rm", remove("k")),
		sleep(300),
		pending("rm"),
		sqlOn("s2", `ROLLBACK`),
		join("rm"),
		get("k"),
	}
}

func insertConflict() []step {
	return []step{
		open(nil),
		sqlOn("s1", `BEGIN`),
		sqlOn("s1", `INSERT INTO storage_entries VALUES ('race', '\x31')`),
		spawn("up", setStr("race", "backend")),
		sleep(300),
		pending("up"),
		query(""),
		sqlOn("s1", `COMMIT`),
		join("up"),
		get("race"),
		sqlOn("s2", `BEGIN`),
		sqlOn("s2", `INSERT INTO storage_entries VALUES ('race2', '\x32')`),
		spawn("up2", setStr("race2", "after-rollback")),
		sleep(300),
		pending("up2"),
		sqlOn("s2", `ROLLBACK`),
		join("up2"),
		get("race2"),
		query(""),
	}
}

func tableLock() []step {
	return []step{
		open(nil),
		setStr("a", "1"),
		sqlOn("s1", `BEGIN`),
		sqlOn("s1", `LOCK TABLE storage_entries IN ACCESS EXCLUSIVE MODE`),
		withTimeout(get("a"), 250),
		withTimeout(setStr("a", "2"), 250),
		withTimeout(remove("a"), 250),
		withTimeout(query(""), 250),
		sqlOn("s1", `ROLLBACK`),
		get("a"),
		setStr("a", "3"),
		query(""),
		sqlOn("s1", `BEGIN`),
		sqlOn("s1", `LOCK TABLE storage_entries IN ROW EXCLUSIVE MODE`),
		withTimeout(get("a"), 250),
		withTimeout(setStr("a", "4"), 250),
		sqlOn("s1", `ROLLBACK`),
		get("a"),
	}
}

func visibility() []step {
	return []step{
		open(nil),
		setStr("committed-before", "yes"),
		sqlOn("s1", `BEGIN`),
		sqlOn("s1", `INSERT INTO storage_entries VALUES ('uncommitted', '\x75')`),
		sqlOn("s1", `DELETE FROM storage_entries WHERE storage_key = 'committed-before'`),
		get("uncommitted"),
		get("committed-before"),
		query(""),
		sqlOn("s1", `ROLLBACK`),
		get("uncommitted"),
		query(""),
		sqlOn("s2", `BEGIN`),
		sqlOn("s2", `INSERT INTO storage_entries VALUES ('now-committed', '\x63')`),
		sqlOn("s2", `COMMIT`),
		get("now-committed"),
		query(""),
	}
}

func openRace() []step {
	return []step{
		{Op: "open_many", Count: 8},
		sqlOn("probe", `SELECT count(*)::text FROM pg_tables WHERE schemaname = 'public' AND tablename = 'storage_entries'`),
		{Op: "open_many", Count: 8},
		open(nil),
		setStr("k", "v"),
		get("k"),
	}
}

func keyLimits() []step {
	stream := func(n int) string {
		var sb strings.Builder
		h := sha256.Sum256([]byte("seed"))
		for sb.Len() < n {
			sb.WriteString(hex.EncodeToString(h[:]))
			h = sha256.Sum256(h[:])
		}
		return sb.String()[:n]
	}
	return []step{
		open(nil),
		setStr(strings.Repeat("k", 2000), "compressible 2000"),
		get(strings.Repeat("k", 2000)),
		setStr(strings.Repeat("k", 20000), "compressible 20000"),
		get(strings.Repeat("k", 20000)),
		remove(strings.Repeat("k", 20000)),
		setStr(stream(2000), "incompressible 2000"),
		get(stream(2000)),
		setStr(stream(3000), "incompressible 3000"),
		get(stream(3000)),
		remove(stream(3000)),
		step{Op: "query_digest", Prefix: p("")},
	}
}

func bulk() []step {
	return []step{
		open(nil),
		{Op: "bulk_set", Prefix: p("bulk/"), Count: 2000},
		{Op: "query_digest", Prefix: p("")},
		{Op: "query_digest", Prefix: p("bulk/1")},
		{Op: "query_digest", Prefix: p("bulk/19")},
		{Op: "query_digest", Prefix: p("nomatch")},
		get("bulk/01999"),
		get("bulk/02000"),
		remove("bulk/00000"),
		{Op: "query_digest", Prefix: p("")},
	}
}
