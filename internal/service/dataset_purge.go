package service

import (
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/danieljustus/symaira-desktop/internal/dataset"
	"github.com/danieljustus/symaira-desktop/internal/dbviews"
	"github.com/danieljustus/symaira-desktop/internal/history"
	"github.com/danieljustus/symaira-desktop/internal/retention"
)

const (
	datasetPurgeJournalDir       = ".symdesk/dataset-purge"
	datasetPurgePhaseSidecar     = "sidecar"
	datasetPurgePhaseActive      = "active"
	datasetPurgePhaseRecovery    = "recovery"
	datasetPurgePhaseComplete    = "complete"
	datasetPurgeStatusInProgress = "in_progress"
	datasetPurgeStatusFailed     = "failed"
	datasetPurgeStatusCompleted  = "completed"
)

// DatasetPurgeRemove is injectable so removal failures can be tested without
// weakening the production confinement provided by os.Root.
var DatasetPurgeRemove = func(root *os.Root, relPath string) error {
	return root.Remove(relPath)
}

type datasetPurgePath struct {
	RelPath     string `json:"path"`
	Kind        string `json:"kind"`
	Identity    string `json:"identity"`
	ContentHash string `json:"content_hash"`
}

type datasetPurgeTrash struct {
	Name             string `json:"name"`
	OriginalPath     string `json:"original_path"`
	PayloadIdentity  string `json:"payload_identity"`
	PayloadHash      string `json:"payload_hash"`
	MetadataIdentity string `json:"metadata_identity"`
	MetadataHash     string `json:"metadata_hash"`
}

type datasetPurgeJournal struct {
	Version      int                 `json:"version"`
	Slug         string              `json:"slug"`
	AcceptedRule string              `json:"accepted_rule"`
	Fingerprint  string              `json:"fingerprint"`
	Paths        []datasetPurgePath  `json:"paths"`
	Trash        []datasetPurgeTrash `json:"trash,omitempty"`
	Phase        string              `json:"phase"`
	Status       string              `json:"status"`
	LastError    string              `json:"last_error,omitempty"`
}

// DatasetPurge permanently removes one dataset after a caller has reviewed
// and accepted the named retention rule. It resumes an incomplete purge from
// its durable journal and never treats a recreated path as the old file.
func (s *Service) DatasetPurge(slug, acceptedRule string) error {
	return s.DatasetPurgeWithFingerprint(slug, acceptedRule, "")
}

// DatasetPurgeWithFingerprint is the acceptance-bound form used by retention.
// An empty fingerprint is accepted only for direct callers; the first start
// records the authoritative fingerprint before destructive work begins.
func (s *Service) DatasetPurgeWithFingerprint(slug, acceptedRule, fingerprint string) error {
	if s == nil || strings.TrimSpace(s.VaultRoot) == "" || s.DB == nil {
		return errors.New("dataset purge requires a vault and sidecar")
	}
	slug = strings.TrimSpace(slug)
	if slug == "" || slug != filepath.Base(slug) || slug != dbviews.Slugify(slug) {
		return fmt.Errorf("dataset slug %q is not filesystem-safe", slug)
	}
	acceptedRule = strings.TrimSpace(acceptedRule)
	if acceptedRule == "" {
		return fmt.Errorf("dataset purge %q requires an accepted retention rule", slug)
	}

	root, err := os.OpenRoot(s.VaultRoot)
	if err != nil {
		return fmt.Errorf("open vault root: %w", err)
	}
	defer func() { _ = root.Close() }()

	journal, journalErr := loadDatasetPurgeJournal(root, slug)
	if journalErr != nil && !os.IsNotExist(journalErr) {
		return journalErr
	}
	if journalErr == nil {
		if journal.Slug != slug || journal.AcceptedRule != acceptedRule {
			return fmt.Errorf("dataset purge journal does not match requested dataset or rule")
		}
		if fingerprint != "" && journal.Fingerprint != fingerprint {
			return fmt.Errorf("dataset purge fingerprint does not match durable journal")
		}
		if journal.Status == datasetPurgeStatusCompleted || journal.Phase == datasetPurgePhaseComplete {
			if err := removeDatasetPurgeJournal(root, slug); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("clean completed dataset purge journal: %w", err)
			}
			return nil
		}
		return s.resumeDatasetPurge(root, journal)
	}

	plan, err := s.preflightDatasetPurge(root, slug, acceptedRule, fingerprint)
	if err != nil {
		return err
	}
	journal = plan
	if err := writeDatasetPurgeJournal(root, journal); err != nil {
		return fmt.Errorf("persist dataset purge journal: %w", err)
	}
	return s.resumeDatasetPurge(root, journal)
}

// preflightDatasetPurge performs all reads and validation before the journal
// is created. In particular, recovery metadata is checked before any sidecar
// or active-vault mutation.
func (s *Service) preflightDatasetPurge(root *os.Root, slug, acceptedRule, acceptedFingerprint string) (*datasetPurgeJournal, error) {
	datasetsRoot, err := openVerifiedDatasetDir(root, dataset.RawDir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = datasetsRoot.Close() }()

	handleName := slug + ".md"
	handleRel := filepath.ToSlash(filepath.Join(dataset.RawDir, handleName))
	handleInfo, err := datasetsRoot.Lstat(handleName)
	if err != nil {
		return nil, fmt.Errorf("dataset purge %q: %w", slug, err)
	}
	if handleInfo.Mode()&os.ModeSymlink != 0 || !handleInfo.Mode().IsRegular() {
		return nil, fmt.Errorf("dataset handle %s must be a regular file, not a symlink", handleRel)
	}
	handleData, err := datasetsRoot.ReadFile(handleName)
	if err != nil {
		return nil, fmt.Errorf("read dataset handle %s: %w", handleRel, err)
	}
	handle, err := dataset.ParseHandle(handleRel, handleData)
	if err != nil {
		return nil, fmt.Errorf("dataset purge %q: %w", slug, err)
	}
	if handle.Slug != slug {
		return nil, fmt.Errorf("dataset handle %s identifies %q, not %q", handleRel, handle.Slug, slug)
	}
	if handle.RetentionRule != acceptedRule {
		return nil, fmt.Errorf("dataset %q declares retention rule %q, not accepted rule %q", slug, handle.RetentionRule, acceptedRule)
	}

	paths := []datasetPurgePath{{
		RelPath: handleRel, Kind: "file", Identity: purgePathIdentity(handleInfo), ContentHash: purgeHash(handleData),
	}}
	sources := make([]retention.RawSource, 0)
	rawInfo, rawErr := datasetsRoot.Lstat(slug)
	if rawErr != nil && !os.IsNotExist(rawErr) {
		return nil, rawErr
	}
	if rawErr == nil {
		if rawInfo.Mode()&os.ModeSymlink != 0 || !rawInfo.IsDir() {
			return nil, fmt.Errorf("dataset raw path %s/%s must be a directory, not a symlink", dataset.RawDir, slug)
		}
		rawRoot, err := openVerifiedDatasetDir(datasetsRoot, slug)
		if err != nil {
			return nil, err
		}
		defer func() { _ = rawRoot.Close() }()
		entries, err := fs.ReadDir(rawRoot.FS(), ".")
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			info, err := rawRoot.Lstat(entry.Name())
			if err != nil {
				return nil, err
			}
			if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
				return nil, fmt.Errorf("dataset raw directory contains non-regular entry %q", entry.Name())
			}
			data, err := rawRoot.ReadFile(entry.Name())
			if err != nil {
				return nil, err
			}
			rel := filepath.ToSlash(filepath.Join(dataset.RawDir, slug, entry.Name()))
			paths = append(paths, datasetPurgePath{RelPath: rel, Kind: "file", Identity: purgePathIdentity(info), ContentHash: purgeHash(data)})
			if strings.EqualFold(filepath.Ext(entry.Name()), ".csv") {
				sources = append(sources, retention.RawSource{Path: rel, Data: data})
			}
		}
		// The directory is removed only after all of its recorded children.
		paths = append(paths, datasetPurgePath{RelPath: filepath.ToSlash(filepath.Join(dataset.RawDir, slug)), Kind: "dir", Identity: purgePathIdentity(rawInfo)})
	}

	sortPurgeSources(sources)
	authoritativeFingerprint := retention.Fingerprint(handleData, sources)
	if acceptedFingerprint != "" && authoritativeFingerprint != acceptedFingerprint {
		return nil, fmt.Errorf("dataset purge proposal is stale: authoritative fingerprint changed")
	}
	store := s.History
	if store == nil {
		store = history.NewStore(s.VaultRoot)
	}
	trashEntries, err := store.TrashListStrict()
	if err != nil {
		return nil, fmt.Errorf("preflight dataset purge trash: %w", err)
	}
	trash := make([]datasetPurgeTrash, 0)
	prefix := filepath.ToSlash(filepath.Join(dataset.RawDir, slug)) + "/"
	for _, entry := range trashEntries {
		rel := filepath.ToSlash(entry.OriginalPath)
		if rel != handleRel && !strings.HasPrefix(rel, prefix) {
			continue
		}
		record, err := snapshotPurgeTrash(root, entry)
		if err != nil {
			return nil, err
		}
		trash = append(trash, record)
	}
	allPaths := purgePathNames(paths)
	for _, entry := range trash {
		allPaths = append(allPaths, entry.OriginalPath)
	}
	if err := store.PreflightPurgePaths(allPaths...); err != nil {
		return nil, fmt.Errorf("preflight dataset purge history: %w", err)
	}
	return &datasetPurgeJournal{
		Version: 1, Slug: slug, AcceptedRule: acceptedRule, Fingerprint: authoritativeFingerprint,
		Paths: paths, Trash: trash, Phase: datasetPurgePhaseSidecar, Status: datasetPurgeStatusInProgress,
	}, nil
}

func (s *Service) resumeDatasetPurge(root *os.Root, journal *datasetPurgeJournal) error {
	if journal.Phase == datasetPurgePhaseSidecar {
		if err := s.DB.DeleteDataset(journal.Slug); err != nil {
			return s.failDatasetPurge(root, journal, err)
		}
		handleRel := filepath.ToSlash(filepath.Join(dataset.RawDir, journal.Slug+".md"))
		if err := s.DeleteDocument(filepath.Join(s.VaultRoot, filepath.FromSlash(handleRel))); err != nil {
			return s.failDatasetPurge(root, journal, fmt.Errorf("deindex dataset handle: %w", err))
		}
		journal.Phase, journal.Status, journal.LastError = datasetPurgePhaseActive, datasetPurgeStatusInProgress, ""
		if err := writeDatasetPurgeJournal(root, journal); err != nil {
			return fmt.Errorf("persist dataset purge progress: %w", err)
		}
	}
	if journal.Phase == datasetPurgePhaseActive {
		for _, path := range journal.Paths {
			if err := removeRecordedPurgePath(root, path); err != nil {
				return s.failDatasetPurge(root, journal, err)
			}
		}
		journal.Phase, journal.Status, journal.LastError = datasetPurgePhaseRecovery, datasetPurgeStatusInProgress, ""
		if err := writeDatasetPurgeJournal(root, journal); err != nil {
			return fmt.Errorf("persist dataset purge progress: %w", err)
		}
	}
	if journal.Phase == datasetPurgePhaseRecovery {
		store := s.History
		if store == nil {
			store = history.NewStore(s.VaultRoot)
		}
		paths := purgeDatasetPaths(journal)
		if err := validatePurgeTrash(root, journal.Trash); err != nil {
			return s.failDatasetPurge(root, journal, err)
		}
		if err := store.PurgePaths(paths...); err != nil {
			return s.failDatasetPurge(root, journal, fmt.Errorf("purge dataset history: %w", err))
		}
		wanted := make([]history.TrashEntry, 0, len(journal.Trash))
		for _, entry := range journal.Trash {
			wanted = append(wanted, history.TrashEntry{Name: entry.Name, OriginalPath: entry.OriginalPath})
		}
		if _, err := store.PurgeTrashEntries(wanted); err != nil {
			return s.failDatasetPurge(root, journal, fmt.Errorf("purge dataset trash: %w", err))
		}
		journal.Phase, journal.Status, journal.LastError = datasetPurgePhaseComplete, datasetPurgeStatusCompleted, ""
		if err := writeDatasetPurgeJournal(root, journal); err != nil {
			return fmt.Errorf("persist completed dataset purge: %w", err)
		}
	}
	if journal.Phase != datasetPurgePhaseComplete {
		return fmt.Errorf("invalid dataset purge journal phase %q", journal.Phase)
	}
	if err := removeDatasetPurgeJournal(root, journal.Slug); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("clean completed dataset purge journal: %w", err)
	}
	return nil
}

func (s *Service) failDatasetPurge(root *os.Root, journal *datasetPurgeJournal, cause error) error {
	journal.Status = datasetPurgeStatusFailed
	journal.LastError = cause.Error()
	if err := writeDatasetPurgeJournal(root, journal); err != nil {
		return fmt.Errorf("%w (also failed to persist purge journal: %v)", cause, err)
	}
	return cause
}

func removeRecordedPurgePath(root *os.Root, path datasetPurgePath) error {
	info, err := root.Lstat(filepath.FromSlash(path.RelPath))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("inspect purge path %s: %w", path.RelPath, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || (path.Kind == "file" && !info.Mode().IsRegular()) || (path.Kind == "dir" && !info.IsDir()) {
		return fmt.Errorf("purge path %s changed type", path.RelPath)
	}
	if purgePathIdentity(info) != path.Identity {
		return fmt.Errorf("purge path %s was replaced", path.RelPath)
	}
	if path.Kind == "file" {
		data, err := root.ReadFile(filepath.FromSlash(path.RelPath))
		if err != nil {
			return fmt.Errorf("read purge path %s: %w", path.RelPath, err)
		}
		if purgeHash(data) != path.ContentHash {
			return fmt.Errorf("purge path %s content changed", path.RelPath)
		}
	}
	if err := DatasetPurgeRemove(root, filepath.FromSlash(path.RelPath)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove purge path %s: %w", path.RelPath, err)
	}
	return nil
}

func purgePathNames(paths []datasetPurgePath) []string {
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		out = append(out, path.RelPath)
	}
	return out
}

func snapshotPurgeTrash(root *os.Root, entry history.TrashEntry) (datasetPurgeTrash, error) {
	payloadPath := filepath.Join(".symdesk", "trash", entry.Name)
	metadataPath := payloadPath + ".trashinfo.json"
	payloadInfo, err := root.Lstat(payloadPath)
	if err != nil || payloadInfo.Mode()&os.ModeSymlink != 0 || !payloadInfo.Mode().IsRegular() {
		return datasetPurgeTrash{}, fmt.Errorf("invalid dataset trash payload %q", entry.Name)
	}
	payload, err := root.ReadFile(payloadPath)
	if err != nil {
		return datasetPurgeTrash{}, err
	}
	metadataInfo, err := root.Lstat(metadataPath)
	if err != nil || metadataInfo.Mode()&os.ModeSymlink != 0 || !metadataInfo.Mode().IsRegular() {
		return datasetPurgeTrash{}, fmt.Errorf("invalid dataset trash metadata %q", entry.Name)
	}
	metadata, err := root.ReadFile(metadataPath)
	if err != nil {
		return datasetPurgeTrash{}, err
	}
	return datasetPurgeTrash{
		Name: entry.Name, OriginalPath: filepath.ToSlash(entry.OriginalPath),
		PayloadIdentity: purgePathIdentity(payloadInfo), PayloadHash: purgeHash(payload),
		MetadataIdentity: purgePathIdentity(metadataInfo), MetadataHash: purgeHash(metadata),
	}, nil
}

func validatePurgeTrash(root *os.Root, records []datasetPurgeTrash) error {
	for _, record := range records {
		for _, item := range []struct{ path, identity, hash string }{
			{filepath.Join(".symdesk", "trash", record.Name), record.PayloadIdentity, record.PayloadHash},
			{filepath.Join(".symdesk", "trash", record.Name+".trashinfo.json"), record.MetadataIdentity, record.MetadataHash},
		} {
			info, err := root.Lstat(item.path)
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return fmt.Errorf("inspect dataset trash %s: %w", record.Name, err)
			}
			if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || purgePathIdentity(info) != item.identity {
				return fmt.Errorf("dataset trash %s was replaced", record.Name)
			}
			data, err := root.ReadFile(item.path)
			if err != nil {
				return err
			}
			if purgeHash(data) != item.hash {
				return fmt.Errorf("dataset trash %s content changed", record.Name)
			}
		}
	}
	return nil
}

func purgeDatasetPaths(journal *datasetPurgeJournal) []string {
	paths := purgePathNames(journal.Paths)
	for _, entry := range journal.Trash {
		paths = append(paths, entry.OriginalPath)
	}
	return paths
}

func isPurgeHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func purgeHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func purgePathIdentity(info fs.FileInfo) string {
	if sys := info.Sys(); sys != nil {
		value := reflect.ValueOf(sys)
		if value.Kind() == reflect.Pointer {
			value = value.Elem()
		}
		if value.IsValid() && value.Kind() == reflect.Struct {
			dev, ino := value.FieldByName("Dev"), value.FieldByName("Ino")
			if dev.IsValid() && ino.IsValid() && dev.CanInterface() && ino.CanInterface() {
				return fmt.Sprintf("%v:%v", dev.Interface(), ino.Interface())
			}
		}
	}
	return fmt.Sprintf("%s:%d:%d", info.Mode(), info.Size(), info.ModTime().UnixNano())
}

func sortPurgeSources(sources []retention.RawSource) {
	for i := 0; i < len(sources); i++ {
		for j := i + 1; j < len(sources); j++ {
			if sources[j].Path < sources[i].Path {
				sources[i], sources[j] = sources[j], sources[i]
			}
		}
	}
}

func datasetPurgeJournalPath(slug string) string {
	return filepath.Join(datasetPurgeJournalDir, slug+".json")
}

func loadDatasetPurgeJournal(root *os.Root, slug string) (*datasetPurgeJournal, error) {
	path := datasetPurgeJournalPath(slug)
	info, err := root.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("dataset purge journal is not a regular file")
	}
	data, err := root.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var journal datasetPurgeJournal
	if err := json.Unmarshal(data, &journal); err != nil {
		return nil, fmt.Errorf("corrupt dataset purge journal: %w", err)
	}
	if journal.Version != 1 || journal.Slug != slug || journal.AcceptedRule == "" || !isPurgeHash(journal.Fingerprint) || len(journal.Paths) == 0 {
		return nil, fmt.Errorf("invalid dataset purge journal")
	}
	if journal.Status != datasetPurgeStatusInProgress && journal.Status != datasetPurgeStatusFailed && journal.Status != datasetPurgeStatusCompleted {
		return nil, fmt.Errorf("invalid dataset purge journal status %q", journal.Status)
	}
	if journal.Phase != datasetPurgePhaseSidecar && journal.Phase != datasetPurgePhaseActive && journal.Phase != datasetPurgePhaseRecovery && journal.Phase != datasetPurgePhaseComplete {
		return nil, fmt.Errorf("invalid dataset purge journal phase %q", journal.Phase)
	}
	handlePath := filepath.ToSlash(filepath.Join(dataset.RawDir, slug+".md"))
	rawPrefix := filepath.ToSlash(filepath.Join(dataset.RawDir, slug)) + "/"
	rawDirPath := strings.TrimSuffix(rawPrefix, "/")
	seen := make(map[string]bool, len(journal.Paths))
	foundHandle := false
	for _, path := range journal.Paths {
		rel := filepath.ToSlash(filepath.Clean(path.RelPath))
		if rel != path.RelPath || filepath.IsAbs(path.RelPath) || seen[path.RelPath] || path.Identity == "" {
			return nil, fmt.Errorf("invalid dataset purge journal path %q", path.RelPath)
		}
		seen[path.RelPath] = true
		switch {
		case path.RelPath == handlePath:
			if path.Kind != "file" || !isPurgeHash(path.ContentHash) {
				return nil, fmt.Errorf("invalid dataset purge handle record")
			}
			foundHandle = true
		case path.RelPath == rawDirPath:
			if path.Kind != "dir" || path.ContentHash != "" {
				return nil, fmt.Errorf("invalid dataset purge raw directory record")
			}
		case strings.HasPrefix(path.RelPath, rawPrefix):
			child := strings.TrimPrefix(path.RelPath, rawPrefix)
			if path.Kind != "file" || child == "" || strings.Contains(child, "/") || !isPurgeHash(path.ContentHash) {
				return nil, fmt.Errorf("invalid dataset purge raw path %q", path.RelPath)
			}
		default:
			return nil, fmt.Errorf("dataset purge journal path %q is outside dataset", path.RelPath)
		}
	}
	for _, entry := range journal.Trash {
		if entry.Name == "" || strings.ContainsAny(entry.Name, "/\\") || entry.OriginalPath == "" || entry.PayloadIdentity == "" || entry.MetadataIdentity == "" || !isPurgeHash(entry.PayloadHash) || !isPurgeHash(entry.MetadataHash) {
			return nil, fmt.Errorf("invalid dataset purge trash record")
		}
		rel := filepath.ToSlash(filepath.Clean(entry.OriginalPath))
		if rel != entry.OriginalPath || filepath.IsAbs(entry.OriginalPath) {
			return nil, fmt.Errorf("invalid dataset purge trash path %q", entry.OriginalPath)
		}
		if entry.OriginalPath != handlePath && !strings.HasPrefix(entry.OriginalPath, rawPrefix) {
			return nil, fmt.Errorf("dataset purge trash path %q is outside dataset", entry.OriginalPath)
		}
		if entry.OriginalPath != handlePath {
			child := strings.TrimPrefix(entry.OriginalPath, rawPrefix)
			if child == "" || strings.Contains(child, "/") {
				return nil, fmt.Errorf("invalid dataset purge trash path %q", entry.OriginalPath)
			}
		}
	}
	if !foundHandle {
		return nil, fmt.Errorf("dataset purge journal does not record its handle")
	}
	return &journal, nil
}

func writeDatasetPurgeJournal(root *os.Root, journal *datasetPurgeJournal) error {
	if err := root.MkdirAll(datasetPurgeJournalDir, 0750); err != nil {
		return err
	}
	data, err := json.MarshalIndent(journal, "", "  ")
	if err != nil {
		return err
	}
	for range 100 {
		random := make([]byte, 12)
		if _, err := cryptorand.Read(random); err != nil {
			return err
		}
		tmpPath := filepath.Join(datasetPurgeJournalDir, ".journal-"+hex.EncodeToString(random)+".tmp")
		file, err := root.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			if os.IsExist(err) {
				continue
			}
			return err
		}
		cleanup := func() { _ = file.Close(); _ = root.Remove(tmpPath) }
		if _, err := file.Write(data); err != nil {
			cleanup()
			return err
		}
		if err := file.Sync(); err != nil {
			cleanup()
			return err
		}
		if err := file.Close(); err != nil {
			_ = root.Remove(tmpPath)
			return err
		}
		if err := root.Rename(tmpPath, datasetPurgeJournalPath(journal.Slug)); err != nil {
			_ = root.Remove(tmpPath)
			return err
		}
		return nil
	}
	return fmt.Errorf("create dataset purge journal temporary file: too many collisions")
}

func removeDatasetPurgeJournal(root *os.Root, slug string) error {
	return root.Remove(datasetPurgeJournalPath(slug))
}

func openVerifiedDatasetDir(parent *os.Root, name string) (*os.Root, error) {
	before, err := parent.Lstat(name)
	if err != nil {
		return nil, err
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.IsDir() {
		return nil, fmt.Errorf("dataset purge path %q must be a directory, not a symlink", name)
	}
	child, err := parent.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	after, err := child.Stat(".")
	if err != nil {
		_ = child.Close()
		return nil, err
	}
	if !os.SameFile(before, after) {
		_ = child.Close()
		return nil, fmt.Errorf("dataset purge path %q changed during verification", name)
	}
	return child, nil
}
