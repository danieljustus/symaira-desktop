package doctor

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/danieljustus/symaira-room/internal/config"
	"github.com/danieljustus/symaira-room/internal/identity"
	"github.com/danieljustus/symaira-room/internal/journal"
)

type Status string

const (
	OK   Status = "ok"
	Warn Status = "warn"
	Fail Status = "fail"
)

type Check struct {
	Name        string `json:"name"`
	Status      Status `json:"status"`
	Message     string `json:"message"`
	Remediation string `json:"remediation"`
}
type Tool struct {
	Name        string `json:"name"`
	Path        string `json:"path,omitempty"`
	Version     string `json:"version,omitempty"`
	Status      Status `json:"status"`
	Remediation string `json:"remediation"`
}
type Report struct {
	Checks []Check `json:"checks"`
	Tools  []Tool  `json:"tools"`
	Failed bool    `json:"failed"`
}

func add(r *Report, name string, s Status, msg, fix string) {
	r.Checks = append(r.Checks, Check{name, s, msg, fix})
	if s == Fail {
		r.Failed = true
	}
}
func fileExists(path string) bool { _, err := os.Stat(path); return err == nil }

func Run(dir string) (*Report, error) {
	r := &Report{Checks: []Check{}, Tools: []Tool{}}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	// A room is identified by its durable manifest; .symroom is local/sync state.
	if fileExists(filepath.Join(dir, "room.toml")) {
		add(r, "room_manifest", OK, "room.toml is present", "No action needed.")
	} else {
		add(r, "room_manifest", Fail, "room.toml is missing", "Run `symroom init <directory> --identity <name>` in an empty room directory.")
	}
	dot := filepath.Join(dir, ".symroom")
	if fileExists(dot) {
		add(r, "sync_folder", OK, ".symroom exists and is local room state", "Keep .symroom out of version control and sync it only through a trusted private folder.")
	} else {
		add(r, "sync_folder", Warn, ".symroom is missing; this may be a non-room directory", "Run `symroom init` here, or run doctor from the room root.")
	}

	cfg := config.NewLoader()
	c, cfgErr := cfg.Load()
	if cfgErr != nil {
		add(r, "identity_config", Warn, fmt.Sprintf("configuration could not be loaded: %v", cfgErr), "Fix the symroom configuration file or set SYMROOM_DEFAULT_IDENTITY.")
	}
	idName := ""
	if cfgErr == nil && c != nil {
		idName = c.DefaultIdentity
	}
	if idName == "" {
		add(r, "identity_presence", Fail, "no default identity is configured", "Set `default_identity` in symroom config or create/select an identity with `--identity <name>`.")
	} else {
		id, loadErr := identity.Load(idName)
		if loadErr != nil {
			add(r, "identity_resolution", Fail, fmt.Sprintf("identity %q could not be resolved: %v", idName, loadErr), "Create it with `symroom identity create <name>` or fix the configured identity name.")
		} else {
			add(r, "identity_resolution", OK, fmt.Sprintf("identity %q resolves to %s", idName, id.MemberID), "No action needed.")
		}
		p := filepath.Join(identity.IdentitiesDir(), idName+".json")
		if st, e := os.Stat(p); e == nil {
			if st.Mode().Perm() == 0600 {
				add(r, "identity_key_mode", OK, "identity key file mode is 0600", "No action needed.")
			} else {
				add(r, "identity_key_mode", Fail, fmt.Sprintf("identity key file mode is %04o", st.Mode().Perm()), "Run `chmod 600 "+p+"` and ensure the identities directory is private.")
			}
		} else if !errors.Is(e, os.ErrNotExist) {
			add(r, "identity_key_mode", Fail, fmt.Sprintf("cannot inspect identity key file: %v", e), "Restore access to the identity file and verify its permissions are 0600.")
		} else {
			add(r, "identity_key_mode", Warn, "identity is supplied by a non-file provider", "Confirm the external provider protects the private key and does not expose it in logs.")
		}
	}
	// Detect duplicate stored member IDs, a useful proxy for copied identity/device state.
	names, _ := identity.List()
	ids := map[string][]string{}
	for _, n := range names {
		if id, e := identity.Load(n); e == nil {
			ids[id.MemberID] = append(ids[id.MemberID], n)
		}
	}
	dup := []string{}
	for mid, ns := range ids {
		if len(ns) > 1 {
			dup = append(dup, mid+" ("+strings.Join(ns, ", ")+")")
		}
	}
	sort.Strings(dup)
	if len(dup) > 0 {
		add(r, "duplicate_identity", Fail, "multiple identity files resolve to the same member ID: "+strings.Join(dup, "; "), "Remove or rename copied identity files; each device/person should use a deliberate, unique identity.")
	} else {
		add(r, "duplicate_identity", OK, "no duplicate stored member IDs detected", "No action needed.")
	}

	idx := filepath.Join(dot, "index.sqlite")
	journalDir := filepath.Join(dir, "journal")
	ist, ie := os.Stat(idx)
	if ie != nil {
		add(r, "index", Warn, "derived index is missing", "Run `symroom index` to rebuild .symroom/index.sqlite.")
	} else {
		stale := false
		_ = filepath.Walk(journalDir, func(p string, info os.FileInfo, e error) error {
			if e == nil && info != nil && info.ModTime().After(ist.ModTime()) {
				stale = true
			}
			return nil
		})
		if stale {
			add(r, "index", Warn, "derived index is older than journal data", "Run `symroom index` to rebuild the derived index.")
		} else {
			add(r, "index", OK, "derived index is present and current", "No action needed.")
		}
	}
	vr, ve := journal.New(filepath.Join(dir, "journal")).Verify()
	if ve != nil {
		add(r, "room_integrity", Fail, fmt.Sprintf("journal verification could not run: %v", ve), "Restore the journal directory and run `symroom verify` for detailed errors.")
	} else if !vr.Valid {
		add(r, "room_integrity", Fail, fmt.Sprintf("journal verification found %d finding(s)", len(vr.Findings)), "Run `symroom verify` and repair or restore the affected journal events from a trusted copy.")
	} else {
		add(r, "room_integrity", OK, "journal chains and signatures verify", "No action needed.")
	}
	for _, name := range []string{"symdesk", "symbrain", "symvault"} {
		t := Tool{Name: name, Status: Warn, Remediation: "Install " + name + " and place it on PATH if this integration is needed; otherwise this warning is informational."}
		if p, e := exec.LookPath(name); e == nil {
			t.Path = p
			out, e := exec.Command(p, "version", "--json").Output()
			if e == nil {
				var v map[string]interface{}
				if json.Unmarshal(out, &v) == nil {
					if x, ok := v["version"].(string); ok {
						t.Version = x
					}
				}
				if t.Version == "" {
					t.Version = strings.TrimSpace(string(out))
				}
			}
			if t.Version != "" {
				t.Status = OK
				t.Remediation = "No action needed."
			} else {
				t.Remediation = "Run `" + name + " version --json` successfully or verify the installed binary."
			}
		}
		r.Tools = append(r.Tools, t)
	}
	return r, nil
}
