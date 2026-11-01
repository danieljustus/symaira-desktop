package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/danieljustus/symaira-desktop/internal/storage"
	"github.com/danieljustus/symaira-desktop/scripts/rust-port/inventory"
)

func committed(t *testing.T) ([]byte, inventory.Oracle) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "..", defaultOutput))
	if err != nil {
		t.Fatalf("read committed fixture: %v", err)
	}
	var head struct {
		Oracle inventory.Oracle `json:"oracle"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		t.Fatal(err)
	}
	return data, head.Oracle
}

func TestCommittedFixtureValidates(t *testing.T) {
	data, oracle := committed(t)
	doc, err := validateFixture(data, oracle)
	if err != nil {
		t.Fatalf("validateFixture: %v", err)
	}
	if doc.ServerMajor != 17 {
		t.Fatalf("server major %d, want the pinned PostgreSQL 17", doc.ServerMajor)
	}
}

func TestValidateFixtureRejectsDamage(t *testing.T) {
	data, oracle := committed(t)
	if _, err := validateFixture(data, inventory.Oracle{Commit: strings.Repeat("0", 40), Release: oracle.Release}); err == nil {
		t.Fatal("a different oracle commit must be rejected")
	}
	if _, err := validateFixture([]byte(strings.Replace(string(data), `"schema_version": 1`, `"schema_version": 2`, 1)), oracle); err == nil {
		t.Fatal("a different schema version must be rejected")
	}
	if _, err := validateFixture([]byte(strings.Replace(string(data), `"id": "crud_basic"`, `"id": "schema_fresh"`, 1)), oracle); err == nil {
		t.Fatal("a duplicated scenario id must be rejected")
	}
	if _, err := validateFixture([]byte(strings.Replace(string(data), `"op": "open"`, `"unknown": 1, "op": "open"`, 1)), oracle); err == nil {
		t.Fatal("an unknown field must be rejected")
	}
}

func TestScenarioIDsFitDatabaseNames(t *testing.T) {
	seen := map[string]bool{}
	for _, spec := range scenarioSpecs() {
		if seen[spec.ID] {
			t.Fatalf("duplicate scenario %s", spec.ID)
		}
		seen[spec.ID] = true
		if n := len(dbPrefix + spec.ID); n > 63 {
			t.Fatalf("database name for %s is %d bytes; PostgreSQL allows 63", spec.ID, n)
		}
		if len(spec.Steps) == 0 {
			t.Fatalf("scenario %s has no steps", spec.ID)
		}
	}
}

func TestBuildDSN(t *testing.T) {
	admin, _ := url.Parse("postgres://postgres:s3cret@127.0.0.1:5432/postgres?sslmode=disable")
	r := &runner{admin: admin, adminPW: "s3cret", db: "symdesk_pg_go_x"}
	cases := []struct {
		name string
		spec *dsnSpec
		want string
	}{
		{"default", nil, "postgres://postgres:s3cret@127.0.0.1:5432/symdesk_pg_go_x?sslmode=disable"},
		{"closed port", &dsnSpec{Host: "closed"}, "postgres://postgres:s3cret@127.0.0.1:1/symdesk_pg_go_x?sslmode=disable"},
		{"params sorted, space escaped", &dsnSpec{Params: map[string]string{"options": "-c search_path=legacy", "application_name": "a"}},
			"postgres://postgres:s3cret@127.0.0.1:5432/symdesk_pg_go_x?application_name=a&options=-c%20search_path%3Dlegacy&sslmode=disable"},
		{"keyword", &dsnSpec{Form: "keyword"}, "host=127.0.0.1 port=5432 user=postgres password=s3cret dbname=symdesk_pg_go_x sslmode=disable"},
		{"missing database", &dsnSpec{Database: "missing"}, "postgres://postgres:s3cret@127.0.0.1:5432/symdesk_pg_missing?sslmode=disable"},
	}
	for _, tc := range cases {
		if got := r.buildDSN(tc.spec); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
	raw := "not a dsn"
	if got := r.buildDSN(&dsnSpec{Raw: &raw}); got != raw {
		t.Errorf("raw DSN rewritten: %q", got)
	}
	if a, b := r.rolePassword("symdesk_pg_ro"), r.rolePassword("symdesk_pg_nocreate"); a == b || !strings.HasPrefix(a, "pw-") {
		t.Errorf("role passwords %q %q", a, b)
	}
}

func TestClassify(t *testing.T) {
	r := &runner{db: "symdesk_pg_go_x"}
	pgErr := &pgconn.PgError{Severity: "ERROR", Code: "42703", Message: `column "storage_key" does not exist`}
	cases := []struct {
		err       error
		class     string
		sqlstate  string
		hasPrefix string
	}{
		{fmt.Errorf("%w: %q", storage.ErrNotFound, "k"), "not_found", "", ""},
		{storage.ErrInvalidKey, "invalid_key", "", ""},
		{fmt.Errorf("get storage key %q: %w", "k", context.DeadlineExceeded), "timeout", "", ""},
		{errors.New("closed pool"), "closed", "", ""},
		{errors.New("storage: PostgreSQL DSN must not be empty"), "empty_dsn", "", ""},
		{errors.New("parse PostgreSQL DSN: cannot parse"), "dsn", "", "parse PostgreSQL DSN: "},
		{fmt.Errorf("connect to PostgreSQL: %w", pgErr), "connect", "42703", "connect to PostgreSQL: "},
		{fmt.Errorf("get storage key %q: %w", "k", pgErr), "server", "42703", ""},
	}
	for _, tc := range cases {
		got := r.classify(tc.err)
		if got.Class != tc.class || got.SQLState != tc.sqlstate || got.Prefix != tc.hasPrefix {
			t.Errorf("%v: got %+v", tc.err, got)
		}
	}
	defer func() {
		if recover() == nil {
			t.Fatal("an unclassified error must abort generation")
		}
	}()
	r.classify(errors.New("something else entirely"))
}

func TestValueInfoInlinesOnlySmallValues(t *testing.T) {
	small := info(make([]byte, inlineValueMax))
	big := info(make([]byte, inlineValueMax+1))
	if small.B64 == "" || small.SHA256 != "" {
		t.Errorf("small value: %+v", small)
	}
	if big.B64 != "" || big.SHA256 == "" || big.Len != inlineValueMax+1 {
		t.Errorf("big value: %+v", big)
	}
	if empty := info(nil); empty.Len != 0 || empty.B64 != "" {
		t.Errorf("empty value: %+v", empty)
	}
}
