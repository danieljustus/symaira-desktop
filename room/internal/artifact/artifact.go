package artifact

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/danieljustus/symaira-room/internal/desk"
	"github.com/danieljustus/symaira-room/internal/event"
	"github.com/danieljustus/symaira-room/internal/identity"
	"github.com/danieljustus/symaira-room/internal/journal"
)

var (
	ErrOutsideRoot = errors.New("path is outside artifact root")
	ErrNotFound    = errors.New("artifact not found")
)

type ArtifactRef struct {
	ID        string `json:"id"`
	Path      string `json:"path"`
	SHA256    string `json:"sha256"`
	Title     string `json:"title,omitempty"`
	SymdeskID string `json:"symdesk_id,omitempty"`
	Status    string `json:"status,omitempty"` // "ok", "modified", "missing"
}

func ComputeSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func MakeRelativePath(targetPath, rootDir string) (string, error) {
	absTarget, err := filepath.Abs(targetPath)
	if err != nil {
		return "", err
	}
	absRoot, err := filepath.Abs(rootDir)
	if err != nil {
		return "", err
	}

	rel, err := filepath.Rel(absRoot, absTarget)
	if err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return "", ErrOutsideRoot
	}
	return rel, nil
}

func Link(roomDir, artifactRoot, filePath, title string, id *identity.Identity) (*event.Event, error) {
	if artifactRoot == "" {
		artifactRoot = roomDir
	}

	relPath, err := MakeRelativePath(filePath, artifactRoot)
	if err != nil {
		return nil, err
	}

	absPath := filepath.Join(artifactRoot, relPath)
	hash, err := ComputeSHA256(absPath)
	if err != nil {
		return nil, fmt.Errorf("compute sha256: %w", err)
	}

	artID := "art_" + journal.ComputeLineHash([]byte(relPath + hash))[7:23]
	if title == "" {
		title = filepath.Base(relPath)
	}

	symdeskID := ""
	if res, err := desk.InspectPath(context.Background(), absPath); err == nil && res != nil {
		symdeskID = res.DocumentID
	}

	bodyMap := map[string]string{
		"artifact_id": artID,
		"path":        relPath,
		"sha256":      hash,
		"title":       title,
		"symdesk_id":  symdeskID,
	}
	bodyBytes, _ := json.Marshal(bodyMap)

	j := journal.New(filepath.Join(roomDir, "journal"))
	ev := &event.Event{
		V:      event.CurrentVersion,
		ID:     "ev_" + artID[4:],
		Room:   "rm_test",
		Author: id.MemberID,
		Kind:   event.KindArtifactLinked,
		Body:   json.RawMessage(bodyBytes),
	}

	if err := j.PrepareEvent(ev); err != nil {
		return nil, err
	}
	if err := ev.Sign(id); err != nil {
		return nil, err
	}
	if err := j.Append(ev); err != nil {
		return nil, err
	}

	return ev, nil
}

func Unlink(roomDir, artID string, id *identity.Identity) (*event.Event, error) {
	bodyMap := map[string]string{
		"artifact_id": artID,
	}
	bodyBytes, _ := json.Marshal(bodyMap)

	j := journal.New(filepath.Join(roomDir, "journal"))
	ev := &event.Event{
		V:      event.CurrentVersion,
		ID:     "ev_" + journal.ComputeLineHash([]byte(artID))[7:23],
		Room:   "rm_test",
		Author: id.MemberID,
		Kind:   event.KindArtifactUnlinked,
		Body:   json.RawMessage(bodyBytes),
	}

	if err := j.PrepareEvent(ev); err != nil {
		return nil, err
	}
	if err := ev.Sign(id); err != nil {
		return nil, err
	}
	if err := j.Append(ev); err != nil {
		return nil, err
	}

	return ev, nil
}

func List(roomDir, artifactRoot string) ([]*ArtifactRef, error) {
	if artifactRoot == "" {
		artifactRoot = roomDir
	}

	j := journal.New(filepath.Join(roomDir, "journal"))
	merged, err := j.MergeAll()
	if err != nil {
		return nil, err
	}

	active := make(map[string]*ArtifactRef)

	for _, ev := range merged {
		switch ev.Kind {
		case event.KindArtifactLinked:
			var bodyMap struct {
				ArtifactID string `json:"artifact_id"`
				Path       string `json:"path"`
				SHA256     string `json:"sha256"`
				Title      string `json:"title"`
				SymdeskID  string `json:"symdesk_id"`
			}
			if err := json.Unmarshal(ev.Body, &bodyMap); err == nil {
				active[bodyMap.ArtifactID] = &ArtifactRef{
					ID:        bodyMap.ArtifactID,
					Path:      bodyMap.Path,
					SHA256:    bodyMap.SHA256,
					Title:     bodyMap.Title,
					SymdeskID: bodyMap.SymdeskID,
				}
			}
		case event.KindArtifactUnlinked:
			var bodyMap struct {
				ArtifactID string `json:"artifact_id"`
			}
			if err := json.Unmarshal(ev.Body, &bodyMap); err == nil {
				delete(active, bodyMap.ArtifactID)
			}
		}
	}

	var result []*ArtifactRef
	for _, ref := range active {
		absPath := filepath.Join(artifactRoot, ref.Path)
		currentHash, err := ComputeSHA256(absPath)
		if err != nil {
			if os.IsNotExist(err) {
				ref.Status = "missing"
			} else {
				ref.Status = "error"
			}
		} else if currentHash != ref.SHA256 {
			ref.Status = "modified"
		} else {
			ref.Status = "ok"
		}
		result = append(result, ref)
	}

	return result, nil
}

func HandleDeskEvent(roomDir, artifactRoot string, item *desk.EventStreamItem, id *identity.Identity) error {
	if artifactRoot == "" {
		artifactRoot = roomDir
	}
	activeArtifacts, err := List(roomDir, artifactRoot)
	if err != nil {
		return err
	}

	relPath, err := MakeRelativePath(item.Path, artifactRoot)
	if err != nil {
		relPath = item.Path
	}

	var targetRef *ArtifactRef
	for _, ref := range activeArtifacts {
		if ref.Path == relPath || filepath.Clean(ref.Path) == filepath.Clean(relPath) {
			targetRef = ref
			break
		}
	}

	if targetRef == nil {
		return nil
	}

	absPath := filepath.Join(artifactRoot, targetRef.Path)
	newHash, err := ComputeSHA256(absPath)
	if err != nil && item.Event != "file_removed" {
		return nil
	}

	bodyMap := map[string]string{
		"artifact_id": targetRef.ID,
		"path":        targetRef.Path,
		"sha256":      newHash,
		"event_type":  item.Event,
	}
	bodyBytes, _ := json.Marshal(bodyMap)

	j := journal.New(filepath.Join(roomDir, "journal"))
	ev := &event.Event{
		V:      event.CurrentVersion,
		ID:     "ev_" + journal.ComputeLineHash([]byte(targetRef.ID + newHash + item.Event))[7:23],
		Room:   "rm_test",
		Author: id.MemberID,
		Kind:   event.KindArtifactChanged,
		Body:   json.RawMessage(bodyBytes),
	}

	if err := j.PrepareEvent(ev); err != nil {
		return err
	}
	if err := ev.Sign(id); err != nil {
		return err
	}
	return j.Append(ev)
}
