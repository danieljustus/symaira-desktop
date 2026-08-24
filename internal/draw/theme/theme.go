// Package theme defines shared design tokens, color palettes, and profile-matched
// styling for SymDraw diagrams across light/dark modes and SymPrint profiles.
package theme

import (
	"sort"
	"strings"
	"sync"
)

// DefaultFontFamily specifies the universal font stack for SymDraw SVGs.
const DefaultFontFamily = "Inter, -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif"

// Theme contains semantic color tokens and styling parameters for diagram rendering.
type Theme struct {
	Name            string   `json:"name"`
	IsDark          bool     `json:"is_dark"`
	Background      string   `json:"background"`
	Surface         string   `json:"surface"`
	SurfaceSubtle   string   `json:"surface_subtle"`
	Border          string   `json:"border"`
	BorderSubtle    string   `json:"border_subtle"`
	Text            string   `json:"text"`
	TextMuted       string   `json:"text_muted"`
	Primary         string   `json:"primary"`
	Secondary       string   `json:"secondary"`
	Accent          string   `json:"accent"`
	Edge            string   `json:"edge"`
	GroupBackground string   `json:"group_background"`
	GroupBorder     string   `json:"group_border"`
	Grid            string   `json:"grid"`
	Palette         []string `json:"palette"`
	FontFamily      string   `json:"font_family"`
}

var (
	registryMu sync.RWMutex
	registry   = map[string]Theme{
		"symaira-dark": {
			Name:            "symaira-dark",
			IsDark:          true,
			Background:      "#0D0C0A",
			Surface:         "#1A1916",
			SurfaceSubtle:   "#24221D",
			Border:          "#2E2C28",
			BorderSubtle:    "#3D3A35",
			Text:            "#F5F4F0",
			TextMuted:       "#9E9B90",
			Primary:         "#E5C397",
			Secondary:       "#F8E6CD",
			Accent:          "#70A5D6",
			Edge:            "#B8B4A8",
			GroupBackground: "#141310",
			GroupBorder:     "#3D3A35",
			Grid:            "#22201C",
			Palette: []string{
				"#E5C397", "#70A5D6", "#98C379", "#E06C75",
				"#D19A66", "#C678DD", "#56B6C2", "#E5C07B",
			},
			FontFamily: DefaultFontFamily,
		},
		"symaira-light": {
			Name:            "symaira-light",
			IsDark:          false,
			Background:      "#FFFFFF",
			Surface:         "#F8F9FA",
			SurfaceSubtle:   "#F1F3F5",
			Border:          "#D0D5DD",
			BorderSubtle:    "#E4E7EC",
			Text:            "#1A1D20",
			TextMuted:       "#6C757D",
			Primary:         "#1E3D59",
			Secondary:       "#17B978",
			Accent:          "#D97706",
			Edge:            "#4B5563",
			GroupBackground: "#F9FAFB",
			GroupBorder:     "#D1D5DB",
			Grid:            "#E5E7EB",
			Palette: []string{
				"#1E3D59", "#17B978", "#D97706", "#8B5CF6",
				"#EC4899", "#3B82F6", "#10B981", "#F59E0B",
			},
			FontFamily: DefaultFontFamily,
		},
		"report": {
			Name:            "report",
			IsDark:          false,
			Background:      "#FFFFFF",
			Surface:         "#F8FAFC",
			SurfaceSubtle:   "#F1F5F9",
			Border:          "#CBD5E1",
			BorderSubtle:    "#E2E8F0",
			Text:            "#1E293B",
			TextMuted:       "#64748B",
			Primary:         "#1F4E79",
			Secondary:       "#3B82F6",
			Accent:          "#0284C7",
			Edge:            "#475569",
			GroupBackground: "#F8FAFC",
			GroupBorder:     "#94A3B8",
			Grid:            "#E2E8F0",
			Palette: []string{
				"#1F4E79", "#3B82F6", "#0284C7", "#059669",
				"#D97706", "#7C3AED", "#DB2777", "#475569",
			},
			FontFamily: DefaultFontFamily,
		},
		"meeting": {
			Name:            "meeting",
			IsDark:          false,
			Background:      "#FFFFFF",
			Surface:         "#F9FAFB",
			SurfaceSubtle:   "#F3F4F6",
			Border:          "#D1D5DB",
			BorderSubtle:    "#E5E7EB",
			Text:            "#111827",
			TextMuted:       "#6B7280",
			Primary:         "#1E3D59",
			Secondary:       "#10B981",
			Accent:          "#F59E0B",
			Edge:            "#374151",
			GroupBackground: "#F9FAFB",
			GroupBorder:     "#9CA3AF",
			Grid:            "#E5E7EB",
			Palette: []string{
				"#1E3D59", "#10B981", "#F59E0B", "#6366F1",
				"#EC4899", "#14B8A6", "#8B5CF6", "#F97316",
			},
			FontFamily: DefaultFontFamily,
		},
		"behoerde": {
			Name:            "behoerde",
			IsDark:          false,
			Background:      "#FFFFFF",
			Surface:         "#FFFFFF",
			SurfaceSubtle:   "#F5F5F5",
			Border:          "#737373",
			BorderSubtle:    "#A3A3A3",
			Text:            "#171717",
			TextMuted:       "#525252",
			Primary:         "#262626",
			Secondary:       "#525252",
			Accent:          "#171717",
			Edge:            "#262626",
			GroupBackground: "#FAFAFA",
			GroupBorder:     "#737373",
			Grid:            "#E5E5E5",
			Palette: []string{
				"#262626", "#525252", "#737373", "#A3A3A3",
				"#171717", "#404040", "#78716C", "#44403C",
			},
			FontFamily: DefaultFontFamily,
		},
		"brief": {
			Name:            "brief",
			IsDark:          false,
			Background:      "#FFFFFF",
			Surface:         "#FFFFFF",
			SurfaceSubtle:   "#FAFAFA",
			Border:          "#D4D4D4",
			BorderSubtle:    "#E5E5E5",
			Text:            "#0A0A0A",
			TextMuted:       "#737373",
			Primary:         "#0A0A0A",
			Secondary:       "#404040",
			Accent:          "#171717",
			Edge:            "#525252",
			GroupBackground: "#FAFAFA",
			GroupBorder:     "#D4D4D4",
			Grid:            "#F5F5F5",
			Palette: []string{
				"#0A0A0A", "#404040", "#737373", "#2563EB",
				"#059669", "#DC2626", "#D97706", "#7C3AED",
			},
			FontFamily: DefaultFontFamily,
		},
	}
)

// Resolve returns the Theme matching name (case-insensitive), or default "symaira-dark" if unknown.
func Resolve(name string) Theme {
	registryMu.RLock()
	defer registryMu.RUnlock()

	norm := strings.ToLower(strings.TrimSpace(name))
	if t, ok := registry[norm]; ok {
		return t
	}

	// Synonyms
	switch norm {
	case "dark":
		return registry["symaira-dark"]
	case "light":
		return registry["symaira-light"]
	default:
		return registry["symaira-dark"]
	}
}

// Register adds or replaces a theme in the registry.
func Register(t Theme) {
	if t.Name == "" {
		return
	}
	if t.FontFamily == "" {
		t.FontFamily = DefaultFontFamily
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[strings.ToLower(t.Name)] = t
}

// Names returns a sorted list of all registered theme names.
func Names() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()

	keys := make([]string, 0, len(registry))
	for k := range registry {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
