package retrieval

import (
	"errors"
	"fmt"
	"path/filepath"
	"sync"
)

var errClientPoolClosed = errors.New("retrieval client pool is closed")

// ClientPool owns retrieval clients for the lifetime of a request-serving
// process. Clients are keyed by their canonical effective index path, so an
// explicit shared index is opened only once even when several vaults use it.
type ClientPool struct {
	mu      sync.Mutex
	clients map[string]*Client
	retired []*Client
	closed  bool
	opener  func(string) (*Client, error)
}

// NewClientPool creates an empty pool. The pool opens clients lazily and must
// be closed by its owning server.
func NewClientPool() *ClientPool {
	return &ClientPool{
		clients: make(map[string]*Client),
		opener:  OpenForVault,
	}
}

// newClientPoolWithOpener is a deterministic test seam for open-count tests.
func newClientPoolWithOpener(opener func(string) (*Client, error)) *ClientPool {
	pool := NewClientPool()
	pool.opener = opener
	return pool
}

// Get returns the client for vaultRoot, opening it once when necessary.
func (p *ClientPool) Get(vaultRoot string) (*Client, error) {
	if p == nil {
		return nil, errClientPoolClosed
	}
	canonicalRoot, err := canonicalPoolPath(vaultRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve retrieval vault root: %w", err)
	}
	indexPath, err := canonicalPoolIndexPath(canonicalRoot)
	if err != nil {
		return nil, err
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil, errClientPoolClosed
	}
	if client := p.clients[indexPath]; client != nil {
		return client, nil
	}
	client, err := p.opener(canonicalRoot)
	if err != nil {
		return nil, err
	}
	if client == nil {
		return nil, errors.New("retrieval client opener returned nil client")
	}
	p.clients[indexPath] = client
	return client, nil
}

// Invalidate removes client from the active cache without closing it. A
// caller may still be using the retired handle; Close releases it at server
// shutdown. The pointer check prevents an older failed request from removing
// a replacement opened after it.
func (p *ClientPool) Invalidate(vaultRoot string, client *Client) {
	if p == nil || client == nil {
		return
	}
	canonicalRoot, err := canonicalPoolPath(vaultRoot)
	if err != nil {
		return
	}
	indexPath, err := canonicalPoolIndexPath(canonicalRoot)
	if err != nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.clients[indexPath] != client {
		return
	}
	delete(p.clients, indexPath)
	p.retired = append(p.retired, client)
}

// Close closes active and retired clients exactly once. Retired clients stay
// open until this point because another request may still hold a pointer after
// an operation invalidated it.
func (p *ClientPool) Close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	clients := make([]*Client, 0, len(p.clients)+len(p.retired))
	for _, client := range p.clients {
		clients = append(clients, client)
	}
	clients = append(clients, p.retired...)
	p.clients = nil
	p.retired = nil
	p.mu.Unlock()

	var closeErr error
	for _, client := range clients {
		closeErr = errors.Join(closeErr, client.Close())
	}
	return closeErr
}

func canonicalPoolIndexPath(vaultRoot string) (string, error) {
	indexPath, err := IndexPathForVault(vaultRoot)
	if err != nil {
		return "", fmt.Errorf("resolve retrieval index path: %w", err)
	}
	indexPath, err = canonicalPoolPath(indexPath)
	if err != nil {
		return "", fmt.Errorf("resolve retrieval index path: %w", err)
	}
	return indexPath, nil
}

func canonicalPoolPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	candidate := absolute
	var suffix []string
	for {
		if resolved, resolveErr := filepath.EvalSymlinks(candidate); resolveErr == nil {
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return filepath.Clean(resolved), nil
		}
		parent := filepath.Dir(candidate)
		if parent == candidate {
			return filepath.Clean(absolute), nil
		}
		suffix = append(suffix, filepath.Base(candidate))
		candidate = parent
	}
}
