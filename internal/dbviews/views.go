package dbviews

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

type Filter struct {
	Key      string `json:"key"`
	Operator string `json:"operator"`
	Value    string `json:"value"`
}

// FilterGroup allows views to express nested all/any conditions. Filters is
// deliberately kept on View for compatibility with views written before
// groups were introduced.
type FilterGroup struct {
	Operator string        `json:"operator"`
	Filters  []Filter      `json:"filters,omitempty"`
	Groups   []FilterGroup `json:"groups,omitempty"`
}

// Template describes the note template and frontmatter values used when a
// user creates an entry from a saved view.
type Template struct {
	Ref      string            `json:"ref,omitempty"`
	Defaults map[string]string `json:"defaults,omitempty"`
}

type Sort struct {
	Key       string `json:"key"`
	Ascending bool   `json:"ascending"`
}

type ComputedColumn struct {
	Formula string `json:"formula,omitempty"`
	Rollup  string `json:"rollup,omitempty"`
}

type View struct {
	ID           string                    `json:"id"`
	Name         string                    `json:"name"`
	Type         string                    `json:"type"`
	GroupBy      string                    `json:"group_by,omitempty"`
	DateProperty string                    `json:"date_property,omitempty"`
	Computed     map[string]ComputedColumn `json:"computed,omitempty"`
	Filters      []Filter                  `json:"filters"`
	FilterGroup  *FilterGroup              `json:"filter_group,omitempty"`
	Sorts        []Sort                    `json:"sorts"`
	Columns      []string                  `json:"columns"`
	Source       string                    `json:"source,omitempty"`
	Template     *Template                 `json:"template,omitempty"`
}

func (m *Manager) Delete(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	data, err := os.ReadFile(m.viewsFile())
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("view not found")
		}
		return err
	}
	var views []View
	if err := json.Unmarshal(data, &views); err != nil {
		return err
	}
	filtered := make([]View, 0, len(views))
	found := false
	for _, view := range views {
		if view.ID == id {
			found = true
			continue
		}
		filtered = append(filtered, view)
	}
	if !found {
		return fmt.Errorf("view not found")
	}
	out, err := json.MarshalIndent(filtered, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(m.viewsFile(), out, 0644)
}

type Manager struct {
	vaultRoot string
	mu        sync.Mutex
}

func NewManager(vaultRoot string) *Manager {
	return &Manager{vaultRoot: vaultRoot}
}

func (m *Manager) viewsFile() string {
	return filepath.Join(m.vaultRoot, ".symdesk", "views.json")
}

func (m *Manager) ensureDir() error {
	dir := filepath.Join(m.vaultRoot, ".symdesk")
	return os.MkdirAll(dir, 0755)
}

func (m *Manager) List() ([]View, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	data, err := os.ReadFile(m.viewsFile())
	if err != nil {
		if os.IsNotExist(err) {
			return []View{}, nil
		}
		return nil, err
	}

	var views []View
	if err := json.Unmarshal(data, &views); err != nil {
		return nil, err
	}
	return views, nil
}

func (m *Manager) Get(id string) (*View, error) {
	views, err := m.List()
	if err != nil {
		return nil, err
	}
	for _, v := range views {
		if v.ID == id {
			return &v, nil
		}
	}
	return nil, fmt.Errorf("view not found")
}

func (m *Manager) Save(view View) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Need to read directly without locking again
	var views []View
	data, err := os.ReadFile(m.viewsFile())
	if err == nil {
		_ = json.Unmarshal(data, &views)
	}

	found := false
	for i, v := range views {
		if v.ID == view.ID {
			views[i] = view
			found = true
			break
		}
	}
	if !found {
		if view.ID == "" {
			view.ID = fmt.Sprintf("view_%d", len(views)+1)
		}
		views = append(views, view)
	}

	if err := m.ensureDir(); err != nil {
		return err
	}

	out, err := json.MarshalIndent(views, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(m.viewsFile(), out, 0644)
}
