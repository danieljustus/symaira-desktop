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

	"github.com/danieljustus/symaira-desktop/internal/contacts"
	"github.com/danieljustus/symaira-desktop/internal/ingest"
	"github.com/danieljustus/symaira-desktop/internal/pdf"
	"github.com/danieljustus/symaira-desktop/internal/retrieval"
	ingestapi "github.com/danieljustus/symaira-ingest/api"
	printapi "github.com/danieljustus/symaira-print/api"
)

// errIsolated is what every inert ingest seam returns. A test that wants a
// real answer from one of them overrides that seam itself.
var errIsolated = errors.New("ingest pipeline is isolated in tests")

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

	pdf.EngineAvailableFunc = func(context.Context) (bool, string) {
		return false, "no typesetting engine in tests"
	}
	pdf.RenderFunc = func(context.Context, []byte, string, printapi.Options) (*printapi.Result, error) {
		return nil, printapi.ErrEngineUnavailable
	}

	// ErrNoVault, not errIsolated: an isolated test should see what a machine
	// with no vault configured sees, so the built-in inbox fallback still runs
	// and the note-writing paths stay under test.
	ingest.IngestFunc = func(context.Context, string, ingestapi.Options) (*ingestapi.Result, error) {
		return nil, ingestapi.ErrNoVault
	}
	ingest.JobsFunc = func(context.Context, ingestapi.Options, int) ([]ingestapi.Job, error) {
		return nil, errIsolated
	}
	ingest.RetryJobFunc = func(context.Context, ingestapi.Options, int64) error { return errIsolated }
	ingest.SplitPDFFunc = func(context.Context, string, string, string) ([]string, error) {
		return nil, errIsolated
	}

	ingest.ExtractTextFunc = func(context.Context, string, ingestapi.Options) (*ingestapi.Extraction, error) {
		return nil, errIsolated
	}
	ingest.MailAccountsFunc = func(string) ([]ingestapi.MailAccount, error) { return nil, nil }
	ingest.FetchMailFunc = func(context.Context, ingestapi.MailFetchOptions) (*ingestapi.MailFetchResult, error) {
		return &ingestapi.MailFetchResult{}, nil
	}
}
