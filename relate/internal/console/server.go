// Package console implements the local web console: a localhost-only,
// authenticated HTTP server exposing an embedded vanilla HTML/JS UI over
// the same app.App service layer as the CLI and MCP server. See
// docs/CONSOLE.md.
package console

import (
	"context"
	"embed"
	"encoding/json"
	"io/fs"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/danieljustus/symaira-relate/internal/app"
	"github.com/danieljustus/symaira-relate/internal/domain/security"
	"github.com/danieljustus/symaira-relate/internal/errs"
)

// shutdownTimeout bounds how long Serve waits for in-flight requests to
// finish after ctx is cancelled before forcing the listener closed.
const shutdownTimeout = 5 * time.Second

// Server timeouts guard against slowloris-style attacks and clients that
// hold connections open without completing a request. The console is
// localhost-only, but any local process or browser tab can still abuse
// unbounded timeouts.
const (
	readHeaderTimeout = 5 * time.Second
	readTimeout       = 30 * time.Second
	writeTimeout      = 60 * time.Second
	idleTimeout       = 120 * time.Second
)

//go:embed static/*
var staticFS embed.FS

// Server is the local web console. It holds no state of its own beyond
// the session token — all data lives behind app.App, exactly like the CLI
// and MCP server.
type Server struct {
	app   *app.App
	token string
	mux   *http.ServeMux
}

// New wires a Server against an already-open App and a session token
// (see GenerateToken). The caller owns the App's lifecycle.
func New(a *app.App, token string) *Server {
	s := &Server{app: a, token: token, mux: http.NewServeMux()}
	s.registerRoutes()
	return s
}

// Handler returns the fully-wired, auth-gated HTTP handler.
func (s *Server) Handler() http.Handler {
	return s.requireAuth(s.mux)
}

// Serve runs the console on ln until ctx is cancelled, then shuts down
// gracefully. ln must be bound to 127.0.0.1 — see Listen.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	httpServer := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}

	errCh := make(chan error, 1)
	go func() { errCh <- httpServer.Serve(ln) }()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
		return ctx.Err()
	case err := <-errCh:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	}
}

func (s *Server) registerRoutes() {
	assets, err := fs.Sub(staticFS, "static")
	if err != nil {
		// staticFS is compiled in via go:embed; a missing "static"
		// subtree is a build-time programming error, not a runtime one.
		panic("console: embedded static assets missing: " + err.Error())
	}
	s.mux.Handle("GET /", http.FileServerFS(assets))

	s.mux.HandleFunc("GET /api/v1/contacts", s.handleContactsList)
	s.mux.HandleFunc("POST /api/v1/contacts", s.handleContactCreate)
	s.mux.HandleFunc("GET /api/v1/contacts/{id}", s.handleContactGet)
	s.mux.HandleFunc("PATCH /api/v1/contacts/{id}", s.handleContactUpdate)
	s.mux.HandleFunc("DELETE /api/v1/contacts/{id}", s.handleContactErase)
	s.mux.HandleFunc("GET /api/v1/contacts/{id}/timeline", s.handleContactTimeline)
	s.mux.HandleFunc("GET /api/v1/contacts/{id}/memberships", s.handleContactMemberships)

	s.mux.HandleFunc("GET /api/v1/organizations", s.handleOrganizationsList)
	s.mux.HandleFunc("POST /api/v1/organizations", s.handleOrganizationCreate)
	s.mux.HandleFunc("GET /api/v1/organizations/{id}", s.handleOrganizationGet)
	s.mux.HandleFunc("PATCH /api/v1/organizations/{id}", s.handleOrganizationUpdate)
	s.mux.HandleFunc("DELETE /api/v1/organizations/{id}", s.handleOrganizationErase)
	s.mux.HandleFunc("GET /api/v1/organizations/{id}/timeline", s.handleOrganizationTimeline)
	s.mux.HandleFunc("GET /api/v1/organizations/{id}/memberships", s.handleOrganizationMemberships)

	s.mux.HandleFunc("POST /api/v1/memberships", s.handleMembershipCreate)

	s.mux.HandleFunc("GET /api/v1/followups", s.handleFollowUpsList)
	s.mux.HandleFunc("POST /api/v1/followups", s.handleFollowUpCreate)
	s.mux.HandleFunc("POST /api/v1/followups/{id}/complete", s.handleFollowUpComplete)
	s.mux.HandleFunc("POST /api/v1/followups/{id}/cancel", s.handleFollowUpCancel)

	s.mux.HandleFunc("GET /api/v1/import/runs", s.handleImportRuns)
	s.mux.HandleFunc("POST /api/v1/import/plan", s.handleImportPlan)
	s.mux.HandleFunc("POST /api/v1/import/apply", s.handleImportApply)
}

// Listen binds a TCP listener on 127.0.0.1 only — never 0.0.0.0 or an
// empty host, which would expose the console (and, through it, every
// contact in the database) to the local network. port 0 picks an
// ephemeral free port; the caller reads the actual chosen port off the
// returned listener's Addr().
func Listen(port int) (net.Listener, error) {
	return net.Listen("tcp", loopbackAddr(port))
}

func loopbackAddr(port int) string {
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

// writeErr maps a service-layer error to an HTTP status via errs.Kind and
// writes it as JSON, redacted exactly like CLI stderr and MCP error
// responses (see docs/PRIVACY.md) — a contact-point value that reached an
// error path can never leak through the console's HTTP responses either.
func writeErr(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch errs.KindOf(err) {
	case errs.KindNotFound:
		status = http.StatusNotFound
	case errs.KindConflict:
		status = http.StatusConflict
	case errs.KindInvalid:
		status = http.StatusBadRequest
	case errs.KindPermission:
		status = http.StatusForbidden
	case errs.KindUnavailable:
		status = http.StatusServiceUnavailable
	}
	writeJSONError(w, status, security.Redact(err.Error()))
}
