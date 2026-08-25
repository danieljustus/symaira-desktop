package ingest

import (
	"context"
	"errors"
	"os"
	"testing"
)

// errNotStubbed is what the seams return by default in this package's tests.
//
// The ingest pipeline is linked in since the repo consolidation, so an
// unguarded test would write into the developer's real vault, archive and
// document store under $HOME. TestMain therefore points every seam at a
// refusing double; a test that needs a pipeline stubs the seam it exercises.
// This package cannot use testsupport.IsolateSideEffects — testsupport imports
// this package, so importing it back would be a cycle.
var errNotStubbed = errors.New("ingest seam not stubbed in this test")

func TestMain(m *testing.M) {
	isolateSeams()
	os.Exit(m.Run())
}

func isolateSeams() {
	// ErrNoVault rather than errNotStubbed: an unstubbed test should see the
	// same thing it would on a machine with no vault configured, which is what
	// the built-in inbox fallback exists for. A test that wants the pipeline
	// to succeed stubs this seam.
	IngestFunc = func(context.Context, string, Options) (*Result, error) {
		return nil, ErrNoVault
	}
	JobsFunc = func(context.Context, Options, int) ([]Job, error) {
		return nil, errNotStubbed
	}
	RetryJobFunc = func(context.Context, Options, int64) error { return errNotStubbed }
	SplitPDFFunc = func(context.Context, string, string, string) ([]string, error) {
		return nil, errNotStubbed
	}
	ExtractTextFunc = func(context.Context, string, Options) (*Extraction, error) {
		return nil, errNotStubbed
	}
	MailAccountsFunc = func(string) ([]MailAccount, error) { return nil, nil }
	FetchMailFunc = func(context.Context, MailFetchOptions) (*MailFetchResult, error) {
		return &MailFetchResult{}, nil
	}
}
