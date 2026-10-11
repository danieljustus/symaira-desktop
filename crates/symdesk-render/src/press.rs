//! Built-in PDF profiles and Typst discovery. Rendering is not implemented here.
//! Contract source: `internal/pdf/internal/press/{profile,engine}.go`.
use crate::pdfops::{self, Context};
use serde::Serialize;
use std::{
    collections::HashMap,
    sync::{Mutex, OnceLock},
    time::{Duration, Instant},
};

#[derive(Clone, Debug, Serialize)]
pub struct Profile {
    pub name: &'static str,
    pub title: &'static str,
    pub description: &'static str,
    pub key_guarantees: &'static str,
    pub template: &'static str,
    pub engine: &'static str,
    #[serde(skip_serializing_if = "str::is_empty")]
    pub form: &'static str,
    #[serde(skip_serializing_if = "<[&str]>::is_empty")]
    pub pdf_standard: &'static [&'static str],
    pub reproducible: bool,
    pub required_fields: &'static [&'static str],
    pub stability: &'static str,
}

#[derive(Debug, Serialize)]
pub struct Capability {
    pub markdown: bool,
    pub pdf_a: bool,
    pub pdf_ua: bool,
    pub din_window: bool,
    pub reproducible: bool,
}

impl Profile {
    pub fn requires_accessibility(&self) -> bool {
        self.pdf_standard
            .iter()
            .any(|s| matches!(*s, "ua-1" | "ua-2"))
    }

    pub fn capability(&self) -> Capability {
        Capability {
            markdown: true,
            pdf_a: self.pdf_standard.iter().any(|s| s.starts_with("a-")),
            pdf_ua: self.requires_accessibility(),
            din_window: !self.form.is_empty(),
            reproducible: self.reproducible,
        }
    }
}

// Keep the shipped presentation order, descriptions and stability claims. In
// particular, selecting a profile is not evidence of PDF/A or DIN compliance.
static PROFILES: [Profile; 5] = [
    Profile {
        name: "behoerde",
        title: "Behörde (DIN 5008 + PDF/A + PDF/UA)",
        description: "Authority letter: DIN 5008 Form A plus archival (PDF/A-2a) and accessible (PDF/UA-1) output for E-Government / BITV 2.0 compliance.",
        key_guarantees: "DIN 5008 authority letter (Form A) + PDF/A-2a + PDF/UA-1",
        template: "behoerde.typ",
        engine: "typst",
        form: "A",
        pdf_standard: &["a-2a", "ua-1"],
        reproducible: false,
        required_fields: &["recipient", "title", "lang"],
        stability: "scaffold",
    },
    Profile {
        name: "brief",
        title: "Brief (DIN 5008)",
        description: "General German business letter. Defaults to DIN 5008 Form B (45 mm Briefkopf). Tagged PDF, no archival requirement.",
        key_guarantees: "DIN 5008 letter (Form B)",
        template: "brief.typ",
        engine: "typst",
        form: "B",
        pdf_standard: &[],
        reproducible: false,
        required_fields: &["recipient"],
        stability: "scaffold",
    },
    Profile {
        name: "meeting",
        title: "Meeting Minutes",
        description: "Accessible and archivable meeting minutes profile. Automatically formats metadata, participants, duration, location, and structured sections (Summary, Decisions, Action Items, Notes, Transcript).",
        key_guarantees: "meeting minutes + PDF/A-2a + PDF/UA-1",
        template: "meeting.typ",
        engine: "typst",
        form: "",
        pdf_standard: &["a-2a", "ua-1"],
        reproducible: false,
        required_fields: &["title", "lang", "date"],
        stability: "beta",
    },
    Profile {
        name: "rechnung",
        title: "Rechnung (DIN 5008)",
        description: "German invoice with windowed-envelope recipient, line items and VAT. Driven by a `data:` block. (Scaffold — VAT/GiroCode in Phase 4.)",
        key_guarantees: "data-driven German invoice (scaffold)",
        template: "rechnung.typ",
        engine: "typst",
        form: "B",
        pdf_standard: &[],
        reproducible: false,
        required_fields: &["recipient", "data"],
        stability: "scaffold",
    },
    Profile {
        name: "report",
        title: "Report / Bericht",
        description: "Multi-page report with cover page, automatic table of contents, running header/footer, page numbers, and themed headings.",
        key_guarantees: "cover page, table of contents, headers/footers, page numbers",
        template: "report.typ",
        engine: "typst",
        form: "",
        pdf_standard: &[],
        reproducible: false,
        required_fields: &["title"],
        stability: "beta",
    },
];

pub fn all() -> &'static [Profile] {
    &PROFILES
}

pub fn lookup(name: &str) -> Option<&'static Profile> {
    PROFILES.iter().find(|profile| profile.name == name)
}

/// Picker-facing profile, matching the reduced `internal/pdf.Profile` facade.
#[derive(Debug, Serialize)]
pub struct ProfileSummary {
    pub name: &'static str,
    pub title: &'static str,
    pub description: &'static str,
    pub stability: &'static str,
}

pub fn profiles() -> Vec<ProfileSummary> {
    all()
        .iter()
        .map(|p| ProfileSummary {
            name: p.name,
            title: p.title,
            description: p.description,
            stability: p.stability,
        })
        .collect()
}

pub const TYPST_INSTALL_HINT: &str = "typst not found on PATH. Install it with one of:\n  • macOS:   brew install typst\n  • Windows: winget install --id Typst.Typst\n  • Cargo:   cargo install typst-cli\n  • Binary:  https://github.com/typst/typst/releases (put it on PATH)";

#[derive(Clone, Debug, Serialize)]
pub struct EngineInfo {
    pub name: &'static str,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub path: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub version: String,
    pub available: bool,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub hint: String,
}

type ProbeCache = Mutex<HashMap<String, (Instant, EngineInfo)>>;
static PROBE_CACHE: OnceLock<ProbeCache> = OnceLock::new();
fn cache() -> &'static ProbeCache {
    PROBE_CACHE.get_or_init(Mutex::default)
}

/// Clear the discovery cache; primarily for isolated tests.
pub fn reset_probe_cache() {
    cache().lock().expect("Typst probe cache").clear();
}

/// Resolve an optional executable override and probe its version, best effort.
/// A failed `--version` does not make an executable unavailable. Positive and
/// negative lookups are cached by trimmed name for ten seconds, as in Go.
pub fn detect_typst(ctx: &Context, bin_override: &str) -> EngineInfo {
    let name = bin_override.trim();
    let name = if name.is_empty() { "typst" } else { name };
    if let Some((expiry, info)) = cache().lock().expect("Typst probe cache").get(name)
        && Instant::now() < *expiry
    {
        return info.clone();
    }
    let mut info = EngineInfo {
        name: "typst",
        path: String::new(),
        version: String::new(),
        available: false,
        hint: String::new(),
    };
    match pdfops::tool_path(name, "typst") {
        Err(_) => info.hint = TYPST_INSTALL_HINT.into(),
        Ok(path) => {
            info.available = true;
            info.path = path.to_string_lossy().into_owned();
            if let Ok(stdout) = pdfops::execute(ctx, &path, &["--version".into()]) {
                info.version = stdout.split_whitespace().nth(1).unwrap_or_default().into();
            }
        }
    }
    cache().lock().expect("Typst probe cache").insert(
        name.into(),
        (Instant::now() + Duration::from_secs(10), info.clone()),
    );
    info
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn registry_capabilities_and_case_sensitive_lookup() {
        assert_eq!(
            all().iter().map(|p| p.name).collect::<Vec<_>>(),
            ["behoerde", "brief", "meeting", "rechnung", "report"]
        );
        assert!(lookup("Report").is_none());
        assert!(lookup(" report ").is_none());
        assert!(lookup("behoerde").unwrap().capability().pdf_a);
        assert!(lookup("meeting").unwrap().requires_accessibility());
        assert!(!lookup("report").unwrap().requires_accessibility());
        let mut profile = lookup("report").unwrap().clone();
        profile.pdf_standard = &["ua-2"];
        profile.reproducible = true;
        let c = profile.capability();
        assert!(c.markdown && c.pdf_ua && c.reproducible && !c.pdf_a && !c.din_window);
    }
}
