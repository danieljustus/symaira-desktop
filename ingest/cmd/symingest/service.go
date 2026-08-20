package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/danieljustus/symaira-corekit/exitcodes"
	"github.com/danieljustus/symaira-ingest/internal/store"
)

const serviceLabel = "dev.symaira.symingest.watch"

type serviceOptions struct {
	Inbox         string `json:"inbox"`
	Vault         string `json:"vault"`
	Archive       string `json:"archive"`
	DB            string `json:"db"`
	OCRLang       string `json:"ocr_lang"`
	ProcessingDir string `json:"processing_dir,omitempty"`
	ProcessedDir  string `json:"processed_dir,omitempty"`
	FailedDir     string `json:"failed_dir,omitempty"`
	StableFor     string `json:"stable_for,omitempty"`
	Binary        string `json:"binary"`
	PlistPath     string `json:"plist_path"`
	LogDir        string `json:"log_dir"`
}

type serviceStatus struct {
	Label       string         `json:"label"`
	Installed   bool           `json:"installed"`
	Loaded      bool           `json:"loaded"`
	Running     bool           `json:"running"`
	PID         int            `json:"pid,omitempty"`
	PlistPath   string         `json:"plist_path"`
	LogDir      string         `json:"log_dir"`
	Inbox       string         `json:"inbox,omitempty"`
	Vault       string         `json:"vault,omitempty"`
	Archive     string         `json:"archive,omitempty"`
	DB          string         `json:"db,omitempty"`
	QueueCounts map[string]int `json:"queue_counts,omitempty"`
	Message     string         `json:"message,omitempty"`
}

func runService(args []string) error {
	fs := flag.NewFlagSet("service", flag.ContinueOnError)
	jsonFlag := fs.Bool("json", false, "Output JSON for status/logs/install dry-run")
	dryRun := fs.Bool("dry-run", false, "Print planned LaunchAgent plist/actions without writing or calling launchctl")
	inbox := fs.String("inbox", "", "Inbox directory to watch (defaults to config inbox)")
	processingDir := fs.String("processing-dir", "", "Move stable files here before enqueueing them")
	processedDir := fs.String("processed-dir", "", "Move successfully processed source files here")
	failedDir := fs.String("failed-dir", "", "Move failed source files here and write .error.json sidecars")
	stableFor := fs.Duration("stable-for", time.Second, "How long a file must remain unchanged before enqueueing")
	lines := fs.Int("lines", 200, "Number of log lines for service logs")
	ocrLang, vault, archive, db := registerSharedFlags(fs)
	configureUsage(fs, "service [flags] <install|uninstall|start|stop|status|logs>", "Manage the macOS LaunchAgent for the symingest inbox watcher. install/uninstall/start/stop have side effects unless --dry-run is set. No secrets are embedded in the LaunchAgent.")
	help, err := parseFlags(fs, args, "invalid service flags")
	if help || err != nil {
		return err
	}
	remaining := fs.Args()
	if len(remaining) == 0 {
		fs.Usage()
		return nil
	}
	cfg, err := resolveConfig(fs, ocrLang, vault, archive, db)
	if err != nil {
		return err
	}
	opts, err := buildServiceOptions(cfg, *inbox, *processingDir, *processedDir, *failedDir, stableFor.String())
	if err != nil {
		return err
	}

	switch remaining[0] {
	case "install":
		return serviceInstall(opts, *dryRun, *jsonFlag)
	case "uninstall":
		return serviceUninstall(opts, *dryRun, *jsonFlag)
	case "start":
		return serviceStart(opts, *dryRun, *jsonFlag)
	case "stop":
		return serviceStop(opts, *dryRun, *jsonFlag)
	case "status":
		return servicePrintStatus(opts, *jsonFlag)
	case "logs":
		return serviceLogs(opts, *lines, *jsonFlag)
	default:
		return exitcodes.Wrapf(nil, exitcodes.ExitNoInput, exitcodes.KindValidation, "unknown service command %q", remaining[0])
	}
}

func buildServiceOptions(cfg *resolvedConfig, inbox, processingDir, processedDir, failedDir, stableFor string) (*serviceOptions, error) {
	if runtime.GOOS != "darwin" {
		return nil, exitcodes.Wrapf(nil, exitcodes.ExitConfig, exitcodes.KindConfig, "service management currently supports macOS LaunchAgents only")
	}
	if inbox == "" {
		inbox = cfg.inbox
	}
	if inbox == "" {
		inbox = os.Getenv("SYMINGEST_INBOX")
	}
	if inbox == "" {
		inbox = os.Getenv("SYMINGEST_INBOX_PATH")
	}
	if inbox == "" {
		return nil, exitcodes.Wrapf(nil, exitcodes.ExitConfig, exitcodes.KindConfig, "no inbox configured; use --inbox or set inbox in config")
	}
	if cfg.vault == "" {
		return nil, exitcodes.Wrapf(nil, exitcodes.ExitConfig, exitcodes.KindConfig, "no vault configured; use --vault, SYMINGEST_VAULT env, or config")
	}
	bin, err := os.Executable()
	if err != nil {
		return nil, exitcodes.Wrap(err, exitcodes.ExitConfig, exitcodes.KindConfig, "cannot locate symingest executable")
	}
	bin, _ = filepath.EvalSymlinks(bin)
	absInbox, err := filepath.Abs(inbox)
	if err != nil {
		return nil, exitcodes.Wrap(err, exitcodes.ExitData, exitcodes.KindValidation, "invalid inbox path")
	}
	absInbox = filepath.Clean(absInbox)
	if processingDir == "" {
		processingDir = filepath.Join(absInbox, ".processing")
	}
	if processedDir == "" {
		processedDir = filepath.Join(absInbox, ".processed")
	}
	if failedDir == "" {
		failedDir = filepath.Join(absInbox, ".failed")
	}
	plist, err := launchAgentPath()
	if err != nil {
		return nil, err
	}
	logDir, err := serviceLogDir()
	if err != nil {
		return nil, err
	}
	return &serviceOptions{
		Inbox:         absInbox,
		Vault:         cfg.vault,
		Archive:       cfg.archive,
		DB:            cfg.db,
		OCRLang:       cfg.ocrLang,
		ProcessingDir: processingDir,
		ProcessedDir:  processedDir,
		FailedDir:     failedDir,
		StableFor:     stableFor,
		Binary:        bin,
		PlistPath:     plist,
		LogDir:        logDir,
	}, nil
}

func serviceInstall(opts *serviceOptions, dryRun, outputJSON bool) error {
	plist := renderLaunchAgent(opts)
	if dryRun {
		if outputJSON {
			return jsonOut(map[string]any{"action": "install", "dry_run": true, "plist_path": opts.PlistPath, "plist": plist})
		}
		fmt.Fprintf(stdout, "Would write LaunchAgent: %s\n\n%s", opts.PlistPath, plist)
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(opts.PlistPath), 0o700); err != nil {
		return exitcodes.Wrap(err, exitcodes.ExitConfig, exitcodes.KindConfig, "create LaunchAgents directory")
	}
	if err := os.MkdirAll(opts.LogDir, 0o700); err != nil {
		return exitcodes.Wrap(err, exitcodes.ExitConfig, exitcodes.KindConfig, "create service log directory")
	}
	if err := os.WriteFile(opts.PlistPath, []byte(plist), 0o600); err != nil {
		return exitcodes.Wrap(err, exitcodes.ExitConfig, exitcodes.KindConfig, "write LaunchAgent plist")
	}
	if outputJSON {
		return jsonOut(map[string]any{"action": "install", "plist_path": opts.PlistPath, "log_dir": opts.LogDir})
	}
	fmt.Fprintf(stdout, "Installed LaunchAgent: %s\nStart with: symingest service start\n", opts.PlistPath)
	return nil
}

func serviceUninstall(opts *serviceOptions, dryRun, outputJSON bool) error {
	if dryRun {
		if outputJSON {
			return jsonOut(map[string]any{"action": "uninstall", "dry_run": true, "plist_path": opts.PlistPath})
		}
		fmt.Fprintf(stdout, "Would stop and remove LaunchAgent: %s\n", opts.PlistPath)
		return nil
	}
	_ = runLaunchctl("bootout", launchctlDomain(), opts.PlistPath)
	if err := os.Remove(opts.PlistPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return exitcodes.Wrap(err, exitcodes.ExitConfig, exitcodes.KindConfig, "remove LaunchAgent plist")
	}
	if outputJSON {
		return jsonOut(map[string]any{"action": "uninstall", "plist_path": opts.PlistPath})
	}
	fmt.Fprintf(stdout, "Uninstalled LaunchAgent: %s\n", opts.PlistPath)
	return nil
}

func serviceStart(opts *serviceOptions, dryRun, outputJSON bool) error {
	if dryRun {
		if outputJSON {
			return jsonOut(map[string]any{"action": "start", "dry_run": true, "domain": launchctlDomain(), "label": serviceLabel})
		}
		fmt.Fprintf(stdout, "Would bootstrap/start %s in %s\n", serviceLabel, launchctlDomain())
		return nil
	}
	if _, err := os.Stat(opts.PlistPath); err != nil {
		return exitcodes.Wrap(err, exitcodes.ExitConfig, exitcodes.KindConfig, "LaunchAgent not installed; run symingest service install first")
	}
	_ = runLaunchctl("bootstrap", launchctlDomain(), opts.PlistPath)
	if err := runLaunchctl("kickstart", "-k", launchctlDomain()+"/"+serviceLabel); err != nil {
		return exitcodes.Wrap(err, exitcodes.ExitGeneric, exitcodes.KindInternal, "start LaunchAgent")
	}
	if outputJSON {
		return jsonOut(map[string]any{"action": "start", "label": serviceLabel})
	}
	fmt.Fprintf(stdout, "Started LaunchAgent: %s\n", serviceLabel)
	return nil
}

func serviceStop(opts *serviceOptions, dryRun, outputJSON bool) error {
	if dryRun {
		if outputJSON {
			return jsonOut(map[string]any{"action": "stop", "dry_run": true, "domain": launchctlDomain(), "label": serviceLabel})
		}
		fmt.Fprintf(stdout, "Would bootout %s from %s\n", serviceLabel, launchctlDomain())
		return nil
	}
	if err := runLaunchctl("bootout", launchctlDomain()+"/"+serviceLabel); err != nil {
		return exitcodes.Wrap(err, exitcodes.ExitGeneric, exitcodes.KindInternal, "stop LaunchAgent")
	}
	if outputJSON {
		return jsonOut(map[string]any{"action": "stop", "label": serviceLabel})
	}
	fmt.Fprintf(stdout, "Stopped LaunchAgent: %s\n", serviceLabel)
	return nil
}

func servicePrintStatus(opts *serviceOptions, outputJSON bool) error {
	status := getServiceStatus(opts)
	if outputJSON {
		return jsonOut(status)
	}
	fmt.Fprintf(stdout, "Label:     %s\nInstalled: %t\nLoaded:    %t\nRunning:   %t\n", status.Label, status.Installed, status.Loaded, status.Running)
	if status.PID != 0 {
		fmt.Fprintf(stdout, "PID:       %d\n", status.PID)
	}
	fmt.Fprintf(stdout, "Plist:     %s\nLogs:      %s\nInbox:     %s\nVault:     %s\nArchive:   %s\nDB:        %s\n", status.PlistPath, status.LogDir, status.Inbox, status.Vault, status.Archive, status.DB)
	if len(status.QueueCounts) > 0 {
		fmt.Fprintln(stdout, "Queue:")
		for k, v := range status.QueueCounts {
			fmt.Fprintf(stdout, "  %s: %d\n", k, v)
		}
	}
	if status.Message != "" {
		fmt.Fprintf(stdout, "Message:   %s\n", status.Message)
	}
	return nil
}

func getServiceStatus(opts *serviceOptions) serviceStatus {
	st := serviceStatus{Label: serviceLabel, PlistPath: opts.PlistPath, LogDir: opts.LogDir, Inbox: opts.Inbox, Vault: opts.Vault, Archive: opts.Archive, DB: opts.DB}
	if _, err := os.Stat(opts.PlistPath); err == nil {
		st.Installed = true
	}
	out, err := launchctlOutput("print", launchctlDomain()+"/"+serviceLabel)
	if err != nil {
		st.Message = strings.TrimSpace(string(out))
	} else {
		st.Loaded = true
		st.Running = strings.Contains(string(out), "state = running") || strings.Contains(string(out), "pid =")
		st.PID = parseLaunchctlPID(string(out))
	}
	if counts, err := queueCounts(opts.DB); err == nil {
		st.QueueCounts = counts
	}
	return st
}

func serviceLogs(opts *serviceOptions, lines int, outputJSON bool) error {
	if lines <= 0 {
		lines = 200
	}
	paths := []string{filepath.Join(opts.LogDir, "watch.log"), filepath.Join(opts.LogDir, "watch.err.log")}
	logs := make(map[string][]string)
	for _, p := range paths {
		got, err := readLastLines(p, lines)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return exitcodes.Wrapf(err, exitcodes.ExitGeneric, exitcodes.KindInternal, "read log %s", p)
		}
		logs[p] = got
	}
	if outputJSON {
		return jsonOut(map[string]any{"label": serviceLabel, "logs": logs})
	}
	for _, p := range paths {
		fmt.Fprintf(stdout, "== %s ==\n", p)
		if len(logs[p]) == 0 {
			fmt.Fprintln(stdout, "(empty)")
			continue
		}
		for _, line := range logs[p] {
			fmt.Fprintln(stdout, line)
		}
	}
	return nil
}

func renderLaunchAgent(opts *serviceOptions) string {
	args := []string{opts.Binary, "watch", "--vault", opts.Vault, "--archive", opts.Archive, "--db", opts.DB, "--ocr-lang", opts.OCRLang}
	if opts.ProcessingDir != "" {
		args = append(args, "--processing-dir", opts.ProcessingDir)
	}
	if opts.ProcessedDir != "" {
		args = append(args, "--processed-dir", opts.ProcessedDir)
	}
	if opts.FailedDir != "" {
		args = append(args, "--failed-dir", opts.FailedDir)
	}
	if opts.StableFor != "" {
		args = append(args, "--stable-for", opts.StableFor)
	}
	args = append(args, opts.Inbox)
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>` + xmlEscape(serviceLabel) + `</string>
  <key>ProgramArguments</key>
  <array>
`)
	for _, a := range args {
		b.WriteString("    <string>" + xmlEscape(a) + "</string>\n")
	}
	b.WriteString(`  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>
  <key>StandardOutPath</key><string>` + xmlEscape(filepath.Join(opts.LogDir, "watch.log")) + `</string>
  <key>StandardErrorPath</key><string>` + xmlEscape(filepath.Join(opts.LogDir, "watch.err.log")) + `</string>
  <key>WorkingDirectory</key><string>` + xmlEscape(filepath.Dir(opts.Inbox)) + `</string>
</dict>
</plist>
`)
	return b.String()
}

func launchAgentPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", exitcodes.Wrap(err, exitcodes.ExitConfig, exitcodes.KindConfig, "cannot determine home directory")
	}
	return filepath.Join(home, "Library", "LaunchAgents", serviceLabel+".plist"), nil
}

func serviceLogDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", exitcodes.Wrap(err, exitcodes.ExitConfig, exitcodes.KindConfig, "cannot determine home directory")
	}
	return filepath.Join(home, "Library", "Logs", "symingest"), nil
}

func launchctlDomain() string {
	return fmt.Sprintf("gui/%d", os.Getuid())
}

func runLaunchctl(args ...string) error {
	out, err := launchctlOutput(args...)
	if err != nil {
		return fmt.Errorf("launchctl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func launchctlOutput(args ...string) ([]byte, error) {
	cmd := exec.Command("/bin/launchctl", args...)
	return cmd.CombinedOutput()
}

func parseLaunchctlPID(out string) int {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "pid =") {
			fields := strings.Fields(line)
			if len(fields) >= 3 {
				pid, _ := strconv.Atoi(fields[2])
				return pid
			}
		}
	}
	return 0
}

func queueCounts(dbPath string) (map[string]int, error) {
	if dbPath == "" {
		return nil, nil
	}
	st, err := store.Open(dbPath)
	if err != nil {
		return nil, err
	}
	defer st.Close()
	jobs, err := st.ListJobs(context.Background(), 0)
	if err != nil {
		return nil, err
	}
	counts := map[string]int{}
	for _, j := range jobs {
		counts[j.Status]++
	}
	return counts, nil
}

func readLastLines(path string, n int) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var lines []string
	s := bufio.NewScanner(f)
	for s.Scan() {
		lines = append(lines, s.Text())
		if len(lines) > n {
			copy(lines, lines[len(lines)-n:])
			lines = lines[:n]
		}
	}
	if err := s.Err(); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return lines, nil
}

func xmlEscape(s string) string {
	replacer := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")
	return replacer.Replace(s)
}

func jsonOut(v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return exitcodes.Wrap(err, exitcodes.ExitGeneric, exitcodes.KindInternal, "marshal JSON")
	}
	fmt.Fprintln(stdout, string(data))
	return nil
}

type watchLock struct{ path string }

func acquireWatchLock(inbox, db string) (*watchLock, error) {
	lockPath, err := watchLockPath(inbox, db)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return nil, err
	}
	for {
		f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_, _ = fmt.Fprintf(f, "%d\n%s\n%s\n", os.Getpid(), inbox, time.Now().UTC().Format(time.RFC3339))
			_ = f.Close()
			return &watchLock{path: lockPath}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		pid, stale := readLockPID(lockPath)
		if stale || pid == 0 || !processAlive(pid) {
			_ = os.Remove(lockPath)
			continue
		}
		return nil, fmt.Errorf("watcher already running for this inbox/db with pid %d (lock: %s)", pid, lockPath)
	}
}

func (l *watchLock) Release() {
	if l != nil && l.path != "" {
		_ = os.Remove(l.path)
	}
}

func watchLockPath(inbox, db string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(filepath.Clean(inbox) + "\x00" + filepath.Clean(db)))
	return filepath.Join(home, "Library", "Application Support", "symingest", "locks", "watch-"+hex.EncodeToString(sum[:8])+".lock"), nil
}

func readLockPID(path string) (int, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, true
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) == 0 {
		return 0, true
	}
	pid, err := strconv.Atoi(strings.TrimSpace(lines[0]))
	if err != nil {
		return 0, true
	}
	return pid, false
}
