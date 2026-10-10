package inventory

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

const oracleProvenancePath = "testdata/port/provenance.json"
const maxOracleRecordBytes = 64 << 20

// OracleBundle preserves the actual source commit across squash merges. Data is
// native Git bundle bytes, encoded as base64 by encoding/json, not reconstructed
// production code. Anchor must remain an ancestor of the checked revision.
type OracleBundle struct {
	Anchor string `json:"anchor"`
	SHA256 string `json:"sha256"`
	Data   []byte `json:"data"`
}

func oracleGitExecutable() (string, error) {
	git, err := exec.LookPath("git")
	if err != nil {
		return "", err
	}
	return filepath.Abs(git)
}

// privateOracleStore reads the caller's immutable objects through alternates;
// imported objects and refs exist only in the owned temporary bare repository.
func privateOracleStore(repoRoot, git string) (string, func(), error) {
	objects, err := inventoryGitOutputWithExecutable(repoRoot, git, "rev-parse", "--path-format=absolute", "--git-path", "objects")
	if err != nil {
		return "", nil, err
	}
	objectPath := strings.TrimSuffix(strings.TrimSuffix(string(objects), "\n"), "\r")
	if !filepath.IsAbs(objectPath) || strings.ContainsAny(objectPath, "\r\n") {
		return "", nil, fmt.Errorf("invalid caller object directory")
	}
	root, err := os.MkdirTemp("", "portgen-oracle-objects-")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(root) }
	if _, err := inventoryGitOutputWithExecutable(root, git, "init", "--bare", "--template=", "--initial-branch=oracle", root); err != nil {
		cleanup()
		return "", nil, err
	}
	if err := os.WriteFile(filepath.Join(root, "objects", "info", "alternates"), []byte(filepath.ToSlash(objectPath)+"\n"), 0600); err != nil {
		cleanup()
		return "", nil, err
	}
	return root, cleanup, nil
}

// CreateOracleBundle is an explicit generation operation. Verification never
// creates or relabels a record. All writes stay in the private object store.
func CreateOracleBundle(repoRoot, revision, anchor string) (*OracleBundle, error) {
	if !replayCommit.MatchString(revision) || !replayCommit.MatchString(anchor) || revision == anchor {
		return nil, fmt.Errorf("oracle bundle needs distinct full source and anchor commits")
	}
	git, err := oracleGitExecutable()
	if err != nil {
		return nil, err
	}
	refs, err := inventoryGitOutputWithExecutable(repoRoot, git, "for-each-ref", "--format=%(refname)", "refs/remotes/origin/main", "refs/heads/main")
	if err != nil {
		return nil, err
	}
	mainRef := ""
	for _, ref := range strings.Fields(string(refs)) {
		if ref == "refs/remotes/origin/main" {
			mainRef = ref
			break
		}
		if ref == "refs/heads/main" {
			mainRef = ref
		}
	}
	if mainRef == "" {
		return nil, fmt.Errorf("oracle bundle requires a verified origin/main or local main history")
	}
	for _, descendant := range []string{"HEAD", revision, mainRef} {
		if _, err := inventoryGitOutputWithExecutable(repoRoot, git, "merge-base", "--is-ancestor", anchor, descendant); err != nil {
			return nil, fmt.Errorf("oracle bundle anchor is not an ancestor: %w", err)
		}
	}
	root, cleanup, err := privateOracleStore(repoRoot, git)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	if _, err := inventoryGitOutputWithExecutable(root, git, "update-ref", "HEAD", revision); err != nil {
		return nil, err
	}
	path := filepath.Join(root, "source.bundle")
	if _, err := inventoryGitOutputWithExecutable(root, git, "bundle", "create", "--version=2", path, "HEAD", "^"+anchor); err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Size() > maxOracleRecordBytes {
		return nil, fmt.Errorf("oracle bundle exceeds %d bytes", maxOracleRecordBytes)
	}
	//nolint:gosec // path is the fixed bundle name inside the newly owned 0700 private store.
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(data)
	return &OracleBundle{Anchor: anchor, SHA256: hex.EncodeToString(hash[:]), Data: data}, nil
}

func recordedOracle(repoRoot, git, head string) (*ProvenanceDocument, error) {
	entry, err := inventoryGitOutputWithExecutable(repoRoot, git, "ls-tree", head, "--", oracleProvenancePath)
	if err != nil {
		return nil, err
	}
	if len(entry) == 0 {
		return nil, nil
	}
	parts := strings.Fields(string(entry))
	if len(parts) != 4 || parts[0] != "100644" || parts[1] != "blob" || !replayCommit.MatchString(parts[2]) || parts[3] != oracleProvenancePath {
		return nil, fmt.Errorf("oracle record must be a regular tracked blob")
	}
	sizeText, err := inventoryGitOutputWithExecutable(repoRoot, git, "cat-file", "-s", parts[2])
	if err != nil {
		return nil, err
	}
	size, err := strconv.ParseInt(strings.TrimSpace(string(sizeText)), 10, 64)
	if err != nil || size < 0 || size > maxOracleRecordBytes {
		return nil, fmt.Errorf("oracle record exceeds its byte bound")
	}
	data, err := inventoryGitOutputWithExecutable(repoRoot, git, "cat-file", "blob", parts[2])
	if err != nil {
		return nil, err
	}
	var record ProvenanceDocument
	if err := json.Unmarshal(data, &record); err != nil {
		return nil, err
	}
	return &record, nil
}

func openRecordedOracle(repoRoot, git, head, revision string) (string, func(), bool, error) {
	record, err := recordedOracle(repoRoot, git, head)
	if err != nil {
		return "", nil, false, err
	}
	if record == nil || record.Oracle.Commit != revision || record.OracleBundle == nil {
		return repoRoot, func() {}, false, nil
	}
	proof := record.OracleBundle
	if !replayCommit.MatchString(revision) || !replayCommit.MatchString(proof.Anchor) || proof.Anchor == revision || len(proof.Data) == 0 || len(proof.Data) > maxOracleRecordBytes {
		return "", nil, false, fmt.Errorf("invalid recorded oracle bundle identity or size")
	}
	hash := sha256.Sum256(proof.Data)
	if proof.SHA256 != hex.EncodeToString(hash[:]) {
		return "", nil, false, fmt.Errorf("recorded oracle bundle checksum mismatch")
	}
	header, pack, ok := bytes.Cut(proof.Data, []byte("\n\n"))
	lines := bytes.Split(header, []byte("\n"))
	if !ok || len(lines) != 3 || string(lines[0]) != "# v2 git bundle" || !bytes.HasPrefix(lines[1], []byte("-"+proof.Anchor+" ")) || string(lines[2]) != revision+" HEAD" || !bytes.HasPrefix(pack, []byte("PACK")) {
		return "", nil, false, fmt.Errorf("oracle bundle must advertise exactly its recorded source and anchor")
	}
	if _, err := inventoryGitOutputWithExecutable(repoRoot, git, "merge-base", "--is-ancestor", proof.Anchor, head); err != nil {
		return "", nil, false, fmt.Errorf("recorded oracle anchor is outside checked history: %w", err)
	}
	root, cleanup, err := privateOracleStore(repoRoot, git)
	if err != nil {
		return "", nil, false, err
	}
	fail := func(err error) (string, func(), bool, error) { cleanup(); return "", nil, false, err }
	path := filepath.Join(root, "source.bundle")
	if err := os.WriteFile(path, proof.Data, 0600); err != nil {
		return fail(err)
	}
	if _, err := inventoryGitOutputWithExecutable(root, git, "bundle", "verify", path); err != nil {
		return fail(fmt.Errorf("verify recorded oracle bundle: %w", err))
	}
	if _, err := inventoryGitOutputWithExecutable(root, git, "-c", "transfer.fsckObjects=true", "bundle", "unbundle", path); err != nil {
		return fail(fmt.Errorf("read recorded oracle objects: %w", err))
	}
	if _, err := inventoryGitOutputWithExecutable(root, git, "merge-base", "--is-ancestor", proof.Anchor, revision); err != nil {
		return fail(fmt.Errorf("recorded source is not descended from its anchor: %w", err))
	}
	return root, cleanup, true, nil
}

// VerifyOracleBundle accepts only an explicit, immutable source record. Equal
// source bytes alone never authorize an arbitrary side-branch or future SHA.
func VerifyOracleBundle(repoRoot, head, revision string) error {
	if !replayCommit.MatchString(head) || !replayCommit.MatchString(revision) {
		return fmt.Errorf("oracle bundle verification requires full commit identities")
	}
	git, err := oracleGitExecutable()
	if err != nil {
		return err
	}
	root, cleanup, bundled, err := openRecordedOracle(repoRoot, git, head, revision)
	if err != nil {
		return err
	}
	defer cleanup()
	if !bundled {
		return fmt.Errorf("source is neither ancestral nor explicitly recorded in an oracle bundle")
	}
	pinned, err := computeGitRevisionProductionSourceDigest(root, revision, func(args ...string) ([]byte, error) { return inventoryGitOutputWithExecutable(root, git, args...) })
	if err != nil {
		return err
	}
	checked, err := computeGitRevisionProductionSourceDigest(repoRoot, head, func(args ...string) ([]byte, error) { return inventoryGitOutputWithExecutable(repoRoot, git, args...) })
	if err != nil {
		return err
	}
	record, err := recordedOracle(repoRoot, git, head)
	if err != nil {
		return err
	}
	if checked != pinned || pinned != record.ProductionSourceDigest {
		return fmt.Errorf("recorded oracle source differs from checked production bytes")
	}
	return nil
}

// VerifyOracleSourceAt keeps normal ancestry authoritative and verifies any
// recorded bundle before accepting its alternative source-preservation route.
func VerifyOracleSourceAt(repoRoot, head, revision string) error {
	if !replayCommit.MatchString(head) || !replayCommit.MatchString(revision) {
		return fmt.Errorf("oracle source verification requires full commit identities")
	}
	git, err := oracleGitExecutable()
	if err != nil {
		return err
	}
	record, err := recordedOracle(repoRoot, git, head)
	if err != nil {
		return err
	}
	if record != nil && record.Oracle.Commit == revision && record.OracleBundle != nil {
		return VerifyOracleBundle(repoRoot, head, revision)
	}
	if _, err := inventoryGitOutputWithExecutable(repoRoot, git, "merge-base", "--is-ancestor", revision, head); err != nil {
		return fmt.Errorf("oracle source is neither ancestral nor explicitly recorded: %w", err)
	}
	return nil
}

// CloneOracleSource materializes a disposable, exact source checkout. Only the
// new clone receives bundled objects; the caller's refs and object store remain
// untouched. Its shared alternate points at the caller, never at a temporary
// private store that cleanup would invalidate.
func CloneOracleSource(repoRoot, revision, destination string) error {
	var err error
	destination, err = filepath.Abs(destination)
	if err != nil {
		return err
	}
	caller, err := filepath.Abs(repoRoot)
	if err != nil {
		return err
	}
	caller, err = filepath.EvalSymlinks(caller)
	if err != nil {
		return err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(destination))
	if err != nil {
		return fmt.Errorf("resolve oracle source destination parent: %w", err)
	}
	destination = filepath.Join(parent, filepath.Base(destination))
	relative, err := filepath.Rel(caller, destination)
	if err != nil && strings.EqualFold(filepath.VolumeName(caller), filepath.VolumeName(destination)) {
		return err
	}
	if err == nil && filepath.IsLocal(relative) {
		return fmt.Errorf("oracle source destination must be outside the caller checkout")
	}
	git, err := oracleGitExecutable()
	if err != nil {
		return err
	}
	headBytes, err := inventoryGitOutputWithExecutable(repoRoot, git, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	head := strings.TrimSpace(string(headBytes))
	if err := VerifyOracleSourceAt(repoRoot, head, revision); err != nil {
		return err
	}
	root, cleanup, bundled, err := openRecordedOracle(repoRoot, git, head, revision)
	if err != nil {
		return err
	}
	defer cleanup()
	if err := os.Mkdir(destination, 0700); err != nil {
		return fmt.Errorf("create owned oracle source destination: %w", err)
	}
	if _, err := inventoryGitOutputWithExecutable(repoRoot, git, "clone", "--shared", "--no-checkout", "--template=", "--", repoRoot, destination); err != nil {
		return err
	}
	if bundled {
		if _, err := inventoryGitOutputWithExecutable(root, git, "update-ref", "HEAD", revision); err != nil {
			return err
		}
		if _, err := inventoryGitOutputWithExecutable(destination, git, "fetch", "--no-tags", "--no-write-fetch-head", "--", root, revision); err != nil {
			return err
		}
	}
	_, err = inventoryGitOutputWithExecutable(destination, git, "checkout", "--detach", revision)
	return err
}

func oracleRevisionDigest(repoRoot, git, revision string) (string, error) {
	root, cleanup, _, err := openRecordedOracle(repoRoot, git, "HEAD", revision)
	if err != nil {
		return "", err
	}
	defer cleanup()
	return computeGitRevisionProductionSourceDigest(root, revision, func(args ...string) ([]byte, error) { return inventoryGitOutputWithExecutable(root, git, args...) })
}

// OracleGitOutput supports source-reading generators after the original source
// branch is gone. The supplied args are fixed Git reads owned by those callers.
func OracleGitOutput(repoRoot, revision string, args ...string) ([]byte, error) {
	git, err := oracleGitExecutable()
	if err != nil {
		return nil, err
	}
	root, cleanup, _, err := openRecordedOracle(repoRoot, git, "HEAD", revision)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	return inventoryGitOutputWithExecutable(root, git, args...)
}
