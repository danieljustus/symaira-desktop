package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/danieljustus/symaira-corekit/exitcodes"
	"github.com/danieljustus/symaira-ingest/internal/config"
	"github.com/danieljustus/symaira-ingest/internal/store"
)

const mailJSONSchemaVersion = config.MailJSONSchemaVersion

const mailWriteReloadSemantics = "Changes are written atomically and take effect on the next symingest watch restart; an already-running watcher is not hot-reloaded."

type mailJSONResponse struct {
	SchemaVersion   int                       `json:"schema_version"`
	Operation       string                    `json:"operation"`
	ConfigPath      string                    `json:"config_path"`
	Accounts        []config.MailAccountView  `json:"accounts"`
	Account         *config.MailAccountView   `json:"account,omitempty"`
	Deleted         bool                      `json:"deleted,omitempty"`
	Valid           bool                      `json:"valid,omitempty"`
	Errors          []string                  `json:"errors,omitempty"`
	Warnings        []string                  `json:"warnings,omitempty"`
	ReloadRequired  bool                      `json:"reload_required"`
	ReloadSemantics string                    `json:"reload_semantics,omitempty"`
	PollStatus      map[string]mailPollStatus `json:"poll_status,omitempty"`
}

// mailPollStatus is the JSON view of store.MailPollStatus, keyed by
// config.AccountID in mailJSONResponse.PollStatus.
type mailPollStatus struct {
	LastPolledAt time.Time `json:"last_polled_at"`
	Status       string    `json:"status"`
	LastError    string    `json:"last_error,omitempty"`
}

// loadMailPollStatus reads the last recorded poll outcome for each account
// from the document store. Errors opening the store are non-fatal: poll
// status is best-effort diagnostic information, not required for `mail list`
// to function.
func loadMailPollStatus(dbPath string, accounts []config.IMAPAccount) map[string]mailPollStatus {
	if dbPath == "" || len(accounts) == 0 {
		return nil
	}
	st, err := store.Open(dbPath)
	if err != nil {
		return nil
	}
	defer st.Close()

	ctx := context.Background()
	result := make(map[string]mailPollStatus)
	for _, acc := range accounts {
		id := config.AccountID(acc)
		s, err := st.GetMailPollStatus(ctx, id)
		if err != nil || s == nil {
			continue
		}
		result[id] = mailPollStatus{LastPolledAt: s.LastPolledAt, Status: s.Status, LastError: s.LastError}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

type mailInput struct {
	Account          *config.IMAPAccount  `json:"account"`
	Accounts         []config.IMAPAccount `json:"accounts"`
	IMAPPollInterval string               `json:"imap_poll_interval"`
}

func runMail(args []string) error {
	fs := flagNewMail()
	mailFlags := mailFlagsFor(fs)
	ocrLang, vault, archive, db := registerSharedFlags(fs)
	help, err := parseFlags(fs, args, "invalid mail flags")
	if help || err != nil {
		return err
	}
	remaining := fs.Args()
	if len(remaining) > 0 && remaining[0] == "accounts" {
		remaining = remaining[1:]
	}
	action := "list"
	if len(remaining) > 0 {
		action = remaining[0]
	}
	if action == "read" {
		action = "list"
	}

	configPath, err := config.ConfigPath(*mailFlags.configPath)
	if err != nil {
		return exitcodes.Wrap(err, exitcodes.ExitConfig, exitcodes.KindConfig, "resolve mail config path")
	}
	if *mailFlags.outputJSON == false && action != "validate" {
		// Human mode remains intentionally small and does not expose secret data.
	}

	if action == "validate" {
		return runMailValidate(configPath, *mailFlags.outputJSON)
	}

	document, err := config.ReadMailConfig(configPath)
	if err != nil {
		return exitcodes.Wrap(err, exitcodes.ExitConfig, exitcodes.KindConfig, "read mail configuration")
	}

	switch action {
	case "list":
		cfg, cfgErr := resolveConfig(fs, ocrLang, vault, archive, db)
		dbPath := ""
		if cfgErr == nil {
			dbPath = cfg.db
		}
		return printMailList(document, *mailFlags.outputJSON, loadMailPollStatus(dbPath, document.Accounts))
	case "create":
		return runMailCreate(configPath, document, *mailFlags.inputPath, *mailFlags.outputJSON)
	case "update":
		selector := *mailFlags.accountID
		if selector == "" && len(remaining) > 1 {
			selector = remaining[1]
		}
		return runMailUpdate(configPath, document, selector, *mailFlags.inputPath, *mailFlags.outputJSON)
	case "delete":
		selector := *mailFlags.accountID
		if selector == "" && len(remaining) > 1 {
			selector = remaining[1]
		}
		return runMailDelete(configPath, document, selector, *mailFlags.outputJSON)
	case "replace":
		return runMailReplace(configPath, document, *mailFlags.inputPath, *mailFlags.outputJSON)
	default:
		return exitcodes.Wrapf(nil, exitcodes.ExitNoInput, exitcodes.KindValidation,
			"unknown mail subcommand %q; supported: list, validate, create, update, delete, replace", action)
	}
}

type mailFlags struct {
	outputJSON *bool
	configPath *string
	inputPath  *string
	accountID  *string
}

func flagNewMail() *flag.FlagSet {
	fs := flag.NewFlagSet("mail", flag.ContinueOnError)
	fs.SetOutput(stdout)
	return fs
}

func mailFlagsFor(fs *flag.FlagSet) mailFlags {
	return mailFlags{
		outputJSON: fs.Bool("json", false, "Output a versioned JSON contract"),
		configPath: fs.String("config", "", "TOML config path (defaults to existing ./.symingest.toml or ~/.config/symingest/config.toml)"),
		inputPath:  fs.String("input", "", "JSON input path, or - to read JSON from stdin"),
		accountID:  fs.String("id", "", "Stable account ID for update/delete"),
	}
}

func runMailValidate(path string, outputJSON bool) error {
	response := mailJSONResponse{SchemaVersion: mailJSONSchemaVersion, Operation: "validate", ConfigPath: path, Accounts: []config.MailAccountView{}, ReloadRequired: false}
	document, err := config.ReadMailConfig(path)
	if err != nil {
		response.Valid = false
		response.Errors = []string{err.Error()}
		if outputJSON {
			return printRulesJSON(response, "failed to marshal mail validation")
		}
		return exitcodes.Wrap(err, exitcodes.ExitConfig, exitcodes.KindConfig, "validate mail configuration")
	}
	validation := config.ValidateMailAccounts(document.Accounts, document.IMAPPollInterval)
	response.Valid = validation.Valid
	response.Errors = validation.Errors
	response.Warnings = validation.Warnings
	for _, account := range document.Accounts {
		response.Accounts = append(response.Accounts, config.ViewAccount(account))
	}
	if outputJSON {
		return printRulesJSON(response, "failed to marshal mail validation")
	}
	if !response.Valid {
		return exitcodes.Wrapf(nil, exitcodes.ExitConfig, exitcodes.KindConfig, "mail configuration is invalid: %s", strings.Join(response.Errors, "; "))
	}
	fmt.Fprintf(stdout, "Mail configuration valid: %d account(s)\n", len(document.Accounts))
	return nil
}

func printMailList(document *config.MailConfigDocument, outputJSON bool, pollStatus map[string]mailPollStatus) error {
	response := mailJSONResponse{SchemaVersion: mailJSONSchemaVersion, Operation: "list", ConfigPath: document.Path, Accounts: []config.MailAccountView{}, ReloadRequired: false, PollStatus: pollStatus}
	for _, account := range document.Accounts {
		response.Accounts = append(response.Accounts, config.ViewAccount(account))
	}
	if outputJSON {
		return printRulesJSON(response, "failed to marshal mail accounts")
	}
	if len(document.Accounts) == 0 {
		fmt.Fprintln(stdout, "No IMAP accounts configured.")
		return nil
	}
	for _, account := range response.Accounts {
		fmt.Fprintf(stdout, "%s: %s@%s:%d folder=%s password=%s\n", account.ID, account.Username, account.Host, account.Port, account.Folder, account.PasswordSecretKind)
		if s, ok := pollStatus[account.ID]; ok {
			if s.Status == "ok" {
				fmt.Fprintf(stdout, "  last poll: %s ok\n", s.LastPolledAt.Format(time.RFC3339))
			} else {
				fmt.Fprintf(stdout, "  last poll: %s failed (%s)\n", s.LastPolledAt.Format(time.RFC3339), s.LastError)
			}
		} else {
			fmt.Fprintln(stdout, "  last poll: never")
		}
	}
	return nil
}

func runMailCreate(path string, document *config.MailConfigDocument, inputPath string, outputJSON bool) error {
	account, _, _, _, err := decodeMailInput(inputPath, false)
	if err != nil {
		return err
	}
	if account == nil {
		return exitcodes.Wrapf(nil, exitcodes.ExitData, exitcodes.KindValidation, "create requires an account JSON object")
	}
	accounts := append([]config.IMAPAccount{}, document.Accounts...)
	accounts = append(accounts, *account)
	if err := config.WriteMailConfig(path, accounts, ""); err != nil {
		return exitcodes.Wrap(err, exitcodes.ExitConfig, exitcodes.KindConfig, "write mail configuration")
	}
	return printMailWriteResponse("create", path, accounts, account, false, outputJSON)
}

func runMailUpdate(path string, document *config.MailConfigDocument, selector, inputPath string, outputJSON bool) error {
	index, err := findMailAccount(document.Accounts, selector)
	if err != nil {
		return err
	}
	account, _, hasSecret, _, err := decodeMailInput(inputPath, true)
	if err != nil {
		return err
	}
	if account == nil {
		return exitcodes.Wrapf(nil, exitcodes.ExitData, exitcodes.KindValidation, "update requires an account JSON object")
	}
	if !hasSecret || isMaskedSecret(account.PasswordSecret) {
		account.PasswordSecret = document.Accounts[index].PasswordSecret
	}
	accounts := append([]config.IMAPAccount{}, document.Accounts...)
	accounts[index] = *account
	if err := config.WriteMailConfig(path, accounts, ""); err != nil {
		return exitcodes.Wrap(err, exitcodes.ExitConfig, exitcodes.KindConfig, "write mail configuration")
	}
	return printMailWriteResponse("update", path, accounts, account, false, outputJSON)
}

func runMailDelete(path string, document *config.MailConfigDocument, selector string, outputJSON bool) error {
	index, err := findMailAccount(document.Accounts, selector)
	if err != nil {
		return err
	}
	accounts := append([]config.IMAPAccount{}, document.Accounts[:index]...)
	accounts = append(accounts, document.Accounts[index+1:]...)
	if err := config.WriteMailConfig(path, accounts, ""); err != nil {
		return exitcodes.Wrap(err, exitcodes.ExitConfig, exitcodes.KindConfig, "write mail configuration")
	}
	return printMailWriteResponse("delete", path, accounts, nil, true, outputJSON)
}

func runMailReplace(path string, document *config.MailConfigDocument, inputPath string, outputJSON bool) error {
	_, accounts, _, pollInterval, err := decodeMailInput(inputPath, false)
	if err != nil {
		return err
	}
	if accounts == nil {
		return exitcodes.Wrapf(nil, exitcodes.ExitData, exitcodes.KindValidation, "replace requires an accounts JSON array")
	}
	if err := config.WriteMailConfig(path, accounts, pollInterval); err != nil {
		return exitcodes.Wrap(err, exitcodes.ExitConfig, exitcodes.KindConfig, "write mail configuration")
	}
	return printMailWriteResponse("replace", path, accounts, nil, false, outputJSON)
}

func printMailWriteResponse(operation, path string, accounts []config.IMAPAccount, account *config.IMAPAccount, deleted, outputJSON bool) error {
	response := mailJSONResponse{SchemaVersion: mailJSONSchemaVersion, Operation: operation, ConfigPath: path, Accounts: []config.MailAccountView{}, Deleted: deleted, ReloadRequired: true, ReloadSemantics: mailWriteReloadSemantics}
	for _, item := range accounts {
		response.Accounts = append(response.Accounts, config.ViewAccount(item))
	}
	if account != nil {
		view := config.ViewAccount(*account)
		response.Account = &view
	}
	if outputJSON {
		return printRulesJSON(response, "failed to marshal mail write response")
	}
	if deleted {
		fmt.Fprintln(stdout, "Deleted IMAP account.")
	} else {
		fmt.Fprintf(stdout, "%s IMAP account configuration. Restart symingest watch to apply it.\n", strings.Title(operation))
	}
	return nil
}

func decodeMailInput(path string, single bool) (*config.IMAPAccount, []config.IMAPAccount, bool, string, error) {
	var reader io.Reader = os.Stdin
	var file *os.File
	if path != "" && path != "-" {
		var err error
		file, err = os.Open(path)
		if err != nil {
			return nil, nil, false, "", exitcodes.Wrap(err, exitcodes.ExitData, exitcodes.KindValidation, "open mail JSON input")
		}
		defer file.Close()
		reader = file
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, nil, false, "", exitcodes.Wrap(err, exitcodes.ExitData, exitcodes.KindValidation, "read mail JSON input")
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil, nil, false, "", exitcodes.Wrapf(nil, exitcodes.ExitData, exitcodes.KindValidation, "mail JSON input is empty")
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		if !single {
			var accounts []config.IMAPAccount
			if arrayErr := json.Unmarshal(data, &accounts); arrayErr == nil {
				return nil, accounts, false, "", nil
			}
		}
		return nil, nil, false, "", exitcodes.Wrap(err, exitcodes.ExitData, exitcodes.KindValidation, "malformed mail JSON input")
	}
	var input mailInput
	if err := json.Unmarshal(data, &input); err != nil {
		return nil, nil, false, "", exitcodes.Wrap(err, exitcodes.ExitData, exitcodes.KindValidation, "decode mail JSON input")
	}
	if rawAccount, ok := raw["account"]; ok {
		var account config.IMAPAccount
		if err := json.Unmarshal(rawAccount, &account); err != nil {
			return nil, nil, false, "", exitcodes.Wrap(err, exitcodes.ExitData, exitcodes.KindValidation, "decode account JSON")
		}
		_, hasSecret := rawAccountField(rawAccount, "password_secret")
		return &account, nil, hasSecret, input.IMAPPollInterval, nil
	}
	if single {
		var account config.IMAPAccount
		if err := json.Unmarshal(data, &account); err != nil {
			return nil, nil, false, "", exitcodes.Wrap(err, exitcodes.ExitData, exitcodes.KindValidation, "decode account JSON")
		}
		_, hasSecret := raw["password_secret"]
		return &account, nil, hasSecret, input.IMAPPollInterval, nil
	}
	if input.Accounts != nil {
		return nil, input.Accounts, false, input.IMAPPollInterval, nil
	}
	var account config.IMAPAccount
	if err := json.Unmarshal(data, &account); err != nil {
		return nil, nil, false, "", exitcodes.Wrap(err, exitcodes.ExitData, exitcodes.KindValidation, "decode account JSON")
	}
	return &account, nil, false, input.IMAPPollInterval, nil
}

func rawAccountField(raw json.RawMessage, key string) (json.RawMessage, bool) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, false
	}
	value, ok := fields[key]
	return value, ok
}

func withSecretMarker(has bool) string {
	if has {
		return "present"
	}
	return ""
}

func findMailAccount(accounts []config.IMAPAccount, selector string) (int, error) {
	if selector == "" {
		return -1, exitcodes.Wrapf(nil, exitcodes.ExitData, exitcodes.KindValidation, "account ID is required (use --id or a positional selector)")
	}
	if index, err := strconv.Atoi(selector); err == nil && index >= 0 && index < len(accounts) {
		return index, nil
	}
	for index, account := range accounts {
		if config.AccountID(account) == selector {
			return index, nil
		}
	}
	return -1, exitcodes.Wrapf(nil, exitcodes.ExitData, exitcodes.KindValidation, "no IMAP account matches %q", selector)
}

func isMaskedSecret(value string) bool {
	return value == "<redacted>" || value == "***" || value == "[redacted]"
}
