package retrieval

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
)

var errClientPoolClosed = errors.New("retrieval client pool is closed")

type clientPoolEntry struct {
	client  *Client
	refs    int
	retired bool
}

// ClientPool owns retrieval clients for one request-serving process. Active
// clients are keyed by canonical effective index path. Operations borrow a
// lease; invalidated clients close as soon as their last lease is released.
type ClientPool struct {
	mu        sync.Mutex
	cond      *sync.Cond
	clients   map[string]*clientPoolEntry
	retired   map[*clientPoolEntry]struct{}
	closed    bool
	closeDone bool
	closing   int
	closeErr  error
	opener    func(string) (*Client, error)
}

// NewClientPool creates an empty pool. The owning server must call Close after
// it has stopped accepting requests.
func NewClientPool() *ClientPool {
	pool := &ClientPool{
		clients: make(map[string]*clientPoolEntry),
		retired: make(map[*clientPoolEntry]struct{}),
		opener:  OpenForVault,
	}
	pool.cond = sync.NewCond(&pool.mu)
	return pool
}

// newClientPoolWithOpener is a deterministic test seam for open-count tests.
func newClientPoolWithOpener(opener func(string) (*Client, error)) *ClientPool {
	pool := NewClientPool()
	pool.opener = opener
	return pool
}

// Acquire returns the effective client plus an idempotent release function.
// The release function must be called after the retrieval operation finishes.
func (p *ClientPool) Acquire(vaultRoot string) (*Client, func(), error) {
	if p == nil {
		return nil, func() {}, errClientPoolClosed
	}
	if strings.TrimSpace(vaultRoot) == "" {
		return nil, func() {}, errors.New("retrieval client pool requires a vault root")
	}
	canonicalRoot, err := canonicalPoolPath(vaultRoot)
	if err != nil {
		return nil, func() {}, fmt.Errorf("resolve retrieval vault root: %w", err)
	}
	requestedKey, err := canonicalPoolIndexPath(canonicalRoot)
	if err != nil {
		return nil, func() {}, err
	}

	p.mu.Lock()
	p.ensureInitializedLocked()
	if p.closed {
		p.mu.Unlock()
		return nil, func() {}, errClientPoolClosed
	}
	if entry := p.clients[requestedKey]; entry != nil {
		entry.refs++
		p.mu.Unlock()
		return entry.client, p.releaseFunc(entry), nil
	}

	// Opening under the pool mutex deliberately serializes first use. It keeps
	// concurrent request bursts from deserializing the same index repeatedly.
	client, err := p.opener(canonicalRoot)
	if err != nil {
		p.mu.Unlock()
		return nil, func() {}, err
	}
	if client == nil || strings.TrimSpace(client.indexPath) == "" {
		p.mu.Unlock()
		if client != nil {
			_ = client.Close()
		}
		return nil, func() {}, errors.New("retrieval client opener returned an invalid client")
	}
	actualKey, err := canonicalPoolPath(client.indexPath)
	if err != nil {
		p.mu.Unlock()
		_ = client.Close()
		return nil, func() {}, fmt.Errorf("resolve opened retrieval index path: %w", err)
	}
	if entry := p.clients[actualKey]; entry != nil {
		entry.refs++
		p.clients[requestedKey] = entry
		p.mu.Unlock()
		_ = client.Close()
		return entry.client, p.releaseFunc(entry), nil
	}

	entry := &clientPoolEntry{client: client, refs: 1}
	p.clients[requestedKey] = entry
	p.clients[actualKey] = entry
	p.mu.Unlock()
	return client, p.releaseFunc(entry), nil
}

// Invalidate retires exactly client, regardless of later configuration
// changes. The handle closes only after all in-flight leases release it.
func (p *ClientPool) Invalidate(client *Client) {
	if p == nil || client == nil {
		return
	}
	p.mu.Lock()
	p.ensureInitializedLocked()
	if p.closed {
		p.mu.Unlock()
		return
	}
	var entry *clientPoolEntry
	for _, candidate := range p.clients {
		if candidate.client == client {
			entry = candidate
			break
		}
	}
	if entry == nil || entry.retired {
		p.mu.Unlock()
		return
	}
	p.retireLocked(entry)
	closeNow := p.startCloseIfIdleLocked(entry)
	p.mu.Unlock()
	if closeNow {
		p.finishClose(entry)
	}
}

// Close prevents new leases, waits for in-flight operations, and closes every
// active or retired client. Concurrent calls receive the same aggregated error.
func (p *ClientPool) Close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	p.ensureInitializedLocked()
	if p.closed {
		for !p.closeDone {
			p.cond.Wait()
		}
		err := p.closeErr
		p.mu.Unlock()
		return err
	}
	p.closed = true
	seen := make(map[*clientPoolEntry]struct{})
	for _, entry := range p.clients {
		seen[entry] = struct{}{}
	}
	for entry := range seen {
		p.retireLocked(entry)
	}
	p.clients = nil

	for {
		var closable []*clientPoolEntry
		for entry := range p.retired {
			if p.startCloseIfIdleLocked(entry) {
				closable = append(closable, entry)
			}
		}
		if len(closable) > 0 {
			p.mu.Unlock()
			for _, entry := range closable {
				p.finishClose(entry)
			}
			p.mu.Lock()
			continue
		}
		if len(p.retired) == 0 && p.closing == 0 {
			p.closeDone = true
			p.cond.Broadcast()
			err := p.closeErr
			p.mu.Unlock()
			return err
		}
		p.cond.Wait()
	}
}

func (p *ClientPool) releaseFunc(entry *clientPoolEntry) func() {
	var once sync.Once
	return func() {
		once.Do(func() { p.release(entry) })
	}
}

func (p *ClientPool) release(entry *clientPoolEntry) {
	p.mu.Lock()
	if entry.refs > 0 {
		entry.refs--
	}
	closeNow := p.startCloseIfIdleLocked(entry)
	p.cond.Broadcast()
	p.mu.Unlock()
	if closeNow {
		p.finishClose(entry)
	}
}

func (p *ClientPool) retireLocked(entry *clientPoolEntry) {
	if entry.retired {
		return
	}
	entry.retired = true
	for key, active := range p.clients {
		if active == entry {
			delete(p.clients, key)
		}
	}
	p.retired[entry] = struct{}{}
}

func (p *ClientPool) startCloseIfIdleLocked(entry *clientPoolEntry) bool {
	if !entry.retired || entry.refs != 0 {
		return false
	}
	if _, ok := p.retired[entry]; !ok {
		return false
	}
	delete(p.retired, entry)
	p.closing++
	return true
}

func (p *ClientPool) finishClose(entry *clientPoolEntry) {
	err := entry.client.Close()
	p.mu.Lock()
	p.closeErr = errors.Join(p.closeErr, err)
	p.closing--
	p.cond.Broadcast()
	p.mu.Unlock()
}

func (p *ClientPool) ensureInitializedLocked() {
	if p.cond == nil {
		p.cond = sync.NewCond(&p.mu)
	}
	if p.clients == nil && !p.closed {
		p.clients = make(map[string]*clientPoolEntry)
	}
	if p.retired == nil {
		p.retired = make(map[*clientPoolEntry]struct{})
	}
	if p.opener == nil {
		p.opener = OpenForVault
	}
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
