package inventory

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A real native Git squash and fresh single-branch clone exercise the structural
// source-preservation contract. These are not production Go behavior fixtures.
func TestRecordedOracleSurvivesSquashWithoutCallerMutation(t *testing.T) {
	git := func(root string, args ...string) string {
		t.Helper()
		//nolint:gosec // args are fixed test operations in disposable local repositories, without a shell.
		cmd := exec.Command("git", append([]string{"-c", "user.name=Oracle Bundle Test", "-c", "user.email=oracle-bundle@example.invalid", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = root
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	write := func(root, name, text string) {
		t.Helper()
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	root := t.TempDir()
	git(root, "init", "--initial-branch=main", "--template=", root)
	write(root, "go.mod", "module oracle-bundle.test\n\ngo 1.26.6\n")
	write(root, "internal/probe/value.go", "package probe\nconst Value = 1\n")
	git(root, "add", ".")
	git(root, "commit", "-m", "base")
	anchor := git(root, "rev-parse", "HEAD")
	git(root, "switch", "-c", "source")
	write(root, "internal/probe/value.go", "package probe\nconst Value = 2\n")
	git(root, "add", ".")
	git(root, "commit", "-m", "actual changed source")
	p := git(root, "rev-parse", "HEAD")
	pinned, err := ComputeGitRevisionProductionSourceDigest(root, p)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := CreateOracleBundle(root, p, anchor)
	if err != nil {
		t.Fatal(err)
	}
	record := ProvenanceDocument{SchemaVersion: 1, Oracle: Oracle{Commit: p, Release: "test-record"}, OracleBundle: proof, ProductionSourceDigest: pinned}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	write(root, oracleProvenancePath, string(data)+"\n")
	git(root, "add", ".")
	git(root, "commit", "-m", "record the actual source bundle")
	git(root, "switch", "main")
	git(root, "merge", "--squash", "source")
	git(root, "commit", "-m", "squash source and record")
	m := git(root, "rev-parse", "HEAD")
	clone := filepath.Join(t.TempDir(), "fresh-main")
	git(root, "clone", "--no-local", "--single-branch", "--branch", "main", "--no-tags", root, clone)
	absent := func() {
		t.Helper()
		//nolint:gosec // p is the commit returned by Git in this test's disposable repository.
		cmd := exec.Command("git", "cat-file", "-e", p+"^{commit}")
		cmd.Dir = clone
		if err := cmd.Run(); err == nil {
			t.Fatal("original source leaked into caller Git objects")
		}
	}
	absent()
	before := git(clone, "show-ref")
	materialized := filepath.Join(t.TempDir(), "source")
	if err := CloneOracleSource(clone, p, materialized); err != nil {
		t.Fatalf("materialize unavailable source from fresh squash clone: %v", err)
	}
	if git(materialized, "rev-parse", "HEAD") != p || git(materialized, "status", "--porcelain") != "" {
		t.Fatal("materialized source is not the exact clean source commit")
	}
	if git(materialized, "show", "HEAD:internal/probe/value.go") != "package probe\nconst Value = 2" {
		t.Fatal("materialized source does not preserve original production bytes")
	}
	absent()
	if err := VerifyOracleBundle(clone, m, p); err != nil {
		t.Fatalf("fresh squash source verification: %v", err)
	}
	got, err := ComputeGitRevisionProductionSourceDigest(clone, p)
	if err != nil || got != pinned {
		t.Fatalf("bundle source digest=%s error=%v, want %s", got, err, pinned)
	}
	verifier, err := NewProductionSourceVerifier(clone)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("captured Git remains usable after PATH isolation", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		if err := verifier(p); err != nil {
			t.Fatal(err)
		}
	})
	if git(clone, "show-ref") != before || git(clone, "rev-parse", "HEAD") != m || git(clone, "status", "--porcelain") != "" {
		t.Fatal("verification mutated caller refs, HEAD or worktree")
	}
	absent()
	if err := VerifyOracleBundle(clone, anchor, p); err == nil {
		t.Fatal("accepted a wrong checked head with no source record")
	}

	for _, kind := range []string{"missing", "checksum", "advertised-source", "corrupt-pack-with-new-checksum", "unrelated-anchor", "changed-production", "malformed-anchor"} {
		t.Run(kind, func(t *testing.T) {
			git(clone, "checkout", "--detach", m)
			mutated := record
			copyProof := *proof
			copyProof.Data = append([]byte(nil), proof.Data...)
			mutated.OracleBundle = &copyProof
			switch kind {
			case "missing":
				mutated.OracleBundle = nil
			case "checksum":
				copyProof.SHA256 = strings.Repeat("0", 64)
			case "advertised-source":
				mutated.Oracle.Commit = strings.Repeat("a", 40)
			case "corrupt-pack-with-new-checksum":
				copyProof.Data[len(copyProof.Data)-1] ^= 1
				hash := sha256.Sum256(copyProof.Data)
				copyProof.SHA256 = hex.EncodeToString(hash[:])
			case "unrelated-anchor":
				copyProof.Anchor = strings.Repeat("b", 40)
				copyProof.Data = []byte(strings.Replace(string(copyProof.Data), "-"+anchor+" ", "-"+copyProof.Anchor+" ", 1))
				hash := sha256.Sum256(copyProof.Data)
				copyProof.SHA256 = hex.EncodeToString(hash[:])
			case "changed-production":
				write(clone, "internal/probe/value.go", "package probe\nconst Value = 3\n")
			case "malformed-anchor":
				copyProof.Anchor = " " + anchor
			}
			data, err := json.MarshalIndent(mutated, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			write(clone, oracleProvenancePath, string(data)+"\n")
			git(clone, "add", ".")
			git(clone, "commit", "-m", "negative "+kind)
			head := git(clone, "rev-parse", "HEAD")
			if err := VerifyOracleBundle(clone, head, mutated.Oracle.Commit); err == nil {
				t.Fatalf("accepted %s", kind)
			}
			destination := filepath.Join(t.TempDir(), "rejected-source")
			if err := CloneOracleSource(clone, mutated.Oracle.Commit, destination); err == nil {
				t.Fatalf("materialized invalid %s source", kind)
			}
			if _, err := os.Stat(destination); !os.IsNotExist(err) {
				t.Fatalf("invalid %s source created a destination: %v", kind, err)
			}
			absent()
		})
	}
}
