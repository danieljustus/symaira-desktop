// Package testsupport neutralizes the side effects that the absorbed tools
// would otherwise have when a test exercises the SymDesk service layer.
//
// Since the repo consolidation, retrieval (symseek), the contact store
// (symrelate) and document ingest (symingest) run in-process rather than as
// sibling binaries a test could simply keep off $PATH. Their state lives under
// the user's home directory, so an unguarded `go test ./...` would write into
// the developer's real search index, document store, vault and archive, read
// their real contacts, and reach their real IMAP accounts.
//
// Every test package that constructs a service.Service therefore calls
// IsolateSideEffects from TestMain. A test that wants results back from one
// of these seams overrides that specific seam itself, after this call.
package testsupport

import (
	"context"
	"errors"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/compose"
	"github.com/danieljustus/symaira-desktop/internal/contacts"
	"github.com/danieljustus/symaira-desktop/internal/ingest"
	"github.com/danieljustus/symaira-desktop/internal/pdf"
	"github.com/danieljustus/symaira-desktop/internal/retrieval"
)

// errIsolated is what every inert ingest seam returns. A test that wants a
// real answer from one of them overrides that seam itself.
var errIsolated = errors.New("ingest pipeline is isolated in tests")

// IsolateCompanionBinaries makes every sibling-binary lookup resolve to
// nothing but the caller's own directory, independent of the host.
//
// compose.Resolve searches $SYMAIRA_BIN and the managed-runtime directory
// (~/.symaira/bin) *before* PATH, so setting PATH alone does not make a
// companion binary absent: on a developer machine that has the managed
// runtime populated, an absence assertion fails, and a positive test can even
// invoke the real installed binary. Both the resolution cache and the
// environment a Resolve call reads are therefore cleared here.
//
// dir is placed on PATH so a caller that wants a double can write one there;
// pass an empty t.TempDir() when the companion binary must be absent.
func IsolateCompanionBinaries(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("PATH", dir)
	// $HOME drives the managed-runtime tier; USERPROFILE is its Windows
	// equivalent, so both are redirected for the same reason.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv(compose.SymairaBinEnvVar, "")
	compose.ResetCache()
	t.Cleanup(compose.ResetCache)
}

// IsolateSideEffects points the in-process seams at inert doubles: the
// hybrid index accepts writes and returns nothing, the contact store reports
// itself unavailable, the PDF renderer reports no engine, and every ingest
// path — document pipeline, job queue, PDF split, OCR and mail poll — refuses
// rather than touching the user's vault, archive, document store or mailbox.
// It is safe to call more than once.
func IsolateSideEffects() {
	retrieval.IndexFunc = func(string, string) error { return nil }
	retrieval.DeleteFunc = func(string) error { return nil }
	retrieval.SearchFunc = func(string, int) ([]retrieval.Result, error) { return nil, nil }

	contacts.AvailableFunc = func(context.Context) bool { return false }
	contacts.ResolveFunc = func(context.Context, string) (*contacts.Ref, error) {
		return nil, contacts.ErrContactNotFound
	}
	contacts.FindByNameFunc = func(context.Context, string) ([]contacts.Ref, error) { return nil, nil }

	pdf.EngineAvailableFunc = func(context.Context) (bool, string) {
		return false, "no typesetting engine in tests"
	}
	pdf.RenderFunc = func(context.Context, []byte, string, pdf.Options) (*pdf.Result, error) {
		return nil, pdf.ErrEngineUnavailable
	}

	// ErrNoVault, not errIsolated: an isolated test should see what a machine
	// with no vault configured sees, so the built-in inbox fallback still runs
	// and the note-writing paths stay under test.
	ingest.IngestFunc = func(context.Context, string, ingest.Options) (*ingest.Result, error) {
		return nil, ingest.ErrNoVault
	}
	ingest.JobsFunc = func(context.Context, ingest.Options, int) ([]ingest.Job, error) {
		return nil, errIsolated
	}
	ingest.RetryJobFunc = func(context.Context, ingest.Options, int64) error { return errIsolated }
	ingest.SplitPDFFunc = func(context.Context, string, string, string) ([]string, error) {
		return nil, errIsolated
	}

	ingest.ExtractTextFunc = func(context.Context, string, ingest.Options) (*ingest.Extraction, error) {
		return nil, errIsolated
	}
	ingest.MailAccountsFunc = func(string) ([]ingest.MailAccount, error) { return nil, nil }
	ingest.FetchMailFunc = func(context.Context, ingest.MailFetchOptions) (*ingest.MailFetchResult, error) {
		return &ingest.MailFetchResult{}, nil
	}

	ingest.RulesFunc = func(context.Context, ingest.Options) ([]ingest.Rule, error) {
		return nil, errIsolated
	}
	ingest.AddRuleFunc = func(context.Context, ingest.Options, string, string, string) (*ingest.Rule, error) {
		return nil, errIsolated
	}
	ingest.UpdateRuleFunc = func(context.Context, ingest.Options, int64, string, string, string) (*ingest.Rule, error) {
		return nil, errIsolated
	}
	ingest.DeleteRuleFunc = func(context.Context, ingest.Options, int64) error { return errIsolated }
	ingest.ReprocessFunc = func(context.Context, ingest.Options, int64) (*ingest.ReprocessResult, error) {
		return nil, errIsolated
	}
	ingest.ReprocessByArchivePathFunc = func(context.Context, ingest.Options, string) (*ingest.ReprocessResult, error) {
		return nil, errIsolated
	}
	ingest.MergePDFsFunc = func(context.Context, []string, string) error { return errIsolated }
	ingest.RotatePDFFunc = func(context.Context, string, string, int, string) error { return errIsolated }
}
