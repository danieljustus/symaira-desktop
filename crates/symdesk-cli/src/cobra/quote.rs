//! Go `fmt`/`strconv` quoting used by Cobra and pflag diagnostics.
#[path = "../../../symroom-cli/src/go_isprint.rs"]
mod go_isprint;

fn push_rune(out: &mut String, ch: char, quote: char) {
    match ch {
        ch if ch == quote => {
            out.push('\\');
            out.push(ch);
        }
        '\\' => out.push_str("\\\\"),
        '\u{7}' => out.push_str("\\a"),
        '\u{8}' => out.push_str("\\b"),
        '\u{c}' => out.push_str("\\f"),
        '\n' => out.push_str("\\n"),
        '\r' => out.push_str("\\r"),
        '\t' => out.push_str("\\t"),
        '\u{b}' => out.push_str("\\v"),
        ch if (ch as u32) < 0x20 || ch as u32 == 0x7f => {
            out.push_str(&format!("\\x{:02x}", ch as u32));
        }
        ch if !go_isprint::is_print(ch) => {
            if (ch as u32) < 0x10000 {
                out.push_str(&format!("\\u{:04x}", ch as u32));
            } else {
                out.push_str(&format!("\\U{:08x}", ch as u32));
            }
        }
        ch => out.push(ch),
    }
}

/// `%q` for a string (`strconv.Quote`).
pub fn go_quote(text: &str) -> String {
    let mut out = String::with_capacity(text.len() + 2);
    out.push('"');
    for ch in text.chars() {
        push_rune(&mut out, ch, '"');
    }
    out.push('"');
    out
}

/// `%q` for a single rune (`strconv.QuoteRune`).
pub fn go_quote_rune(ch: char) -> String {
    let mut out = String::from("'");
    push_rune(&mut out, ch, '\'');
    out.push('\'');
    out
}

/// `%#q`: a raw string when `strconv.CanBackquote`, otherwise `%q`.
pub fn go_sharp_quote(text: &str) -> String {
    // strconv.CanBackquote: multi-byte runes pass except the BOM; ASCII
    // controls (but tab), DEL and the backquote itself do not.
    let can_backquote = text.chars().all(|ch| {
        if ch.len_utf8() > 1 {
            ch != '\u{feff}'
        } else {
            !((ch < ' ' && ch != '\t') || ch == '`' || ch == '\u{7f}')
        }
    });
    if can_backquote {
        format!("`{text}`")
    } else {
        go_quote(text)
    }
}

/// `json.Marshal` of a string (HTML-safe, as `encoding/json` does by default).
pub fn go_json_string(text: &str) -> String {
    let mut out = String::with_capacity(text.len() + 2);
    out.push('"');
    for ch in text.chars() {
        match ch {
            '"' => out.push_str("\\\""),
            '\\' => out.push_str("\\\\"),
            '\n' => out.push_str("\\n"),
            '\r' => out.push_str("\\r"),
            '\t' => out.push_str("\\t"),
            '\u{8}' => out.push_str("\\b"),
            '\u{c}' => out.push_str("\\f"),
            ch if (ch as u32) < 0x20 || matches!(ch, '<' | '>' | '&' | '\u{2028}' | '\u{2029}') => {
                out.push_str(&format!("\\u{:04x}", ch as u32));
            }
            ch => out.push(ch),
        }
    }
    out.push('"');
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn quotes_like_go() {
        assert_eq!(
            go_quote("a\"b\\c\n\u{1}é\u{a0}"),
            "\"a\\\"b\\\\c\\n\\x01é\\u00a0\""
        );
        assert_eq!(go_quote_rune('\''), "'\\''");
        assert_eq!(go_quote_rune('Ã'), "'Ã'");
        assert_eq!(go_sharp_quote("plain"), "`plain`");
        assert_eq!(go_sharp_quote("with`tick"), "\"with`tick\"");
        assert_eq!(
            go_json_string("<&>\u{2028}\u{1}"),
            "\"\\u003c\\u0026\\u003e\\u2028\\u0001\""
        );
    }
}
