//! Last-line-of-defense masking of email- and phone-like substrings.
//!
//! The Go patterns run on RE2 where `\d` is ASCII-only and `\s` is
//! `[\t\n\f\r ]` (no `\v`, no Unicode spaces). The Rust `regex` crate defaults
//! to Unicode classes, so the classes are spelled out explicitly.
use regex::{NoExpand, Regex};
use std::sync::OnceLock;

struct Patterns {
    email: Regex,
    phone: Regex,
    ipv4: Regex,
}

fn patterns() -> &'static Patterns {
    static PATTERNS: OnceLock<Patterns> = OnceLock::new();
    PATTERNS.get_or_init(|| Patterns {
        email: Regex::new(r"[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}").unwrap(),
        phone: Regex::new(r"\+?[0-9][0-9\-\t\n\f\r ().]{7,}[0-9]").unwrap(),
        ipv4: Regex::new(r"^[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}$").unwrap(),
    })
}

/// Mask email- and phone-like substrings; dotted-quad IPv4 addresses that the
/// phone pattern happens to match are left untouched.
pub fn redact(s: &str) -> String {
    let p = patterns();
    let s = p.email.replace_all(s, NoExpand("[redacted-email]"));
    p.phone
        .replace_all(&s, |caps: &regex::Captures<'_>| {
            let m = &caps[0];
            if p.ipv4.is_match(m) {
                m.to_owned()
            } else {
                "[redacted-phone]".to_owned()
            }
        })
        .into_owned()
}
