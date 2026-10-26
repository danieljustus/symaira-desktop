// Command permissionsgen freezes the Go permissions manager contract
// (internal/permissions) as a Rust replay fixture. Each scenario seeds a
// disposable config directory, executes a sequence of Manager operations
// against the production package and records every result together with the
// final file bytes, modes and file set.
//
// Plain-text tokens are random, so they are never recorded. An operation that
// mints a token carries a "pin" label: the generator checks that the minted
// token authenticates the user (recorded as "authenticates") and then pins the
// stored hash to HashToken(pin) via SetTokenHash, which keeps users.json
// deterministic without hiding the mint behavior.
//
// Paths in error text are recorded relative to the scenario root as <root>.
package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/danieljustus/symaira-desktop/internal/permissions"
	"github.com/danieljustus/symaira-desktop/scripts/rust-port/fixtureoracle"
	"github.com/danieljustus/symaira-desktop/scripts/rust-port/inventory"
)

const defaultOutput = "testdata/port/vault/permissions.json"

var tokenShape = regexp.MustCompile(`^[0-9a-f]{64}$`)

type document struct {
	SchemaVersion int              `json:"schema_version"`
	Oracle        inventory.Oracle `json:"oracle"`
	Scenarios     []scenario       `json:"scenarios"`
}

type scenario struct {
	ID string `json:"id"`
	// UnixOnly marks scenarios whose recorded error text or modes depend on
	// POSIX path syntax and permission bits.
	UnixOnly bool             `json:"unix_only,omitempty"`
	Setup    setup            `json:"setup"`
	NewError string           `json:"new_error,omitempty"`
	Ops      []map[string]any `json:"ops"`
	Final    final            `json:"final"`
}

type setup struct {
	ConfigDir string `json:"config_dir"`
	// Files holds UTF-8 text; FilesB64 holds raw bytes (invalid UTF-8).
	Files     map[string]string `json:"files,omitempty"`
	FilesB64  map[string]string `json:"files_b64,omitempty"`
	FileModes map[string]int    `json:"file_modes,omitempty"`
	// DirModes pre-creates the directory with this chmod before NewManager.
	DirModes map[string]int `json:"dir_modes,omitempty"`
	// LockConfigDir chmods the config directory after NewManager.
	LockConfigDir int `json:"lock_config_dir,omitempty"`
}

type final struct {
	ConfigDirMode int             `json:"config_dir_mode,omitempty"`
	Files         map[string]file `json:"files"`
}

type file struct {
	Content string `json:"content"`
	Mode    int    `json:"mode"`
}

func main() {
	output := flag.String("output", defaultOutput, "fixture path")
	check := flag.Bool("check", false, "fail if fixture differs")
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
	identityRoot, identityErr := fixtureoracle.FindRepositoryRoot(".")
	if identityErr != nil {
		fatal("resolve fixture source: %v", identityErr)
	}
	if identityErr := fixtureoracle.ValidateSource(identityRoot, inventory.Oracle{Commit: *commit, Release: *release}); identityErr != nil {
		fatal("verify selected fixture source: %v", identityErr)
	}

	doc := document{SchemaVersion: 1, Oracle: inventory.Oracle{Commit: *commit, Release: *release}}
	for _, spec := range scenarioSpecs() {
		doc.Scenarios = append(doc.Scenarios, run(spec))
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
			fatal("permissions fixture drift; regenerate deliberately")
		}
		fmt.Println("PASS permissions fixture")
		return
	}
	if err := os.MkdirAll(filepath.Dir(*output), 0o750); err != nil {
		fatal("create fixture directory: %v", err)
	}
	if err := os.WriteFile(*output, content, 0o600); err != nil {
		fatal("write fixture: %v", err)
	}
	ops := 0
	for _, s := range doc.Scenarios {
		ops += len(s.Ops)
	}
	fmt.Printf("PASS permissions fixture generated (%d scenarios, %d operations)\n", len(doc.Scenarios), ops)
}

func run(spec scenario) scenario {
	root, err := os.MkdirTemp("", "symdesk-permissions-")
	if err != nil {
		fatal("temp root: %v", err)
	}
	defer func() {
		// Scenarios may leave directories unreadable; restore the search bit
		// through a root-scoped handle so cleanup cannot follow a swapped
		// path outside the scenario root.
		if scoped, openErr := os.OpenRoot(root); openErr == nil {
			_ = fs.WalkDir(scoped.FS(), ".", func(path string, d fs.DirEntry, err error) error {
				if err == nil && d.IsDir() {
					_ = scoped.Chmod(path, 0o700)
				}
				return nil
			})
			_ = scoped.Close()
		}
		_ = os.RemoveAll(root)
	}()
	norm := func(s string) string { return strings.ReplaceAll(s, root, "<root>") }

	for rel, mode := range spec.Setup.DirModes {
		path := filepath.Join(root, filepath.FromSlash(rel))
		must(os.MkdirAll(path, 0o700))
		must(os.Chmod(path, permMode(mode)))
	}
	writeSetup := func(rel string, data []byte) {
		path := filepath.Join(root, filepath.FromSlash(rel))
		must(os.MkdirAll(filepath.Dir(path), 0o700))
		must(os.WriteFile(path, data, 0o600))
		if mode, ok := spec.Setup.FileModes[rel]; ok {
			must(os.Chmod(path, permMode(mode)))
		}
	}
	for rel, text := range spec.Setup.Files {
		writeSetup(rel, []byte(text))
	}
	for rel, b64 := range spec.Setup.FilesB64 {
		raw, err := base64.StdEncoding.DecodeString(b64)
		must(err)
		writeSetup(rel, raw)
	}
	for rel, mode := range spec.Setup.DirModes {
		// DirModes applied before files; re-apply in case a file write
		// needed the directory writable.
		must(os.Chmod(filepath.Join(root, filepath.FromSlash(rel)), permMode(mode)))
	}

	configDir := filepath.Join(root, filepath.FromSlash(spec.Setup.ConfigDir))
	out := spec
	out.Ops = nil
	manager, err := permissions.NewManager(configDir)
	if err != nil {
		out.NewError = norm(err.Error())
		out.Final = snapshot(root, configDir, false)
		return out
	}
	if spec.Setup.LockConfigDir != 0 {
		must(os.Chmod(configDir, permMode(spec.Setup.LockConfigDir)))
	}
	for _, op := range spec.Ops {
		recorded := map[string]any{}
		for k, v := range op {
			recorded[k] = v
		}
		recorded["result"] = execute(manager, op, norm)
		out.Ops = append(out.Ops, recorded)
	}
	out.Final = snapshot(root, configDir, true)
	return out
}

func snapshot(root, configDir string, created bool) final {
	f := final{Files: map[string]file{}}
	if info, err := os.Stat(configDir); err == nil && created {
		f.ConfigDirMode = int(info.Mode().Perm())
	}
	// Walk and read through a root-scoped handle: every path stays confined
	// to the disposable scenario root, even if a scenario plants a symlink.
	scoped, openErr := os.OpenRoot(root)
	must(openErr)
	defer func() { _ = scoped.Close() }()
	_ = fs.WalkDir(scoped.FS(), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return nil
		}
		// Record the mode first, then make the file readable for the snapshot.
		_ = scoped.Chmod(path, 0o600)
		data, readErr := fs.ReadFile(scoped.FS(), path)
		must(readErr)
		f.Files[path] = file{Content: string(data), Mode: int(info.Mode().Perm())}
		return nil
	})
	return f
}

func execute(m *permissions.Manager, op map[string]any, norm func(string) string) map[string]any {
	result := map[string]any{}
	errOut := func(err error) {
		if err != nil {
			result["error"] = norm(err.Error())
		}
	}
	str := func(key string) string {
		s, _ := op[key].(string)
		return s
	}
	list := func(key string) []string {
		raw, ok := op[key].([]any)
		if !ok {
			return nil
		}
		out := make([]string, len(raw))
		for i, v := range raw {
			out[i], _ = v.(string)
		}
		return out
	}
	actor := func() *permissions.User {
		raw, ok := op["as"].(map[string]any)
		if !ok {
			return nil
		}
		u := &permissions.User{}
		u.Name, _ = raw["name"].(string)
		u.TokenHash, _ = raw["token_hash"].(string)
		if roles, ok := raw["roles"].([]any); ok {
			for _, r := range roles {
				s, _ := r.(string)
				u.Roles = append(u.Roles, s)
			}
		}
		return u
	}
	pin := func(name, token string) {
		if token == "" {
			result["token"] = "none"
			return
		}
		result["token"] = "generated"
		result["token_shape_ok"] = tokenShape.MatchString(token)
		if u, err := m.Authenticate(token); err == nil && u.Name == name {
			result["authenticates"] = true
		} else {
			result["authenticates"] = false
		}
		if label := str("pin"); label != "" {
			must(m.SetTokenHash(name, permissions.HashToken(label)))
		}
	}

	switch str("op") {
	case "authenticate":
		user, err := m.Authenticate(str("token"))
		errOut(err)
		if user != nil {
			result["user"] = user
		}
	case "can_read":
		result["allowed"] = m.CanRead(actor(), str("path"))
	case "can_write":
		result["allowed"] = m.CanWrite(actor(), str("path"))
	case "can_read_many":
		result["paths"] = m.CanReadMany(actor(), list("paths"))
	case "user_add":
		roles := list("roles")
		token, err := m.UserAdd(str("name"), roles...)
		errOut(err)
		if err == nil {
			pin(str("name"), token)
		}
	case "user_list":
		users, err := m.UserList()
		errOut(err)
		result["users"] = users
	case "user_remove":
		errOut(m.UserRemove(str("name")))
	case "user_generate_token":
		token, err := m.UserGenerateToken(str("name"))
		errOut(err)
		if err == nil {
			pin(str("name"), token)
		}
	case "set_token_hash":
		errOut(m.SetTokenHash(str("name"), str("hash")))
	case "group_add":
		errOut(m.GroupAdd(str("name")))
	case "group_list":
		groups, err := m.GroupList()
		errOut(err)
		result["groups"] = groups
	case "group_remove":
		errOut(m.GroupRemove(str("name")))
	case "group_add_member":
		errOut(m.GroupAddMember(str("group"), str("user")))
	case "group_remove_member":
		errOut(m.GroupRemoveMember(str("group"), str("user")))
	case "set_document_rule":
		raw, _ := op["rule"].(map[string]any)
		rule := permissions.DocumentRule{}
		rule.Path, _ = raw["path"].(string)
		rule.Owner, _ = raw["owner"].(string)
		toList := func(key string) []string {
			arr, ok := raw[key].([]any)
			if !ok {
				return nil
			}
			out := make([]string, len(arr))
			for i, v := range arr {
				out[i], _ = v.(string)
			}
			return out
		}
		rule.ReadUsers, rule.ReadGroups = toList("read_users"), toList("read_groups")
		rule.WriteUsers, rule.WriteGroups = toList("write_users"), toList("write_groups")
		errOut(m.SetDocumentRule(rule))
	case "get_document_rule":
		rule, err := m.GetDocumentRule(str("path"))
		errOut(err)
		if rule != nil {
			result["rule"] = rule
		}
	default:
		fatal("unknown op %q", str("op"))
	}
	return result
}

// permMode converts a recorded permission value to a file mode, rejecting
// anything outside the permission bits instead of silently truncating it.
func permMode(mode int) os.FileMode {
	if mode < 0 || mode > 0o777 {
		fatal("permission bits out of range: %#o", mode)
	}
	return os.FileMode(mode & 0o777)
}

// putPairs stores alternating string-key/value pairs, refusing a dangling key.
func putPairs(m map[string]any, kv []any) {
	if len(kv)%2 != 0 {
		fatal("odd key/value argument count: %d", len(kv))
	}
	for i := 0; i+1 < len(kv); i += 2 {
		key, ok := kv[i].(string)
		if !ok {
			fatal("non-string key %v", kv[i])
		}
		m[key] = kv[i+1]
	}
}

func must(err error) {
	if err != nil {
		fatal("%v", err)
	}
}

// ---- scenario corpus ---------------------------------------------------

type ops []map[string]any

func o(op string, kv ...any) map[string]any {
	m := map[string]any{"op": op}
	putPairs(m, kv)
	return m
}

func actorOf(name string, roles ...string) map[string]any {
	list := make([]any, len(roles))
	for i, r := range roles {
		list[i] = r
	}
	return map[string]any{"name": name, "roles": list}
}

func strs(v ...string) []any {
	out := make([]any, len(v))
	for i, s := range v {
		out[i] = s
	}
	return out
}

func hashOf(token string) string { return permissions.HashToken(token) }

func sc(id string, files map[string]string, list ops) scenario {
	s := scenario{ID: id, Setup: setup{ConfigDir: "cfg"}, Ops: list}
	if len(files) > 0 {
		s.Setup.Files = map[string]string{}
		for name, content := range files {
			s.Setup.Files["cfg/"+name] = content
		}
	}
	return s
}

func usersJSON(pairs ...string) string {
	// pairs: name, token, role...; separated by "|"
	var b strings.Builder
	b.WriteString("[")
	first := true
	for _, entry := range pairs {
		parts := strings.Split(entry, "|")
		if !first {
			b.WriteString(",")
		}
		first = false
		roles := make([]string, 0, len(parts)-2)
		for _, r := range parts[2:] {
			roles = append(roles, fmt.Sprintf("%q", r))
		}
		fmt.Fprintf(&b, `{"name":%q,"token_hash":%q,"roles":[%s]}`, parts[0], hashOf(parts[1]), strings.Join(roles, ","))
	}
	b.WriteString("]")
	return b.String()
}

func scenarioSpecs() []scenario {
	var all []scenario
	add := func(s scenario) { all = append(all, s) }
	unix := func(s scenario) scenario { s.UnixOnly = true; return s }

	// --- construction ---------------------------------------------------
	add(sc("fresh_manager_reads_empty", nil, ops{
		o("user_list"), o("group_list"), o("get_document_rule", "path", "a.md"),
		o("authenticate", "token", "anything"),
		o("can_read", "as", actorOf("u", "user"), "path", "a.md"),
		o("can_write", "as", actorOf("u", "user"), "path", "a.md"),
	}))
	add(unix(scenario{ID: "new_manager_creates_missing_dir_0700", Setup: setup{ConfigDir: "cfg"}, Ops: ops{o("user_list")}}))
	add(unix(scenario{ID: "new_manager_creates_nested_dirs_0700", Setup: setup{ConfigDir: "a/b/c/cfg"}, Ops: ops{o("group_add", "name", "g")}}))
	add(unix(scenario{ID: "new_manager_keeps_existing_dir_mode", Setup: setup{ConfigDir: "cfg", DirModes: map[string]int{"cfg": 0o755}}, Ops: ops{o("group_add", "name", "g")}}))
	add(unix(scenario{ID: "new_manager_config_dir_is_file", Setup: setup{ConfigDir: "cfg", Files: map[string]string{"cfg": "plain"}}}))
	add(unix(scenario{ID: "new_manager_parent_is_file", Setup: setup{ConfigDir: "blocker/cfg", Files: map[string]string{"blocker": "plain"}}}))

	// --- authentication -------------------------------------------------
	authUsers := usersJSON("admin|admin-token|admin", "ann|ann-token|user", "wk|worker-token|worker", "multi|multi-token|user|worker")
	add(sc("authenticate_matrix", map[string]string{"users.json": authUsers}, ops{
		o("authenticate", "token", "admin-token"),
		o("authenticate", "token", "ann-token"),
		o("authenticate", "token", "worker-token"),
		o("authenticate", "token", "multi-token"),
		o("authenticate", "token", ""),
		o("authenticate", "token", "wrong"),
		o("authenticate", "token", "ADMIN-TOKEN"),
		o("authenticate", "token", " admin-token"),
		o("authenticate", "token", "admin-token\n"),
		o("authenticate", "token", hashOf("admin-token")),
	}))
	add(sc("authenticate_hash_comparison_exact", map[string]string{"users.json": fmt.Sprintf(`[{"name":"upper","token_hash":%q,"roles":["user"]},{"name":"short","token_hash":"abc","roles":["user"]},{"name":"empty","token_hash":"","roles":["user"]},{"name":"first","token_hash":%q,"roles":["admin"]},{"name":"second","token_hash":%q,"roles":["user"]}]`,
		strings.ToUpper(hashOf("tok")), hashOf("dup"), hashOf("dup"))}, ops{
		o("authenticate", "token", "tok"),
		o("authenticate", "token", "dup"),
		o("authenticate", "token", "abc"),
	}))
	add(sc("authenticate_missing_roles_field", map[string]string{"users.json": fmt.Sprintf(`[{"name":"bare","token_hash":%q},{"name":"nullroles","token_hash":%q,"roles":null}]`, hashOf("t1"), hashOf("t2"))}, ops{
		o("authenticate", "token", "t1"),
		o("authenticate", "token", "t2"),
		o("can_write", "as", actorOf("bare"), "path", "x.md"),
	}))

	// --- access checks --------------------------------------------------
	rules := `[
  {"path":"private.md","owner":"ann","read_users":["bob"],"write_users":["carl"],"read_groups":["readers"],"write_groups":["writers"]},
  {"path":"open-owner.md","owner":"ann"},
  {"path":"*","owner":"root","read_users":["star"],"write_groups":["writers"]}
]`
	groups := `[{"name":"readers","members":["dora"]},{"name":"writers","members":["erin","dora"]},{"name":"empty","members":[]},{"name":"nullmembers"}]`
	access := ops{}
	for _, who := range []string{"ann", "bob", "carl", "dora", "erin", "star", "root", "nobody"} {
		for _, path := range []string{"private.md", "open-owner.md", "other.md", "*"} {
			access = append(access, o("can_read", "as", actorOf(who, "user"), "path", path))
			access = append(access, o("can_write", "as", actorOf(who, "user"), "path", path))
		}
	}
	access = append(access,
		o("can_read", "as", actorOf("nobody", "admin"), "path", "private.md"),
		o("can_write", "as", actorOf("nobody", "admin"), "path", "private.md"),
		o("can_read", "as", actorOf("nobody", "worker", "admin"), "path", "private.md"),
		o("can_read", "as", actorOf("nobody", "Admin"), "path", "private.md"),
		o("can_read", "as", nil, "path", "private.md"),
		o("can_write", "as", nil, "path", "other.md"),
		o("can_read", "as", actorOf("", "user"), "path", "open-owner.md"),
	)
	add(sc("access_matrix_exact_then_wildcard", map[string]string{"permissions.json": rules, "groups.json": groups}, access))
	add(sc("access_without_wildcard_is_public", map[string]string{"permissions.json": `[{"path":"only.md","owner":"ann"}]`}, ops{
		o("can_read", "as", actorOf("bob", "user"), "path", "other.md"),
		o("can_write", "as", actorOf("bob", "user"), "path", "other.md"),
		o("can_read", "as", actorOf("bob", "user"), "path", "only.md"),
		o("can_write", "as", actorOf("bob", "user"), "path", "only.md"),
		o("can_read", "as", actorOf("bob", "user"), "path", "ONLY.md"),
		o("can_read", "as", actorOf("bob", "user"), "path", "./only.md"),
		o("can_read", "as", actorOf("bob", "user"), "path", ""),
	}))
	add(sc("access_unknown_group_and_empty_members", map[string]string{
		"permissions.json": `[{"path":"p.md","owner":"ann","read_groups":["ghost","empty","nullmembers"],"write_groups":["ghost"]}]`,
		"groups.json":      groups,
	}, ops{
		o("can_read", "as", actorOf("dora", "user"), "path", "p.md"),
		o("can_write", "as", actorOf("dora", "user"), "path", "p.md"),
	}))
	add(sc("access_duplicate_groups_use_first", map[string]string{
		"permissions.json": `[{"path":"p.md","owner":"ann","read_groups":["g"]}]`,
		"groups.json":      `[{"name":"g","members":["first"]},{"name":"g","members":["second"]}]`,
	}, ops{
		o("can_read", "as", actorOf("first", "user"), "path", "p.md"),
		o("can_read", "as", actorOf("second", "user"), "path", "p.md"),
	}))
	add(sc("access_duplicate_rule_paths_use_first", map[string]string{
		"permissions.json": `[{"path":"p.md","owner":"ann"},{"path":"p.md","owner":"bob"},{"path":"*","owner":"zed"}]`,
	}, ops{
		o("can_read", "as", actorOf("ann", "user"), "path", "p.md"),
		o("can_read", "as", actorOf("bob", "user"), "path", "p.md"),
		o("can_read", "as", actorOf("zed", "user"), "path", "q.md"),
		o("get_document_rule", "path", "p.md"),
		o("get_document_rule", "path", "q.md"),
		o("get_document_rule", "path", "*"),
	}))

	// --- CanReadMany ----------------------------------------------------
	paths := strs("private.md", "open-owner.md", "other.md", "private.md", "*", "")
	add(sc("read_many_filters", map[string]string{"permissions.json": rules, "groups.json": groups}, ops{
		o("can_read_many", "as", actorOf("ann", "user"), "paths", paths),
		o("can_read_many", "as", actorOf("bob", "user"), "paths", paths),
		o("can_read_many", "as", actorOf("dora", "user"), "paths", paths),
		o("can_read_many", "as", actorOf("star", "user"), "paths", paths),
		o("can_read_many", "as", actorOf("nobody", "user"), "paths", paths),
		o("can_read_many", "as", actorOf("nobody", "admin"), "paths", paths),
		o("can_read_many", "as", nil, "paths", paths),
		o("can_read_many", "as", actorOf("nobody", "user"), "paths", strs()),
		o("can_read_many", "as", actorOf("nobody", "admin"), "paths", strs()),
		o("can_read_many", "as", nil, "paths", strs()),
		o("can_read_many", "as", actorOf("nobody", "user")),
		o("can_read_many", "as", actorOf("nobody", "admin")),
	}))

	// --- malformed and unusual inputs ----------------------------------
	for _, c := range []struct{ id, content string }{
		{"truncated", `[{"path":"p.md","owner":"ann"`},
		{"garbage", `not json`},
		{"object_root", `{"path":"p.md"}`},
		{"string_root", `"text"`},
		{"number_root", `12`},
		{"bool_root", `true`},
		{"null_root", `null`},
		{"whitespace_only", " \n\t "},
		{"empty_array", `[]`},
		{"bom_prefix", "\ufeff[]"},
		{"trailing_garbage", `[] x`},
		{"trailing_comma", `[{"path":"p.md","owner":"a"},]`},
		{"owner_number", `[{"path":"p.md","owner":5}]`},
		{"path_object", `[{"path":{"a":1},"owner":"a"}]`},
		{"read_users_string", `[{"path":"p.md","owner":"a","read_users":"bob"}]`},
		{"read_users_number_element", `[{"path":"p.md","owner":"a","read_users":["bob",1]}]`},
		{"read_users_null_element", `[{"path":"p.md","owner":"a","read_users":[null,"bob"]}]`},
		{"element_not_object", `[1]`},
		{"element_null", `[null]`},
		{"element_array", `[[]]`},
		{"two_type_errors_first_wins", `[{"path":1,"owner":2}]`},
		{"type_error_then_syntax_error", `[{"path":1,"owner":"a"} x]`},
		{"unknown_fields_ignored", `[{"path":"p.md","owner":"ann","extra":{"deep":[1,2,3]},"note":null}]`},
		{"keys_case_insensitive", `[{"PATH":"p.md","Owner":"ann","READ_USERS":["bob"]}]`},
		{"duplicate_keys_last_wins", `[{"path":"a.md","path":"p.md","owner":"x","owner":"ann"}]`},
		{"exact_and_folded_keys", `[{"Path":"first.md","path":"p.md","owner":"ann"}]`},
		{"folded_after_exact", `[{"path":"p.md","PATH":"q.md","owner":"ann"}]`},
		{"duplicate_list_keys_reuse_backing_array", `[{"path":"p.md","owner":"a","read_users":["a","b","c"],"read_users":["x"],"read_users":[null,null,null],"write_users":["w1","w2"],"write_users":null,"write_users":[null,"z"]}]`},
		{"duplicate_list_keys_empty_resets_backing", `[{"path":"p.md","owner":"a","read_users":["a","b"],"read_users":[],"read_users":[null,null]}]`},
		{"duplicate_string_key_null_keeps_value", `[{"path":"p.md","owner":"a","owner":null}]`},
		{"unicode_folded_keys", `[{"p\u0061th":"p.md","owner":"ann","read_u\u017fers":["bob"],"WRITE_USERS":["carl"]}]`},
		{"null_scalar_keeps_zero", `[{"path":"p.md","owner":null}]`},
		{"escapes_and_unicode", `[{"path":"p\u00e4\n\"q\".md","owner":"\ud83d\ude00"}]`},
		{"lone_surrogates", `[{"path":"a\ud800b","owner":"\udc00"}]`},
		{"huge_number_ignored_field", `[{"path":"p.md","owner":"a","n":1e999999}]`},
		{"deep_nesting_in_unknown_field", `[{"path":"p.md","owner":"a","x":[[[[[[[[[[1]]]]]]]]]]}]`},
		{"control_char_in_string", "[{\"path\":\"a\x01\",\"owner\":\"b\"}]"},
		{"bad_escape", `[{"path":"\q","owner":"b"}]`},
		{"bad_unicode_escape", `[{"path":"\u12G4","owner":"b"}]`},
		{"missing_colon", `[{"path" "p.md"}]`},
		{"literal_typo", `[tru]`},
		{"number_leading_zero", `[01]`},
		{"single_quotes", `[{'path':'p.md'}]`},
	} {
		add(sc("permissions_file_"+c.id, map[string]string{"permissions.json": c.content}, ops{
			o("get_document_rule", "path", "p.md"),
			o("can_read", "as", actorOf("bob", "user"), "path", "p.md"),
			o("can_write", "as", actorOf("bob", "user"), "path", "p.md"),
			o("can_read", "as", actorOf("ann", "user"), "path", "p.md"),
			o("can_read_many", "as", actorOf("bob", "user"), "paths", strs("p.md", "q.md")),
			o("can_read_many", "as", actorOf("x", "admin"), "paths", strs("p.md")),
		}))
	}
	for _, c := range []struct{ id, content string }{
		{"truncated", `[{"name":"g"`},
		{"object_root", `{}`},
		{"members_number", `[{"name":"g","members":1}]`},
		{"members_null_elements", `[{"name":"g","members":[null,"bob"]}]`},
		{"null_root", `null`},
		{"whitespace_only", "\n"},
		{"keys_case_insensitive", `[{"NAME":"g","Members":["bob"]}]`},
	} {
		add(sc("groups_file_"+c.id, map[string]string{
			"groups.json":      c.content,
			"permissions.json": `[{"path":"p.md","owner":"ann","read_groups":["g"]}]`,
		}, ops{
			o("group_list"),
			o("can_read", "as", actorOf("bob", "user"), "path", "p.md"),
			o("can_read_many", "as", actorOf("bob", "user"), "paths", strs("p.md", "q.md")),
			o("group_add", "name", "h"),
			o("group_add_member", "group", "g", "user", "bob"),
			o("group_remove", "name", "g"),
			o("group_remove_member", "group", "g", "user", "bob"),
		}))
	}
	for _, c := range []struct{ id, content string }{
		{"truncated", `[{"name":"u"`},
		{"object_root", `{}`},
		{"name_number", `[{"name":1}]`},
		{"roles_string", `[{"name":"u","roles":"admin"}]`},
		{"null_root", `null`},
		{"whitespace_only", " "},
		{"keys_case_insensitive", fmt.Sprintf(`[{"NAME":"u","Token_Hash":%q,"ROLES":["admin"]}]`, hashOf("tok"))},
	} {
		add(sc("users_file_"+c.id, map[string]string{"users.json": c.content}, ops{
			o("authenticate", "token", "tok"),
			o("user_list"),
			o("user_add", "name", "new", "pin", "pinned-new"),
			o("user_remove", "name", "u"),
			o("user_generate_token", "name", "u", "pin", "pinned-u"),
			o("set_token_hash", "name", "u", "hash", "h"),
		}))
	}
	add(scenario{ID: "files_with_invalid_utf8", Setup: setup{ConfigDir: "cfg", FilesB64: map[string]string{
		"cfg/permissions.json": base64.StdEncoding.EncodeToString([]byte("[{\"path\":\"p\xff.md\",\"owner\":\"a\xe2\x82\"}]")),
	}}, Ops: ops{
		o("get_document_rule", "path", "p\ufffd.md"),
		o("get_document_rule", "path", "p.md"),
	}})
	add(unix(scenario{ID: "users_file_is_directory", Setup: setup{ConfigDir: "cfg", DirModes: map[string]int{"cfg/users.json": 0o700}}, Ops: ops{
		o("authenticate", "token", "tok"), o("user_list"), o("user_add", "name", "u"),
	}}))
	add(unix(scenario{ID: "permissions_file_is_directory", Setup: setup{ConfigDir: "cfg", DirModes: map[string]int{"cfg/permissions.json": 0o700}}, Ops: ops{
		o("get_document_rule", "path", "p.md"),
		o("can_read", "as", actorOf("bob", "user"), "path", "p.md"),
		o("can_read_many", "as", actorOf("bob", "user"), "paths", strs("p.md")),
		o("set_document_rule", "rule", map[string]any{"path": "p.md", "owner": "a"}),
	}}))
	add(unix(scenario{ID: "unreadable_files", Setup: setup{ConfigDir: "cfg",
		Files:     map[string]string{"cfg/users.json": "[]", "cfg/groups.json": "[]", "cfg/permissions.json": "[]"},
		FileModes: map[string]int{"cfg/users.json": 0, "cfg/groups.json": 0, "cfg/permissions.json": 0}}, Ops: ops{
		o("authenticate", "token", "tok"),
		o("user_list"), o("group_list"),
		o("get_document_rule", "path", "p.md"),
		o("can_read", "as", actorOf("bob", "user"), "path", "p.md"),
		o("can_read_many", "as", actorOf("bob", "user"), "paths", strs("p.md")),
		o("group_add", "name", "g"),
	}}))

	// --- user management -----------------------------------------------
	add(sc("user_add_defaults_and_roles", nil, ops{
		o("user_add", "name", "ann", "pin", "t-ann"),
		o("user_add", "name", "adm", "roles", strs("admin"), "pin", "t-adm"),
		o("user_add", "name", "many", "roles", strs("user", "worker", "user"), "pin", "t-many"),
		o("user_add", "name", "emptyroles", "roles", strs(), "pin", "t-empty"),
		o("user_add", "name", "blankrole", "roles", strs(""), "pin", "t-blank"),
		o("user_list"),
		o("authenticate", "token", "t-adm"),
		o("authenticate", "token", "t-many"),
	}))
	add(sc("user_add_duplicate_and_empty_name", nil, ops{
		o("user_add", "name", "ann", "pin", "t-ann"),
		o("user_add", "name", "ann", "roles", strs("admin"), "pin", "t-other"),
		o("user_add", "name", ""),
		o("user_add", "name", " ", "pin", "t-space"),
		o("user_add", "name", "Ann", "pin", "t-ann-upper"),
		o("user_list"),
		o("authenticate", "token", "t-ann"),
		o("authenticate", "token", "t-other"),
	}))
	add(unix(scenario{ID: "user_add_preserves_existing_file_mode", Setup: setup{ConfigDir: "cfg", Files: map[string]string{"cfg/users.json": "[]"}, FileModes: map[string]int{"cfg/users.json": 0o644}}, Ops: ops{
		o("user_add", "name", "ann", "pin", "t-ann"),
	}}))
	add(sc("user_add_html_unicode_names", nil, ops{
		o("user_add", "name", "a<b>&c", "roles", strs("x<y"), "pin", "t-1"),
		o("user_add", "name", "line\u2028sep\u2029para", "pin", "t-2"),
		o("user_add", "name", "tab\there\nnl\rcr\bbs\fff\x01ctl\x7fdel", "pin", "t-3"),
		o("user_add", "name", "q\"uote\\back", "pin", "t-4"),
		o("user_add", "name", "ünï😀", "pin", "t-5"),
		o("user_list"),
	}))
	add(sc("user_remove_cleans_groups", map[string]string{
		"users.json":  usersJSON("ann|a|user", "bob|b|user", "carl|c|admin"),
		"groups.json": `[{"name":"g1","members":["ann","bob","ann"]},{"name":"g2","members":["bob"]},{"name":"g3","members":["carl"]},{"name":"g4"},{"name":"g5","members":[]}]`,
	}, ops{
		o("user_remove", "name", "bob"),
		o("user_list"),
		o("group_list"),
		o("user_remove", "name", "ann"),
		o("group_list"),
		o("user_remove", "name", "ghost"),
		o("user_remove", "name", ""),
		o("authenticate", "token", "b"),
		o("authenticate", "token", "c"),
	}))
	add(sc("user_remove_missing_writes_nothing", nil, ops{
		o("user_remove", "name", "ghost"),
		o("user_list"),
	}))
	add(sc("user_remove_last_user_leaves_empty_array", map[string]string{"users.json": usersJSON("ann|a|user"), "groups.json": `[{"name":"g","members":["ann"]}]`}, ops{
		o("user_remove", "name", "ann"),
		o("user_list"),
		o("group_list"),
	}))
	add(sc("user_remove_without_groups_file_creates_none", map[string]string{"users.json": usersJSON("ann|a|user")}, ops{
		o("user_remove", "name", "ann"),
	}))
	add(sc("user_generate_token_rotates", map[string]string{"users.json": usersJSON("ann|a|user", "bob|b|admin")}, ops{
		o("authenticate", "token", "a"),
		o("user_generate_token", "name", "ann", "pin", "rot-1"),
		o("authenticate", "token", "a"),
		o("authenticate", "token", "rot-1"),
		o("user_generate_token", "name", "ghost"),
		o("user_generate_token", "name", ""),
		o("user_list"),
	}))
	add(sc("set_token_hash_overwrites", map[string]string{"users.json": usersJSON("ann|a|user", "ann|dup|admin")}, ops{
		o("set_token_hash", "name", "ann", "hash", hashOf("seeded")),
		o("authenticate", "token", "seeded"),
		o("authenticate", "token", "a"),
		o("authenticate", "token", "dup"),
		o("set_token_hash", "name", "ghost", "hash", "x"),
		o("set_token_hash", "name", "ann", "hash", ""),
		o("user_list"),
	}))

	// --- groups ---------------------------------------------------------
	add(sc("group_lifecycle", nil, ops{
		o("group_add", "name", ""),
		o("group_add", "name", "g"),
		o("group_add", "name", "g"),
		o("group_add", "name", "G"),
		o("group_list"),
		o("group_add_member", "group", "g", "user", "ann"),
		o("group_add_member", "group", "g", "user", "ann"),
		o("group_add_member", "group", "g", "user", "bob"),
		o("group_add_member", "group", "g", "user", ""),
		o("group_add_member", "group", "ghost", "user", "ann"),
		o("group_list"),
		o("group_remove_member", "group", "g", "user", "ann"),
		o("group_remove_member", "group", "g", "user", "ghost"),
		o("group_remove_member", "group", "ghost", "user", "ann"),
		o("group_remove_member", "group", "g", "user", "bob"),
		o("group_remove_member", "group", "g", "user", ""),
		o("group_list"),
		o("group_remove", "name", "g"),
		o("group_remove", "name", "g"),
		o("group_list"),
	}))
	add(sc("group_remove_on_missing_file_writes_null", nil, ops{
		o("group_remove", "name", "ghost"),
		o("group_list"),
	}))
	add(sc("group_remove_member_on_null_members_writes_null_members", map[string]string{"groups.json": `[{"name":"g"}]`}, ops{
		o("group_remove_member", "group", "g", "user", "ann"),
		o("group_list"),
	}))
	add(sc("group_remove_member_empty_stays_empty_array", map[string]string{"groups.json": `[{"name":"g","members":[]}]`}, ops{
		o("group_remove_member", "group", "g", "user", "ann"),
		o("group_list"),
	}))
	add(sc("group_add_member_duplicate_names_use_first", map[string]string{"groups.json": `[{"name":"g","members":[]},{"name":"g","members":["x"]}]`}, ops{
		o("group_add_member", "group", "g", "user", "ann"),
		o("group_remove_member", "group", "g", "user", "x"),
		o("group_remove", "name", "g"),
		o("group_list"),
	}))
	add(sc("group_remove_empty_name_is_noop_write", map[string]string{"groups.json": `[{"name":"","members":["a"]},{"name":"g"}]`}, ops{
		o("group_remove", "name", ""),
		o("group_list"),
	}))

	// --- document rules -------------------------------------------------
	rule := func(path, owner string, extra ...any) map[string]any {
		m := map[string]any{"path": path, "owner": owner}
		putPairs(m, extra)
		return m
	}
	add(sc("set_document_rule_lifecycle", nil, ops{
		o("set_document_rule", "rule", rule("", "ann")),
		o("set_document_rule", "rule", rule("a.md", "ann", "read_users", strs("bob"), "write_groups", strs("g"))),
		o("get_document_rule", "path", "a.md"),
		o("set_document_rule", "rule", rule("a.md", "carl", "write_users", strs("dora"))),
		o("get_document_rule", "path", "a.md"),
		o("set_document_rule", "rule", rule("b.md", "ann")),
		o("set_document_rule", "rule", rule("*", "root", "read_groups", strs("all"))),
		o("get_document_rule", "path", "zzz.md"),
		o("set_document_rule", "rule", rule("a.md", "")),
		o("get_document_rule", "path", "a.md"),
		o("set_document_rule", "rule", rule("b.md", "", "read_users", strs("x"))),
		o("get_document_rule", "path", "b.md"),
		o("set_document_rule", "rule", rule("new.md", "", "read_users", strs("x"))),
		o("get_document_rule", "path", "new.md"),
		o("set_document_rule", "rule", rule("*", "")),
		o("set_document_rule", "rule", rule("b.md", "")),
		o("get_document_rule", "path", "b.md"),
	}))
	add(sc("set_document_rule_empty_owner_unknown_writes_null", nil, ops{
		o("set_document_rule", "rule", rule("ghost.md", "")),
		o("get_document_rule", "path", "ghost.md"),
	}))
	add(sc("set_document_rule_omitempty_and_escapes", nil, ops{
		o("set_document_rule", "rule", rule("a<b>&.md", "o\u2028", "read_users", strs("u&1", ""), "read_groups", strs(), "write_users", strs("w\"q"), "write_groups", strs("g\\"))),
		o("set_document_rule", "rule", rule("empty-lists.md", "ann", "read_users", strs(), "write_groups", strs())),
		o("get_document_rule", "path", "a<b>&.md"),
		o("get_document_rule", "path", "empty-lists.md"),
	}))
	add(sc("set_document_rule_replace_keeps_position", map[string]string{"permissions.json": `[{"path":"a.md","owner":"x"},{"path":"b.md","owner":"y"},{"path":"c.md","owner":"z"}]`}, ops{
		o("set_document_rule", "rule", rule("b.md", "new")),
		o("set_document_rule", "rule", rule("a.md", "")),
		o("set_document_rule", "rule", rule("c.md", "z2", "read_users", strs("q"))),
		o("get_document_rule", "path", "a.md"),
	}))
	add(sc("set_document_rule_duplicate_paths_touch_first", map[string]string{"permissions.json": `[{"path":"a.md","owner":"first"},{"path":"a.md","owner":"second"}]`}, ops{
		o("set_document_rule", "rule", rule("a.md", "")),
		o("get_document_rule", "path", "a.md"),
	}))
	add(sc("set_document_rule_normalizes_existing_file", map[string]string{"permissions.json": `[ {"PATH":"a.md","Owner":"x","unknown":1,"read_users":[],"write_users":null} ]`}, ops{
		o("set_document_rule", "rule", rule("b.md", "y")),
	}))

	// --- write failures -------------------------------------------------
	add(unix(scenario{ID: "write_fails_when_config_dir_locked", Setup: setup{ConfigDir: "cfg", LockConfigDir: 0o500}, Ops: ops{
		o("user_add", "name", "ann", "pin", "t"),
		o("group_add", "name", "g"),
		o("set_document_rule", "rule", rule("a.md", "ann")),
		o("group_remove", "name", "g"),
		o("user_list"),
	}}))
	add(unix(scenario{ID: "write_fails_for_readonly_existing_file", Setup: setup{ConfigDir: "cfg", Files: map[string]string{"cfg/groups.json": "[]"}, FileModes: map[string]int{"cfg/groups.json": 0o400}}, Ops: ops{
		o("group_add", "name", "g"),
		o("group_list"),
	}}))

	// --- long deterministic chain --------------------------------------
	chain := ops{
		o("user_add", "name", "admin", "roles", strs("admin"), "pin", "c-admin"),
		o("user_add", "name", "ann", "pin", "c-ann"),
		o("user_add", "name", "bob", "pin", "c-bob"),
		o("group_add", "name", "team"),
		o("group_add_member", "group", "team", "user", "ann"),
		o("group_add_member", "group", "team", "user", "bob"),
		o("set_document_rule", "rule", rule("plan.md", "ann", "read_groups", strs("team"), "write_users", strs("bob"))),
		o("can_read", "as", actorOf("bob", "user"), "path", "plan.md"),
		o("can_write", "as", actorOf("bob", "user"), "path", "plan.md"),
		o("can_write", "as", actorOf("carl", "user"), "path", "plan.md"),
		o("user_remove", "name", "bob"),
		o("can_read", "as", actorOf("bob", "user"), "path", "plan.md"),
		o("group_list"),
		o("authenticate", "token", "c-bob"),
		o("authenticate", "token", "c-ann"),
		o("user_generate_token", "name", "ann", "pin", "c-ann2"),
		o("authenticate", "token", "c-ann"),
		o("authenticate", "token", "c-ann2"),
		o("set_document_rule", "rule", rule("plan.md", "")),
		o("can_read", "as", actorOf("bob", "user"), "path", "plan.md"),
		o("user_list"),
	}
	add(sc("chain_users_groups_rules", nil, chain))

	sort.SliceStable(all, func(i, j int) bool { return all[i].ID < all[j].ID })
	for i := 1; i < len(all); i++ {
		if all[i].ID == all[i-1].ID {
			fatal("duplicate scenario id %s", all[i].ID)
		}
	}
	return all
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "FAIL "+strings.TrimSpace(format)+"\n", args...)
	os.Exit(1)
}
