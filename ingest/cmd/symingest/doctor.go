package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/danieljustus/symaira-corekit/exitcodes"
	"github.com/danieljustus/symaira-ingest/internal/config"
	"github.com/danieljustus/symaira-ingest/internal/secret"
	"github.com/danieljustus/symaira-ingest/internal/store"
	"github.com/emersion/go-imap/v2/imapclient"
)

type doctorStatus string

const (
	doctorOK   doctorStatus = "ok"
	doctorWarn doctorStatus = "warn"
	doctorFail doctorStatus = "fail"
)

type doctorCheck struct {
	Name    string       `json:"name"`
	Status  doctorStatus `json:"status"`
	Message string       `json:"message"`
}

type doctorReport struct {
	Status   doctorStatus  `json:"status"`
	Checks   []doctorCheck `json:"checks"`
	Failures int           `json:"failures"`
	Warnings int           `json:"warnings"`
}

// doctorIMAPClient is the subset of IMAP operations needed by checkIMAP.
type doctorIMAPClient interface {
	Login(username, password string) error
	Select(folder string) error
	Logout() error
}

type doctorIMAPReal struct {
	client *imapclient.Client
}

func (c *doctorIMAPReal) Login(username, password string) error {
	return c.client.Login(username, password).Wait()
}

func (c *doctorIMAPReal) Select(folder string) error {
	_, err := c.client.Select(folder, nil).Wait()
	return err
}

func (c *doctorIMAPReal) Logout() error {
	return c.client.Logout().Wait()
}

// doctorDialIMAP connects to an IMAP server over TLS. Package-level so tests
// can replace it with a fake.
var doctorDialIMAP = func(addr, host string) (doctorIMAPClient, error) {
	c, err := imapclient.DialTLS(addr, &imapclient.Options{TLSConfig: &tls.Config{ServerName: host}})
	if err != nil {
		return nil, err
	}
	return &doctorIMAPReal{client: c}, nil
}

func checkIMAP(ctx context.Context, report *doctorReport, accounts []config.IMAPAccount) {
	for i, acc := range accounts {
		name := fmt.Sprintf("imap.account.%d", i)

		if secret.IsPlaintext(acc.PasswordSecret) {
			report.add(name+".password_secret", doctorWarn, fmt.Sprintf("password for %s is stored in plaintext config; use keychain:// or symvault:// instead", acc.Username))
		}

		pwd, err := secret.Resolve(ctx, acc.PasswordSecret)
		if err != nil {
			report.add(name, doctorFail, fmt.Sprintf("cannot resolve password for %s: %v", acc.Username, err))
			continue
		}

		addr := fmt.Sprintf("%s:%d", acc.Host, acc.Port)
		client, err := doctorDialIMAP(addr, acc.Host)
		if err != nil {
			report.add(name, doctorFail, fmt.Sprintf("cannot connect to %s: %v", addr, err))
			continue
		}

		if err := client.Login(acc.Username, pwd); err != nil {
			client.Logout()
			report.add(name, doctorFail, fmt.Sprintf("login failed for %s: %v", acc.Username, err))
			continue
		}

		folder := acc.Folder
		if folder == "" {
			folder = "INBOX"
		}

		if err := client.Select(folder); err != nil {
			client.Logout()
			report.add(name, doctorFail, fmt.Sprintf("cannot select folder %s: %v", folder, err))
			continue
		}

		client.Logout()
		report.add(name, doctorOK, fmt.Sprintf("connected to %s as %s", addr, acc.Username))
	}
}

// checkMailPollStatus surfaces the last recorded poll outcome per IMAP
// account, so a stalled background poller is visible without reading the
// LaunchAgent log. This is best-effort: a store that cannot be opened yields
// no additional checks rather than failing doctor outright (checkWritableDB
// already reports a hard failure for that case).
func checkMailPollStatus(ctx context.Context, report *doctorReport, dbPath string, accounts []config.IMAPAccount) {
	if dbPath == "" {
		return
	}
	st, err := store.Open(dbPath)
	if err != nil {
		return
	}
	defer st.Close()

	for i, acc := range accounts {
		name := fmt.Sprintf("imap.account.%d.last_poll", i)
		s, err := st.GetMailPollStatus(ctx, config.AccountID(acc))
		if err != nil || s == nil {
			report.add(name, doctorOK, "never polled")
			continue
		}
		if s.Status == "ok" {
			report.add(name, doctorOK, fmt.Sprintf("%s ok", s.LastPolledAt.Format(time.RFC3339)))
		} else {
			report.add(name, doctorWarn, fmt.Sprintf("%s failed: %s", s.LastPolledAt.Format(time.RFC3339), s.LastError))
		}
	}
}

func (r *doctorReport) add(name string, status doctorStatus, message string) {
	r.Checks = append(r.Checks, doctorCheck{Name: name, Status: status, Message: message})
	switch status {
	case doctorFail:
		r.Failures++
	case doctorWarn:
		r.Warnings++
	}
	if r.Failures > 0 {
		r.Status = doctorFail
	} else if r.Warnings > 0 {
		r.Status = doctorWarn
	} else {
		r.Status = doctorOK
	}
}

func runDoctor(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	paperlessFlag := fs.Bool("paperless", false, "Check Paperless API connectivity as well")
	jsonFlag := fs.Bool("json", false, "Output a stable JSON report")
	baseURL := fs.String("base-url", "", "Paperless-ngx URL override (or PAPERLESS_URL / config)")
	token := fs.String("token", "", "Paperless api token override (or PAPERLESS_TOKEN env); never printed")
	inbox := fs.String("inbox", "", "Watch inbox directory override")
	ocrLang, vault, archive, db := registerSharedFlags(fs)
	configureUsage(fs, "doctor [flags]", "Validate local prerequisites, paths, OCR tools and optional Paperless connectivity.")
	help, err := parseFlags(fs, args, "invalid doctor flags")
	if help || err != nil {
		return err
	}
	cfg, err := resolveConfig(fs, ocrLang, vault, archive, db)
	if err != nil {
		return err
	}
	if *inbox != "" {
		cfg.inbox = *inbox
	}
	if *baseURL == "" {
		*baseURL = os.Getenv("PAPERLESS_URL")
	}
	if *baseURL == "" {
		*baseURL = cfg.paperlessBaseURL
	}
	if *token == "" {
		*token = os.Getenv("PAPERLESS_TOKEN")
	}

	report := runDoctorChecks(context.Background(), cfg, *paperlessFlag, *baseURL, *token)
	if *jsonFlag {
		data, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return exitcodes.Wrap(err, exitcodes.ExitGeneric, exitcodes.KindInternal, "failed to marshal doctor report")
		}
		fmt.Fprintln(stdout, string(data))
	} else {
		printDoctorReport(stdout, report)
	}
	if report.Failures > 0 {
		return exitcodes.Wrapf(nil, exitcodes.ExitGeneric, exitcodes.KindConfig, "doctor found %d hard blocker(s)", report.Failures)
	}
	if report.Warnings > 0 {
		return exitcodes.Wrapf(nil, exitcodes.ExitNoInput, exitcodes.KindConfig, "doctor found %d warning(s)", report.Warnings)
	}
	return nil
}

func runDoctorChecks(ctx context.Context, cfg *resolvedConfig, includePaperless bool, baseURL, token string) *doctorReport {
	report := &doctorReport{Status: doctorOK}
	if cfg.vault == "" {
		report.add("config.vault", doctorFail, "vault is not configured")
	} else {
		report.add("config.vault", doctorOK, cfg.vault)
		checkWritableDir(report, "path.vault", cfg.vault)
	}
	if cfg.archive == "" {
		report.add("config.archive", doctorFail, "archive path is not configured")
	} else {
		checkWritableDir(report, "path.archive", cfg.archive)
	}
	if cfg.db == "" {
		report.add("config.db", doctorFail, "database path is not configured")
	} else {
		checkWritableDB(report, cfg.db)
	}
	if cfg.ocrLang == "" {
		report.add("config.ocr_lang", doctorWarn, "ocr language not set; defaulting to eng")
	} else {
		report.add("config.ocr_lang", doctorOK, cfg.ocrLang)
	}
	if cfg.inbox == "" {
		report.add("config.inbox", doctorWarn, "inbox is not configured; watch mode requires an explicit directory")
	} else {
		checkWritableDir(report, "path.inbox", cfg.inbox)
	}
	checkCommand(report, "tool.pdftoppm", "pdftoppm", doctorFail)
	checkTesseract(report, cfg.ocrLang)
	if runtime.GOOS == "darwin" {
		checkCommand(report, "tool.sips", "sips", doctorWarn)
	}
	checkOptionalCommand(report, "tool.optional.textutil", "textutil")
	checkOptionalCommand(report, "tool.optional.pandoc", "pandoc")
	checkOptionalCommand(report, "tool.optional.libreoffice", "libreoffice")
	checkOptionalCommand(report, "tool.optional.soffice", "soffice")
	checkOptionalCommand(report, "tool.optional.pdfinfo", "pdfinfo")
	checkOptionalCommand(report, "tool.optional.pdfseparate", "pdfseparate")
	checkOptionalCommand(report, "tool.optional.pdfunite", "pdfunite")
	checkOptionalCommand(report, "tool.optional.qpdf", "qpdf")
	checkOptionalCommand(report, "tool.optional.magika", "magika")
	if len(cfg.raw.IMAPAccounts) > 0 {
		checkIMAP(ctx, report, cfg.raw.IMAPAccounts)
		checkMailPollStatus(ctx, report, cfg.db, cfg.raw.IMAPAccounts)
	}
	if includePaperless {
		checkPaperless(ctx, report, baseURL, token)
	}
	return report
}

func checkWritableDir(report *doctorReport, name, dir string) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		report.add(name, doctorFail, fmt.Sprintf("cannot create directory: %v", err))
		return
	}
	f, err := os.CreateTemp(dir, ".symingest-doctor-*")
	if err != nil {
		report.add(name, doctorFail, fmt.Sprintf("not writable: %v", err))
		return
	}
	path := f.Name()
	if err := f.Close(); err != nil {
		report.add(name, doctorFail, fmt.Sprintf("temp file close failed: %v", err))
		return
	}
	_ = os.Remove(path)
	report.add(name, doctorOK, dir)
}

func checkWritableDB(report *doctorReport, dbPath string) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		report.add("path.db", doctorFail, fmt.Sprintf("cannot create database directory: %v", err))
		return
	}
	st, err := store.Open(dbPath)
	if err != nil {
		report.add("path.db", doctorFail, fmt.Sprintf("cannot open database: %v", err))
		return
	}
	if err := st.Close(); err != nil {
		report.add("path.db", doctorFail, fmt.Sprintf("cannot close database: %v", err))
		return
	}
	report.add("path.db", doctorOK, dbPath)
}

func checkCommand(report *doctorReport, name, command string, missing doctorStatus) {
	path, err := exec.LookPath(command)
	if err != nil {
		report.add(name, missing, fmt.Sprintf("%s not found in PATH", command))
		return
	}
	report.add(name, doctorOK, path)
}

func checkOptionalCommand(report *doctorReport, name, command string) {
	path, err := exec.LookPath(command)
	if err != nil {
		report.add(name, doctorOK, fmt.Sprintf("%s not found in PATH (optional)", command))
		return
	}
	report.add(name, doctorOK, path)
}

func checkTesseract(report *doctorReport, lang string) {
	path, err := exec.LookPath("tesseract")
	if err != nil {
		report.add("tool.tesseract", doctorFail, "tesseract not found in PATH")
		return
	}
	out, err := exec.Command(path, "--list-langs").CombinedOutput()
	if err != nil {
		report.add("tool.tesseract", doctorFail, fmt.Sprintf("tesseract --list-langs failed: %v", err))
		return
	}
	if lang != "" && !languageListed(string(out), lang) {
		report.add("tool.tesseract.lang", doctorFail, fmt.Sprintf("language %q is not installed", lang))
		return
	}
	report.add("tool.tesseract", doctorOK, path)
}

func languageListed(output, lang string) bool {
	for _, line := range strings.Split(output, "\n") {
		if strings.TrimSpace(line) == lang {
			return true
		}
	}
	return false
}

func checkPaperless(ctx context.Context, report *doctorReport, baseURL, token string) {
	if strings.TrimSpace(baseURL) == "" {
		report.add("paperless.url", doctorFail, "Paperless base URL is not configured")
		return
	}
	if strings.TrimSpace(token) == "" {
		report.add("paperless.token", doctorFail, "Paperless token is missing (set PAPERLESS_TOKEN or pass --token)")
		return
	}
	url := strings.TrimRight(baseURL, "/") + "/api/documents/?page_size=1"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		report.add("paperless.api", doctorFail, fmt.Sprintf("invalid Paperless URL: %v", err))
		return
	}
	req.Header.Set("Authorization", "Token "+token)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		report.add("paperless.api", doctorFail, fmt.Sprintf("request failed: %v", err))
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		report.add("paperless.api", doctorFail, fmt.Sprintf("unexpected HTTP status %s", resp.Status))
		return
	}
	var payload struct {
		Count   int               `json:"count"`
		Results []json.RawMessage `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		report.add("paperless.api", doctorFail, fmt.Sprintf("unexpected response JSON: %v", err))
		return
	}
	report.add("paperless.api", doctorOK, fmt.Sprintf("reachable; %d documents reported", payload.Count))
}

func printDoctorReport(w io.Writer, report *doctorReport) {
	fmt.Fprintf(w, "symingest doctor: %s (%d failures, %d warnings)\n", strings.ToUpper(string(report.Status)), report.Failures, report.Warnings)
	for _, c := range report.Checks {
		fmt.Fprintf(w, "[%s] %s: %s\n", strings.ToUpper(string(c.Status)), c.Name, c.Message)
	}
}
