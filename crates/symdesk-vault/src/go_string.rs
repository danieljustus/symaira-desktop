//! Go 1.26.6 string compatibility helpers.
//!
//! That toolchain uses Unicode 15.0 tables. Rust 1.98 uses newer Unicode data
//! and its string lowercase operation applies full/contextual mappings, while
//! Go's `strings.ToLower` applies one simple rune mapping at a time.

use unicode_general_category::{GeneralCategory, get_general_category};

/// Quotes a valid UTF-8 string exactly like Go's `strconv.Quote`.
pub fn quote(value: &str) -> String {
    let mut out = String::with_capacity(value.len() + 2);
    out.push('"');
    for character in value.chars() {
        if character == '"' || character == '\\' {
            out.push('\\');
            out.push(character);
            continue;
        }
        if go_is_print(character) {
            out.push(character);
            continue;
        }
        match character {
            '\u{07}' => out.push_str("\\a"),
            '\u{08}' => out.push_str("\\b"),
            '\u{0c}' => out.push_str("\\f"),
            '\n' => out.push_str("\\n"),
            '\r' => out.push_str("\\r"),
            '\t' => out.push_str("\\t"),
            '\u{0b}' => out.push_str("\\v"),
            other if (other as u32) < 0x20 || other == '\u{7f}' => {
                out.push_str(&format!("\\x{:02x}", other as u32));
            }
            other if (other as u32) < 0x1_0000 => {
                out.push_str(&format!("\\u{:04x}", other as u32));
            }
            other => out.push_str(&format!("\\U{:08x}", other as u32)),
        }
    }
    out.push('"');
    out
}

/// Lowercases each rune with Go 1.26.6's Unicode 15.0 simple mapping.
pub fn lowercase(value: &str) -> String {
    value.chars().map(go_simple_lower).collect()
}

fn go_simple_lower(value: char) -> char {
    // Unicode's full lowercase mapping expands U+0130 to `i` + combining dot,
    // but Go's simple mapping is the single rune `i`.
    if value == '\u{0130}' {
        return 'i';
    }
    let mut mapping = value.to_lowercase();
    let Some(first) = mapping.next() else {
        return value;
    };
    if mapping.next().is_some() || rejects_case_edge(value, first) {
        value
    } else {
        first
    }
}

/// Excludes case edges introduced after Go 1.26.6's Unicode 15.0 tables, plus
/// the Turkic dotless-I edge that Go's simple folding intentionally omits.
pub(crate) fn rejects_case_edge(from: char, to: char) -> bool {
    let from = from as u32;
    let to = to as u32;
    matches!(
        (from, to),
        (0x0131, 0x0049)
            | (0x019b, 0xa7dc)
            | (0xa7dc, 0x019b)
            | (0x0264, 0xa7cb)
            | (0xa7cb, 0x0264)
            | (0x1c89, 0x1c8a)
            | (0x1c8a, 0x1c89)
            | (0xa7cc, 0xa7cd)
            | (0xa7cd, 0xa7cc)
            | (0xa7ce, 0xa7cf)
            | (0xa7cf, 0xa7ce)
            | (0xa7d2, 0xa7d3)
            | (0xa7d3, 0xa7d2)
            | (0xa7d4, 0xa7d5)
            | (0xa7d5, 0xa7d4)
            | (0xa7da, 0xa7db)
            | (0xa7db, 0xa7da)
    ) || ((0x10d50..=0x10d65).contains(&from) && to == from + 0x20)
        || ((0x10d70..=0x10d85).contains(&from) && to + 0x20 == from)
        || ((0x16ea0..=0x16eb8).contains(&from) && to == from + 0x1b)
        || ((0x16ebb..=0x16ed3).contains(&from) && to + 0x1b == from)
}

fn go_is_print(value: char) -> bool {
    if value == ' ' {
        return true;
    }
    if not_printable_in_unicode_15(value) {
        return false;
    }
    matches!(
        get_general_category(value),
        GeneralCategory::UppercaseLetter
            | GeneralCategory::LowercaseLetter
            | GeneralCategory::TitlecaseLetter
            | GeneralCategory::ModifierLetter
            | GeneralCategory::OtherLetter
            | GeneralCategory::NonspacingMark
            | GeneralCategory::SpacingMark
            | GeneralCategory::EnclosingMark
            | GeneralCategory::DecimalNumber
            | GeneralCategory::LetterNumber
            | GeneralCategory::OtherNumber
            | GeneralCategory::ConnectorPunctuation
            | GeneralCategory::DashPunctuation
            | GeneralCategory::OpenPunctuation
            | GeneralCategory::ClosePunctuation
            | GeneralCategory::InitialPunctuation
            | GeneralCategory::FinalPunctuation
            | GeneralCategory::OtherPunctuation
            | GeneralCategory::MathSymbol
            | GeneralCategory::CurrencySymbol
            | GeneralCategory::ModifierSymbol
            | GeneralCategory::OtherSymbol
    )
}

// `unicode-general-category` 1.1.0 uses Unicode 16.0. This is the exhaustive
// set of scalar ranges it classifies as printable that Go 1.26.6's generated
// Unicode 15.0 `strconv.IsPrint` table does not.
fn not_printable_in_unicode_15(value: char) -> bool {
    matches!(
        value as u32,
        0x0897
            | 0x1b4e..=0x1b4f
            | 0x1b7f
            | 0x1c89..=0x1c8a
            | 0x2427..=0x2429
            | 0x2ffc..=0x2fff
            | 0x31e4..=0x31e5
            | 0x31ef
            | 0xa7cb..=0xa7cd
            | 0xa7da..=0xa7dc
            | 0x105c0..=0x105f3
            | 0x10d40..=0x10d65
            | 0x10d69..=0x10d85
            | 0x10d8e..=0x10d8f
            | 0x10ec2..=0x10ec4
            | 0x10efc
            | 0x11380..=0x11389
            | 0x1138b
            | 0x1138e
            | 0x11390..=0x113b5
            | 0x113b7..=0x113c0
            | 0x113c2
            | 0x113c5
            | 0x113c7..=0x113ca
            | 0x113cc..=0x113d5
            | 0x113d7..=0x113d8
            | 0x113e1..=0x113e2
            | 0x116d0..=0x116e3
            | 0x11bc0..=0x11be1
            | 0x11bf0..=0x11bf9
            | 0x11f5a
            | 0x13460..=0x143fa
            | 0x16100..=0x16139
            | 0x16d40..=0x16d79
            | 0x18cff
            | 0x1cc00..=0x1ccf9
            | 0x1cd00..=0x1ceb3
            | 0x1e5d0..=0x1e5fa
            | 0x1e5ff
            | 0x1f8b2..=0x1f8bb
            | 0x1f8c0..=0x1f8c1
            | 0x1fa89
            | 0x1fa8f
            | 0x1fabe
            | 0x1fac6
            | 0x1fadc
            | 0x1fadf
            | 0x1fae9
            | 0x1fbcb..=0x1fbef
            | 0x2ebf0..=0x2ee5d
    )
}

#[cfg(test)]
mod tests {
    use super::{lowercase, quote};

    #[test]
    fn lowercase_matches_go_simple_per_rune_semantics() {
        assert_eq!(lowercase("İ"), "i");
        assert_eq!(lowercase("ΟΣ"), "οσ");
        assert_eq!(lowercase("ΟΣΟΣ"), "οσοσ");
        for value in ["\u{1c89}", "\u{10d50}", "\u{16ea0}"] {
            assert_eq!(lowercase(value), value);
        }
    }

    #[test]
    fn quote_matches_go_named_and_unicode_15_escapes() {
        assert_eq!(
            quote("\x07\x08\x0c\n\r\t\x0b\\\""),
            "\"\\a\\b\\f\\n\\r\\t\\v\\\\\\\"\""
        );
        assert_eq!(quote("\u{7f}"), "\"\\x7f\"");
        assert_eq!(quote("\u{85}"), "\"\\u0085\"");
        assert_eq!(quote("\u{a0}"), "\"\\u00a0\"");
        assert_eq!(quote("\u{897}"), "\"\\u0897\"");
        assert_eq!(quote("\u{2028}"), "\"\\u2028\"");
        assert_eq!(quote("\u{e0001}"), "\"\\U000e0001\"");
        assert_eq!(quote("\u{1cc00}"), "\"\\U0001cc00\"");
        assert_eq!(quote("é東"), "\"é東\"");
    }
}
