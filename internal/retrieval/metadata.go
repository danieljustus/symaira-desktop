package retrieval

import (
	"github.com/danieljustus/symaira-desktop/internal/retrieval/internal/engine"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

// SearchMetadataFromVault maps the selected vault contract fields to the
// canonical hybrid-search representation.
func SearchMetadataFromVault(doc *vault.Document) SearchMetadata {
	return engine.SearchMetadataFromVault(doc)
}
