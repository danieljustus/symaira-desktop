// Package testsupport neutralizes the side effects that the absorbed tools
// would otherwise have when a test exercises the SymDesk service layer.
//
// Since the repo consolidation, retrieval (symseek) and the contact store
// (symrelate) run in-process rather than as sibling binaries a test could
// simply keep off $PATH. Their state lives under the user's home directory,
// so an unguarded `go test ./...` would write into the developer's real
// search index and read their real contacts.
//
// Every test package that constructs a service.Service therefore calls
// IsolateSideEffects from TestMain. A test that wants results back from one
// of these seams overrides that specific seam itself, after this call.
package testsupport

import (
	"context"

	"github.com/danieljustus/symaira-desktop/internal/contacts"
	"github.com/danieljustus/symaira-desktop/internal/pdf"
	"github.com/danieljustus/symaira-desktop/internal/retrieval"
	printapi "github.com/danieljustus/symaira-print/api"
)

// IsolateSideEffects points the in-process seams at inert doubles: the
// hybrid index accepts writes and returns nothing, the contact store reports
// itself unavailable, and the PDF renderer reports no engine. It is safe to
// call more than once.
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
}
