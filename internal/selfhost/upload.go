package selfhost

import (
	cryptorand "crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/vault"
)

func (s *Server) handlePutFile(w http.ResponseWriter, r *http.Request) {
	rel, err := resolveRequestPath(r.URL.Query().Get("path"))
	if err != nil || strings.ToLower(filepath.Ext(rel)) != ".md" {
		writeError(w, http.StatusBadRequest, "only vault-relative Markdown files can be updated")
		return
	}
	user := userFromContext(r)
	if !s.perm.CanWrite(user, rel) {
		writeError(w, http.StatusForbidden, "access denied")
		return
	}
	data, err := readLimited(r.Body, maxNoteBytes)
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, err.Error())
		return
	}
	if err := s.vaultRoot.MkdirAll(filepath.Dir(rel), 0750); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := atomicWrite(s.vaultRoot, rel, data, 0644); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	doc, err := vault.ParseBytes(filepath.Join(s.cfg.VaultRoot, rel), data)
	if err == nil {
		err = s.db.IndexDocument(doc)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (s *Server) handleIngest(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes+(1<<20))
	if err := r.ParseMultipartForm(maxUploadBytes); err != nil { //nolint:gosec // G120: bounded by maxUploadBytes and the MaxBytesReader above
		writeError(w, http.StatusRequestEntityTooLarge, "upload exceeds 100 MiB or is invalid")
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "multipart field 'file' is required")
		return
	}
	defer func() { _ = file.Close() }()

	id, err := NewJobID()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	name := safeFilename(header.Filename)
	rel, err := resolveRequestPath(filepath.ToSlash(filepath.Join("archive", time.Now().UTC().Format("2006/01"), id+"-"+name)))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.vaultRoot.MkdirAll(filepath.Dir(rel), 0750); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := writeUpload(s.vaultRoot, rel, file); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	job, err := s.jobs.Create(rel, name, header.Header.Get("Content-Type"))
	if err != nil {
		_ = s.vaultRoot.Remove(rel)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}

func safeFilename(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.TrimSpace(name)
	if name == "." || name == "" {
		return "document.bin"
	}
	return name
}

func writeUpload(root *os.Root, path string, src multipart.File) error {
	tmp, tmpName, err := createRootTemp(root, filepath.Dir(path), ".upload-")
	if err != nil {
		return err
	}
	defer func() { _ = root.Remove(tmpName) }()
	if err := tmp.Chmod(0640); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := io.Copy(tmp, io.LimitReader(src, maxUploadBytes+1)); err != nil {
		_ = tmp.Close()
		return err
	}
	if info, err := tmp.Stat(); err != nil || info.Size() > maxUploadBytes {
		_ = tmp.Close()
		return fmt.Errorf("upload exceeds 100 MiB")
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return root.Rename(tmpName, path)
}

func atomicWrite(root *os.Root, path string, data []byte, mode os.FileMode) error {
	tmp, tmpName, err := createRootTemp(root, filepath.Dir(path), ".write-")
	if err != nil {
		return err
	}
	defer func() { _ = root.Remove(tmpName) }()
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return root.Rename(tmpName, path)
}

func createRootTemp(root *os.Root, dir, prefix string) (*os.File, string, error) {
	for range 100 {
		random := make([]byte, 12)
		if _, err := cryptorand.Read(random); err != nil {
			return nil, "", err
		}
		name := filepath.Join(dir, prefix+hex.EncodeToString(random)+".tmp")
		file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		return file, name, err
	}
	return nil, "", fmt.Errorf("create temporary file: too many collisions")
}

func readLimited(reader io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("request body exceeds limit")
	}
	return data, nil
}
