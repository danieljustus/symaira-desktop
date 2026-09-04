package selfhost

import (
	"fmt"

	"github.com/danieljustus/symaira-desktop/internal/service"
)

// newService creates a request-scoped service that borrows the server-owned
// sidecar and retrieval pool. Neither resource is closed by the service.
func (s *Server) newService() (*service.Service, error) {
	if s.retrievalPool == nil {
		return nil, fmt.Errorf("retrieval client pool is unavailable")
	}
	return service.NewWithRetrievalPool(s.cfg.VaultRoot, s.db, s.retrievalPool), nil
}
