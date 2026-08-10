package selfhost

import (
	"net/http"

	"github.com/danieljustus/symaira-desktop/internal/service"
)

// This file implements the read-only notebook endpoints (issue #428):
//
//	GET /api/v1/notebooks     — list all notebooks
//	GET /api/v1/notebooks/{id} — one notebook and its resolved sources
//
// A notebook is a named, bounded set of vault sources (VAULT.md section
// 10). Listing notebooks never exposes source content, so no permission
// filtering applies there; resolving one notebook's sources does — a
// source the caller cannot read is omitted, the same way handleAIAsk
// filters retrieval results.

// handleListNotebooks lists every notebook in the vault. Notebook metadata
// (title, description, id) carries no document content, so it is not
// permission-filtered — only a notebook's resolved sources are.
func (s *Server) handleListNotebooks(w http.ResponseWriter, r *http.Request) {
	svc := service.New(s.cfg.VaultRoot, s.db)
	list, err := svc.NotebookList()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

type notebookResponse struct {
	ID          string                `json:"id"`
	Path        string                `json:"path"`
	Title       string                `json:"title"`
	Description string                `json:"description,omitempty"`
	Created     string                `json:"created"`
	Sources     []notebookSourceEntry `json:"sources"`
}

type notebookSourceEntry struct {
	Path    string `json:"path"`
	Title   string `json:"title,omitempty"`
	Missing bool   `json:"missing,omitempty"`
}

// handleGetNotebook resolves one notebook by id and its current sources.
// A source the caller cannot read is omitted entirely rather than shown
// with redacted content — the same "never received" contract handleAIAsk
// already applies to retrieval.
func (s *Server) handleGetNotebook(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "notebook id is required")
		return
	}
	svc := service.New(s.cfg.VaultRoot, s.db)
	nb, err := svc.NotebookGet(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "notebook not found: "+err.Error())
		return
	}
	refs, err := nb.ResolveSources(svc.VaultRoot)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	user := userFromContext(r)
	paths := make([]string, len(refs))
	for i, ref := range refs {
		paths[i] = ref.Path
	}
	allowed := s.perm.CanReadMany(user, paths)
	allowedSet := make(map[string]bool, len(allowed))
	for _, p := range allowed {
		allowedSet[p] = true
	}

	sources := make([]notebookSourceEntry, 0, len(refs))
	for _, ref := range refs {
		if !allowedSet[ref.Path] {
			continue
		}
		sources = append(sources, notebookSourceEntry{Path: ref.Path, Title: ref.Title, Missing: ref.Missing})
	}

	writeJSON(w, http.StatusOK, notebookResponse{
		ID:          nb.ID,
		Path:        nb.Path,
		Title:       nb.Title,
		Description: nb.Description,
		Created:     nb.Created,
		Sources:     sources,
	})
}
