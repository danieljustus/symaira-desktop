// sidecar-go-helper is the Go production-sidecar process used by the
// cross-language round-trip gate. Its stdout is one JSON result; diagnostics
// are deliberately kept on stderr.
package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/sidecar"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

type documentInput struct {
	Path     string   `json:"path"`
	Markdown string   `json:"markdown"`
	MTimeNS  int64    `json:"mtime_ns"`
	Links    []string `json:"links,omitempty"`
}

type operationInput struct {
	Documents []documentInput `json:"documents"`
	Delete    []string        `json:"delete,omitempty"`
	Query     string          `json:"query,omitempty"`
	Ready     string          `json:"ready,omitempty"`
	Go        string          `json:"go,omitempty"`
	Release   string          `json:"release,omitempty"`
	Vault     string          `json:"vault,omitempty"`
	HoldMS    int             `json:"hold_ms,omitempty"`
}

type result struct {
	Outcome    string      `json:"outcome"`
	ErrorClass string      `json:"error_class,omitempty"`
	Busy       bool        `json:"busy,omitempty"`
	ElapsedMS  int64       `json:"elapsed_ms"`
	Snapshot   interface{} `json:"snapshot,omitempty"`
	Hits       interface{} `json:"hits,omitempty"`
}

func main() {
	started := time.Now()
	cmd, dbPath, inputPath := args()
	input := operationInput{}
	if inputPath != "" {
		data, err := os.ReadFile(inputPath)
		if err != nil {
			emit(started, err)
			return
		}
		if err := json.Unmarshal(data, &input); err != nil {
			emit(started, err)
			return
		}
	}
	var err error
	switch cmd {
	case "create", "mutate", "rollback", "snapshot", "search", "refresh", "open-check", "integrity", "writer", "lock-holder":
		err = run(cmd, dbPath, input)
	default:
		err = fmt.Errorf("unknown command %q", cmd)
	}
	if err != nil {
		emit(started, err)
		return
	}
	out := result{Outcome: "ok", ElapsedMS: time.Since(started).Milliseconds()}
	if cmd == "snapshot" || cmd == "create" || cmd == "mutate" || cmd == "rollback" {
		if db, e := sidecar.Open(dbPath); e == nil {
			out.Snapshot, _ = snapshot(db.RawConnectionForTesting())
			_ = db.Close()
		}
	}
	if cmd == "search" {
		db, e := sidecar.Open(dbPath)
		if e == nil {
			out.Hits, err = search(db, input.Query)
			_ = db.Close()
		}
		if err != nil {
			emit(started, err)
			return
		}
	}
	printJSON(out)
}

func args() (string, string, string) {
	cmd := ""
	dbPath, inputPath := "", ""
	for i := 1; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--db":
			i++
			if i < len(os.Args) {
				dbPath = os.Args[i]
			}
		case "--input":
			i++
			if i < len(os.Args) {
				inputPath = os.Args[i]
			}
		default:
			if cmd == "" {
				cmd = os.Args[i]
			}
		}
	}
	return cmd, dbPath, inputPath
}

func run(cmd, dbPath string, in operationInput) error {
	if cmd == "open-check" {
		db, err := sidecar.Open(dbPath)
		if db != nil {
			_ = db.Close()
		}
		return err
	}
	if cmd == "integrity" {
		db, err := sidecar.Open(dbPath)
		if err != nil {
			return err
		}
		defer db.Close()
		return db.CheckIntegrity()
	}
	if cmd == "lock-holder" {
		return holdLock(dbPath, in)
	}
	if cmd == "writer" {
		return writerRetry(dbPath, in)
	}
	db, err := sidecar.Open(dbPath)
	if err != nil {
		return err
	}
	defer func() { _, _ = db.RawConnectionForTesting().Exec("PRAGMA wal_checkpoint(TRUNCATE)"); _ = db.Close() }()
	switch cmd {
	case "refresh":
		if in.Vault == "" {
			return errors.New("refresh vault is required")
		}
		return db.RefreshIndex(in.Vault)
	case "create":
		return index(db, in.Documents)
	case "mutate", "writer":
		if err := index(db, in.Documents); err != nil {
			return err
		}
		for _, path := range in.Delete {
			if err := db.DeleteDocument(path); err != nil {
				return err
			}
		}
		return nil
	case "rollback":
		if len(in.Documents) != 1 {
			return errors.New("rollback requires one document")
		}
		doc, err := makeDocument(in.Documents[0])
		if err != nil {
			return err
		}
		return db.IndexDocument(doc)
	case "snapshot", "search":
		return nil
	}
	return nil
}

func makeDocument(in documentInput) (*vault.Document, error) {
	doc, err := vault.ParseBytes(in.Path, []byte(in.Markdown))
	if err != nil {
		return nil, err
	}
	doc.ModTime = time.Unix(0, in.MTimeNS).UTC()
	if in.Links != nil {
		doc.Links = append([]string(nil), in.Links...)
	}
	return doc, nil
}

func index(db *sidecar.DB, inputs []documentInput) error {
	docs := make([]*vault.Document, 0, len(inputs))
	for _, in := range inputs {
		doc, err := makeDocument(in)
		if err != nil {
			return err
		}
		docs = append(docs, doc)
	}
	return db.IndexDocuments(docs)
}

func writerRetry(path string, in operationInput) error {
	deadline := time.Now().Add(5 * time.Second)
	for {
		db, err := sidecar.Open(path)
		if err == nil {
			err = index(db, in.Documents)
			if err == nil {
				for _, deleted := range in.Delete {
					err = db.DeleteDocument(deleted)
					if err != nil {
						break
					}
				}
			}
			_, _ = db.RawConnectionForTesting().Exec("PRAGMA wal_checkpoint(TRUNCATE)")
			_ = db.Close()
		}
		if err == nil {
			return nil
		}
		class, _ := classify(err)
		if class != "locked" || time.Now().After(deadline) {
			return err
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func holdLock(path string, in operationInput) error {
	db, err := sidecar.Open(path)
	if err != nil {
		return err
	}
	defer db.Close()
	conn := db.RawConnectionForTesting()
	if _, err := conn.Exec("BEGIN IMMEDIATE"); err != nil {
		return err
	}
	defer conn.Exec("ROLLBACK")
	if in.Ready != "" {
		if err := os.WriteFile(in.Ready, []byte("ready\n"), 0600); err != nil {
			return err
		}
	}
	if in.Go != "" {
		if err := waitFile(in.Go, 10*time.Second); err != nil {
			return err
		}
	}
	deadline := time.Now().Add(time.Duration(in.HoldMS) * time.Millisecond)
	for {
		if in.Release != "" {
			if _, err := os.Stat(in.Release); err == nil {
				return nil
			}
		}
		if in.HoldMS > 0 && time.Now().After(deadline) {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitFile(path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("handshake timeout waiting for %s", path)
}

func emit(started time.Time, err error) {
	class, busy := classify(err)
	fmt.Fprintf(os.Stderr, "%s\n", err)
	printJSON(result{Outcome: "error", ErrorClass: class, Busy: busy, ElapsedMS: time.Since(started).Milliseconds()})
}
func classify(err error) (string, bool) {
	if err == nil {
		return "", false
	}
	s := strings.ToLower(err.Error())
	if strings.Contains(s, "locked") || strings.Contains(s, "busy") {
		return "locked", true
	}
	if strings.Contains(s, "not a database") || strings.Contains(s, "malformed") || strings.Contains(s, "disk image") || strings.Contains(s, "file is encrypted") || strings.Contains(s, "integrity check failed") || strings.Contains(s, "page") || strings.Contains(s, "btree") {
		return "corrupt", false
	}
	if strings.Contains(s, "permission") || strings.Contains(s, "readonly") || strings.Contains(s, "read-only") {
		return "readonly", false
	}
	if strings.Contains(s, "constraint") {
		return "constraint", false
	}
	return "error", false
}
func printJSON(v interface{}) { b, _ := json.Marshal(v); fmt.Println(string(b)) }

func snapshot(conn *sql.DB) (map[string]interface{}, error) {
	state := map[string]interface{}{}
	state["files"], _ = rows(conn, `SELECT path,sha256,title,created_at,modified_at,"type",document_date,person,status,due_date,confidence,ocr_json_path,simhash,asn,size,mtime_ns FROM files ORDER BY path`, func(r *sql.Rows) (map[string]interface{}, error) {
		var p, sha, title, created, modified, typ string
		var date, person, status, due, ocr, simhash sql.NullString
		var conf, asn, size, mtime sql.NullInt64
		err := r.Scan(&p, &sha, &title, &created, &modified, &typ, &date, &person, &status, &due, &conf, &ocr, &simhash, &asn, &size, &mtime)
		return map[string]interface{}{"path": p, "sha256": sha, "title": title, "created_at": created, "modified_at": modified, "type": typ, "document_date": nullableString(date), "person": nullableString(person), "status": nullableString(status), "due_date": nullableString(due), "confidence": nullableInt(conf), "ocr_json_path": nullableString(ocr), "simhash": nullableString(simhash), "asn": nullableInt(asn), "size": nullableInt(size), "mtime_ns": nullableInt(mtime)}, err
	})
	state["properties"], _ = rows(conn, `SELECT f.path,p.key,p.value,p.value_type FROM file_properties p JOIN files f ON f.id=p.file_id ORDER BY f.path,p.key`, func(r *sql.Rows) (map[string]interface{}, error) {
		var p, k, typ string
		var v sql.NullString
		err := r.Scan(&p, &k, &v, &typ)
		return map[string]interface{}{"path": p, "key": k, "value": nullableString(v), "value_type": typ}, err
	})
	state["links"], _ = rows(conn, `SELECT from_path,to_path,kind FROM links ORDER BY from_path,to_path,kind`, func(r *sql.Rows) (map[string]interface{}, error) {
		var f, t, k string
		err := r.Scan(&f, &t, &k)
		return map[string]interface{}{"from": f, "to": t, "kind": k}, err
	})
	state["fts_search"], _ = rows(conn, `SELECT f.path,x.title,x.body FROM fts_search x JOIN files f ON f.id=x.rowid ORDER BY f.path`, func(r *sql.Rows) (map[string]interface{}, error) {
		var p, t, b string
		err := r.Scan(&p, &t, &b)
		return map[string]interface{}{"path": p, "title": t, "body": b}, err
	})
	state["fts_norm"], _ = rows(conn, `SELECT f.path,x.norm FROM fts_norm x JOIN files f ON f.id=x.rowid ORDER BY f.path`, func(r *sql.Rows) (map[string]interface{}, error) {
		var p, n string
		err := r.Scan(&p, &n)
		return map[string]interface{}{"path": p, "norm": n}, err
	})
	state["fts_tri"], _ = rows(conn, `SELECT f.path,x.body FROM fts_tri x JOIN files f ON f.id=x.rowid ORDER BY f.path`, func(r *sql.Rows) (map[string]interface{}, error) {
		var p, b string
		err := r.Scan(&p, &b)
		return map[string]interface{}{"path": p, "body": b}, err
	})
	return state, nil
}
func rows(conn *sql.DB, query string, fn func(*sql.Rows) (map[string]interface{}, error)) ([]map[string]interface{}, error) {
	rs, err := conn.Query(query)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	out := []map[string]interface{}{}
	for rs.Next() {
		v, e := fn(rs)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rs.Err()
}
func nullableString(v sql.NullString) interface{} {
	if v.Valid {
		return v.String
	}
	return nil
}
func nullableInt(v sql.NullInt64) interface{} {
	if v.Valid {
		return v.Int64
	}
	return nil
}
func search(db *sidecar.DB, q string) ([]map[string]string, error) {
	hits, err := db.Search(q)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]string, 0, len(hits))
	for _, h := range hits {
		out = append(out, map[string]string{"path": h.Path, "title": h.Title, "snippet": h.Body})
	}
	return out, nil
}

// Keep filepath imported in the helper's Windows build when future lock
// fixtures add nested paths; this also documents that all paths are local.
var _ = filepath.Separator
