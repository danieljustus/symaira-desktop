//! Port of Go `internal/draw/theme`: design tokens and the theme registry.
//!
//! Go keeps the registry in a process-global map guarded by a mutex; the port
//! models it as a [`ThemeRegistry`] value instead. [`resolve`] and [`names`]
//! read the pristine built-in registry (the one a Go process has before any
//! `Register` call); `Register` is [`ThemeRegistry::register`] on an owned
//! registry. Behavior per call is identical.

use std::collections::BTreeMap;
use std::sync::OnceLock;

use super::gotext::{to_lower, trim_space};
use super::ir::write_string;

/// Go `theme.DefaultFontFamily`.
pub const DEFAULT_FONT_FAMILY: &str =
    "Inter, -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif";

/// Go `theme.Theme`. `palette` is `None` for a nil Go slice (JSON `null`).
#[derive(Debug, Clone, PartialEq, Eq, Default)]
pub struct Theme {
    pub name: String,
    pub is_dark: bool,
    pub background: String,
    pub surface: String,
    pub surface_subtle: String,
    pub border: String,
    pub border_subtle: String,
    pub text: String,
    pub text_muted: String,
    pub primary: String,
    pub secondary: String,
    pub accent: String,
    pub edge: String,
    pub group_background: String,
    pub group_border: String,
    pub grid: String,
    pub palette: Option<Vec<String>>,
    pub font_family: String,
}

impl Theme {
    /// Go `json.Marshal(Theme)`: fixed field order, no `omitempty`.
    pub fn to_json(&self) -> String {
        fn text(out: &mut String, key: &str, value: &str) {
            out.push('"');
            out.push_str(key);
            out.push_str("\":");
            write_string(out, value);
            out.push(',');
        }
        let mut out = String::from("{");
        text(&mut out, "name", &self.name);
        out.push_str("\"is_dark\":");
        out.push_str(if self.is_dark { "true," } else { "false," });
        text(&mut out, "background", &self.background);
        text(&mut out, "surface", &self.surface);
        text(&mut out, "surface_subtle", &self.surface_subtle);
        text(&mut out, "border", &self.border);
        text(&mut out, "border_subtle", &self.border_subtle);
        text(&mut out, "text", &self.text);
        text(&mut out, "text_muted", &self.text_muted);
        text(&mut out, "primary", &self.primary);
        text(&mut out, "secondary", &self.secondary);
        text(&mut out, "accent", &self.accent);
        text(&mut out, "edge", &self.edge);
        text(&mut out, "group_background", &self.group_background);
        text(&mut out, "group_border", &self.group_border);
        text(&mut out, "grid", &self.grid);
        out.push_str("\"palette\":");
        match &self.palette {
            None => out.push_str("null"),
            Some(colors) => {
                out.push('[');
                for (index, color) in colors.iter().enumerate() {
                    if index > 0 {
                        out.push(',');
                    }
                    write_string(&mut out, color);
                }
                out.push(']');
            }
        }
        out.push_str(",\"font_family\":");
        write_string(&mut out, &self.font_family);
        out.push('}');
        out
    }
}

/// The theme registry (Go `registry` map keyed by lowercased name).
#[derive(Debug, Clone)]
pub struct ThemeRegistry {
    themes: BTreeMap<String, Theme>,
}

fn builtin_themes() -> Vec<Theme> {
    vec![
        Theme {
            name: "symaira-dark".to_owned(),
            is_dark: true,
            background: "#0D0C0A".to_owned(),
            surface: "#1A1916".to_owned(),
            surface_subtle: "#24221D".to_owned(),
            border: "#2E2C28".to_owned(),
            border_subtle: "#3D3A35".to_owned(),
            text: "#F5F4F0".to_owned(),
            text_muted: "#9E9B90".to_owned(),
            primary: "#E5C397".to_owned(),
            secondary: "#F8E6CD".to_owned(),
            accent: "#70A5D6".to_owned(),
            edge: "#B8B4A8".to_owned(),
            group_background: "#141310".to_owned(),
            group_border: "#3D3A35".to_owned(),
            grid: "#22201C".to_owned(),
            palette: Some(vec![
                "#E5C397".to_owned(),
                "#70A5D6".to_owned(),
                "#98C379".to_owned(),
                "#E06C75".to_owned(),
                "#D19A66".to_owned(),
                "#C678DD".to_owned(),
                "#56B6C2".to_owned(),
                "#E5C07B".to_owned(),
            ]),
            font_family: DEFAULT_FONT_FAMILY.to_owned(),
        },
        Theme {
            name: "symaira-light".to_owned(),
            is_dark: false,
            background: "#FFFFFF".to_owned(),
            surface: "#F8F9FA".to_owned(),
            surface_subtle: "#F1F3F5".to_owned(),
            border: "#D0D5DD".to_owned(),
            border_subtle: "#E4E7EC".to_owned(),
            text: "#1A1D20".to_owned(),
            text_muted: "#6C757D".to_owned(),
            primary: "#1E3D59".to_owned(),
            secondary: "#17B978".to_owned(),
            accent: "#D97706".to_owned(),
            edge: "#4B5563".to_owned(),
            group_background: "#F9FAFB".to_owned(),
            group_border: "#D1D5DB".to_owned(),
            grid: "#E5E7EB".to_owned(),
            palette: Some(vec![
                "#1E3D59".to_owned(),
                "#17B978".to_owned(),
                "#D97706".to_owned(),
                "#8B5CF6".to_owned(),
                "#EC4899".to_owned(),
                "#3B82F6".to_owned(),
                "#10B981".to_owned(),
                "#F59E0B".to_owned(),
            ]),
            font_family: DEFAULT_FONT_FAMILY.to_owned(),
        },
        Theme {
            name: "report".to_owned(),
            is_dark: false,
            background: "#FFFFFF".to_owned(),
            surface: "#F8FAFC".to_owned(),
            surface_subtle: "#F1F5F9".to_owned(),
            border: "#CBD5E1".to_owned(),
            border_subtle: "#E2E8F0".to_owned(),
            text: "#1E293B".to_owned(),
            text_muted: "#64748B".to_owned(),
            primary: "#1F4E79".to_owned(),
            secondary: "#3B82F6".to_owned(),
            accent: "#0284C7".to_owned(),
            edge: "#475569".to_owned(),
            group_background: "#F8FAFC".to_owned(),
            group_border: "#94A3B8".to_owned(),
            grid: "#E2E8F0".to_owned(),
            palette: Some(vec![
                "#1F4E79".to_owned(),
                "#3B82F6".to_owned(),
                "#0284C7".to_owned(),
                "#059669".to_owned(),
                "#D97706".to_owned(),
                "#7C3AED".to_owned(),
                "#DB2777".to_owned(),
                "#475569".to_owned(),
            ]),
            font_family: DEFAULT_FONT_FAMILY.to_owned(),
        },
        Theme {
            name: "meeting".to_owned(),
            is_dark: false,
            background: "#FFFFFF".to_owned(),
            surface: "#F9FAFB".to_owned(),
            surface_subtle: "#F3F4F6".to_owned(),
            border: "#D1D5DB".to_owned(),
            border_subtle: "#E5E7EB".to_owned(),
            text: "#111827".to_owned(),
            text_muted: "#6B7280".to_owned(),
            primary: "#1E3D59".to_owned(),
            secondary: "#10B981".to_owned(),
            accent: "#F59E0B".to_owned(),
            edge: "#374151".to_owned(),
            group_background: "#F9FAFB".to_owned(),
            group_border: "#9CA3AF".to_owned(),
            grid: "#E5E7EB".to_owned(),
            palette: Some(vec![
                "#1E3D59".to_owned(),
                "#10B981".to_owned(),
                "#F59E0B".to_owned(),
                "#6366F1".to_owned(),
                "#EC4899".to_owned(),
                "#14B8A6".to_owned(),
                "#8B5CF6".to_owned(),
                "#F97316".to_owned(),
            ]),
            font_family: DEFAULT_FONT_FAMILY.to_owned(),
        },
        Theme {
            name: "behoerde".to_owned(),
            is_dark: false,
            background: "#FFFFFF".to_owned(),
            surface: "#FFFFFF".to_owned(),
            surface_subtle: "#F5F5F5".to_owned(),
            border: "#737373".to_owned(),
            border_subtle: "#A3A3A3".to_owned(),
            text: "#171717".to_owned(),
            text_muted: "#525252".to_owned(),
            primary: "#262626".to_owned(),
            secondary: "#525252".to_owned(),
            accent: "#171717".to_owned(),
            edge: "#262626".to_owned(),
            group_background: "#FAFAFA".to_owned(),
            group_border: "#737373".to_owned(),
            grid: "#E5E5E5".to_owned(),
            palette: Some(vec![
                "#262626".to_owned(),
                "#525252".to_owned(),
                "#737373".to_owned(),
                "#A3A3A3".to_owned(),
                "#171717".to_owned(),
                "#404040".to_owned(),
                "#78716C".to_owned(),
                "#44403C".to_owned(),
            ]),
            font_family: DEFAULT_FONT_FAMILY.to_owned(),
        },
        Theme {
            name: "brief".to_owned(),
            is_dark: false,
            background: "#FFFFFF".to_owned(),
            surface: "#FFFFFF".to_owned(),
            surface_subtle: "#FAFAFA".to_owned(),
            border: "#D4D4D4".to_owned(),
            border_subtle: "#E5E5E5".to_owned(),
            text: "#0A0A0A".to_owned(),
            text_muted: "#737373".to_owned(),
            primary: "#0A0A0A".to_owned(),
            secondary: "#404040".to_owned(),
            accent: "#171717".to_owned(),
            edge: "#525252".to_owned(),
            group_background: "#FAFAFA".to_owned(),
            group_border: "#D4D4D4".to_owned(),
            grid: "#F5F5F5".to_owned(),
            palette: Some(vec![
                "#0A0A0A".to_owned(),
                "#404040".to_owned(),
                "#737373".to_owned(),
                "#2563EB".to_owned(),
                "#059669".to_owned(),
                "#DC2626".to_owned(),
                "#D97706".to_owned(),
                "#7C3AED".to_owned(),
            ]),
            font_family: DEFAULT_FONT_FAMILY.to_owned(),
        },
    ]
}

impl ThemeRegistry {
    /// A registry holding exactly the built-in themes.
    pub fn builtin() -> ThemeRegistry {
        let themes = builtin_themes()
            .into_iter()
            .map(|theme| (theme.name.clone(), theme))
            .collect();
        ThemeRegistry { themes }
    }

    /// Go `theme.Resolve`: case-insensitive, space-trimmed lookup; the
    /// synonyms `dark`/`light` and every unknown name fall back to the
    /// symaira themes (default `symaira-dark`).
    pub fn resolve(&self, name: &str) -> Theme {
        let norm = to_lower(trim_space(name));
        if let Some(theme) = self.themes.get(&norm) {
            return theme.clone();
        }
        let key = match norm.as_str() {
            "light" => "symaira-light",
            _ => "symaira-dark",
        };
        self.themes.get(key).cloned().unwrap_or_default()
    }

    /// Go `theme.Register`: an empty name is ignored, an empty font family
    /// takes the default, the key is the lowercased name (not trimmed).
    pub fn register(&mut self, mut theme: Theme) {
        if theme.name.is_empty() {
            return;
        }
        if theme.font_family.is_empty() {
            theme.font_family = DEFAULT_FONT_FAMILY.to_owned();
        }
        self.themes.insert(to_lower(&theme.name), theme);
    }

    /// Go `theme.Names`: registered keys in sorted (byte) order.
    pub fn names(&self) -> Vec<String> {
        self.themes.keys().cloned().collect()
    }
}

fn pristine() -> &'static ThemeRegistry {
    static REGISTRY: OnceLock<ThemeRegistry> = OnceLock::new();
    REGISTRY.get_or_init(ThemeRegistry::builtin)
}

/// Go `theme.Resolve` against the built-in registry.
pub fn resolve(name: &str) -> Theme {
    pristine().resolve(name)
}

/// Go `theme.Names` against the built-in registry.
pub fn names() -> Vec<String> {
    pristine().names()
}
