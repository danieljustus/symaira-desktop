package selfhost

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type commandRequest struct {
	Arguments []string `json:"arguments"`
	Stdin     string   `json:"stdin,omitempty"`
}

// streamingCommands emit newline-delimited JSON incrementally as an LLM
// streams tokens (see cmd/symdesk's outputStream), rather than one JSON
// document at completion. Their HTTP response is streamed line-by-line
// instead of buffered, so a client sees partial output as it is produced.
var streamingCommands = map[string]bool{"ask": true, "transform": true}

// subprocessEnvExclude lists environment variables that authenticate the
// server process itself and must never reach a remotely spawned symdesk
// subprocess. The remote command allowlist (validateRemoteCommand) limits
// which commands can run, but does not stop a future command from echoing
// environment state back to an authenticated client, so these are stripped
// unconditionally regardless of which command is invoked.
var subprocessEnvExclude = map[string]bool{
	"SYMDESK_SERVER_TOKEN": true,
	"SYMDESK_WORKER_TOKEN": true,
}

// subprocessEnv builds the environment for a remotely spawned symdesk
// subprocess: a filtered copy of the server's own environment with the
// server/worker auth tokens removed, plus SYMDESK_SIDECAR pointing at this
// server's index database. Filtering (rather than allow-listing from
// scratch) keeps every other variable the allowed commands need — PATH,
// HOME, and the SYMDESK_LLM_*/SYMDESK_OLLAMA_*/SYMDESK_ANTHROPIC_URL
// variables that `ask`/`transform` read via internal/ai and internal/config
// — working exactly as before, while ensuring the credentials that
// authenticate this server can never leak into a subprocess whose arguments
// an already-authenticated remote client controls.
func (s *Server) subprocessEnv() []string {
	parent := os.Environ()
	env := make([]string, 0, len(parent)+1)
	for _, kv := range parent {
		name, _, found := strings.Cut(kv, "=")
		if found && subprocessEnvExclude[name] {
			continue
		}
		env = append(env, kv)
	}
	return append(env, "SYMDESK_SIDECAR="+filepath.Join(s.cfg.VaultRoot, ".symdesk", "server", "sidecar.db"))
}

func (s *Server) handleCommand(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	var request commandRequest
	if err := decodeJSON(r, &request, 2<<20); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := validateRemoteCommand(request.Arguments); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	args := append([]string{}, request.Arguments...)
	if !containsArg(args, "--json") {
		args = append(args, "--json")
	}
	args = append(args, "--vault", s.cfg.VaultRoot)
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.cfg.Executable, args...) //nolint:gosec // G204: s.cfg.Executable is server-configured, and args was already checked by validateRemoteCommand above
	cmd.Env = s.subprocessEnv()
	cmd.Stdin = strings.NewReader(request.Stdin)

	if streamingCommands[args[0]] {
		s.streamCommand(w, cmd)
		return
	}

	out := &limitedBuffer{limit: maxCommandBytes}
	errOut := &limitedBuffer{limit: 1 << 20}
	cmd.Stdout = out
	cmd.Stderr = errOut
	err := cmd.Run()
	if errors.Is(out.err, errLimitExceeded) {
		writeError(w, http.StatusRequestEntityTooLarge, "command output exceeded 32 MiB")
		return
	}
	if err != nil {
		message := strings.TrimSpace(errOut.String())
		if message == "" {
			message = err.Error()
		}
		writeError(w, http.StatusUnprocessableEntity, message)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out.Bytes())
}

// streamCommand runs cmd and relays its stdout to w one NDJSON line at a
// time, flushing after every line so the client observes output as the
// subprocess produces it. Once the 200 status and first bytes are written,
// errors can no longer change the HTTP status; a failure is instead reported
// as a trailing NDJSON error line so the client can distinguish it from a
// normal event. Cancellation (client disconnect or the 5-minute timeout)
// flows through cmd's context and kills the subprocess.
func (s *Server) streamCommand(w http.ResponseWriter, cmd *exec.Cmd) {
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	errOut := &limitedBuffer{limit: 1 << 20}
	cmd.Stderr = errOut
	if err := cmd.Start(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)
	flusher, canFlush := w.(http.Flusher)

	reader := bufio.NewReader(stdout)
	var written int64
	truncated := false
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			if !truncated {
				if written+int64(len(line)) > maxCommandBytes {
					truncated = true
					_ = cmd.Process.Kill()
				} else {
					written += int64(len(line))
					if _, writeErr := w.Write(line); writeErr != nil {
						_ = cmd.Process.Kill()
						_ = cmd.Wait()
						return
					}
					if canFlush {
						flusher.Flush()
					}
				}
			}
		}
		if readErr != nil {
			break
		}
	}

	runErr := cmd.Wait()
	var event map[string]string
	switch {
	case truncated:
		event = map[string]string{"type": "error", "message": "command output exceeded 32 MiB"}
	case runErr != nil:
		message := strings.TrimSpace(errOut.String())
		if message == "" {
			message = runErr.Error()
		}
		event = map[string]string{"type": "error", "message": message}
	default:
		return
	}
	line, err := json.Marshal(event)
	if err != nil {
		return
	}
	_, _ = w.Write(append(line, '\n'))
	if canFlush {
		flusher.Flush()
	}
}

var allowedRemoteCommands = map[string]map[string]bool{
	"doctor": {"": true}, "ls": {"": true}, "search": {"": true}, "backlinks": {"": true},
	"graph": {"": true}, "similar": {"": true}, "duplicates": {"": true}, "transform": {"": true}, "ask": {"": true},
	"note":      {"new": true, "move": true, "delete": true, "daily": true},
	"paperless": {"import": true},
	"props":     {"get": true, "edit": true}, "relations": {"inverse": true},
	"views":    {"list": true, "get": true, "save": true, "delete": true, "new-entry": true, "siblings": true, "exec": true},
	"docs":     {"list": true, "review": true},
	"doc":      {"status": true, "due": true, "type": true, "correspondent": true, "tag": true, "asn": true},
	"tags":     {"rename": true, "merge": true, "delete": true},
	"conflict": {"resolve": true}, "history": {"": true, "prune": true, "show": true}, "restore": {"": true},
	"trash": {"list": true, "restore": true, "delete": true},
}

func validateRemoteCommand(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("command is required")
	}
	for _, arg := range args {
		lower := strings.ToLower(arg)
		if lower == "--vault" || strings.HasPrefix(lower, "--vault=") || lower == "--output" || strings.HasPrefix(lower, "--output=") {
			return fmt.Errorf("server-controlled path flags are not allowed")
		}
	}
	subcommands, ok := allowedRemoteCommands[args[0]]
	if !ok {
		return fmt.Errorf("command %q is not available remotely", args[0])
	}
	if subcommands[""] {
		return nil
	}
	sub := ""
	if len(args) > 1 && !strings.HasPrefix(args[1], "-") {
		sub = args[1]
	}
	if !subcommands[sub] {
		return fmt.Errorf("subcommand %q is not available remotely", sub)
	}
	return nil
}

func containsArg(args []string, wanted string) bool {
	for _, arg := range args {
		if arg == wanted {
			return true
		}
	}
	return false
}

var errLimitExceeded = errors.New("buffer limit exceeded")

type limitedBuffer struct {
	bytes.Buffer
	limit int
	err   error
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	remaining := b.limit - b.Len()
	if remaining <= 0 {
		b.err = errLimitExceeded
		return len(p), nil
	}
	if len(p) > remaining {
		_, _ = b.Buffer.Write(p[:remaining])
		b.err = errLimitExceeded
		return len(p), nil
	}
	return b.Buffer.Write(p)
}
