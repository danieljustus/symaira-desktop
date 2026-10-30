//! vCard import compatibility with the Go go-vcard v0.1.0 decoder.
use crate::import::{ImportRow, ParseResult, ValidationIssue, identity_hash};
use std::collections::HashMap;

#[derive(Default)]
struct Field {
    value: String,
    params: HashMap<String, Vec<String>>,
}
type Card = HashMap<String, Vec<Field>>;

pub fn parse_vcard(input: &str) -> ParseResult {
    let mut decoder = Decoder {
        lines: input.split_inclusive('\n').collect(),
        next: 0,
    };
    let mut rows = Vec::new();
    let mut issues = Vec::new();
    let mut card_num = 0;
    while let Some(card) = decoder.decode() {
        card_num += 1;
        let card = match card {
            Ok(c) => c,
            Err(err) => {
                issues.push(ValidationIssue {
                    row_number: card_num,
                    field: String::new(),
                    message: format!("failed to parse vCard entry: {err}"),
                });
                continue;
            }
        };
        let value = |key: &str| {
            card.get(key)
                .and_then(|fs| fs.first())
                .map(|f| f.value.clone())
                .unwrap_or_default()
        };
        let values = |key: &str| {
            card.get(key)
                .map(|fs| fs.iter().map(|f| f.value.clone()).collect())
        };
        let mut row = ImportRow {
            row_number: card_num,
            display_name: preferred(&card, "FN"),
            emails: values("EMAIL"),
            phones: values("TEL"),
            urls: values("URL"),
            ..ImportRow::default()
        };
        let name = preferred(&card, "N");
        let mut parts = name.split(';');
        row.family_name = parts.next().unwrap_or_default().into();
        row.given_name = parts.next().unwrap_or_default().into();
        if row.display_name.is_empty() {
            row.display_name = format!("{} {}", row.given_name, row.family_name)
                .trim()
                .to_owned();
        }
        if row.display_name.is_empty() {
            issues.push(ValidationIssue {
                row_number: card_num,
                field: "FN".into(),
                message: "card has no FN or N to derive a display name from".into(),
            });
            continue;
        }
        row.organization_name = value("ORG").split(';').next().unwrap_or_default().into();
        row.title = value("TITLE");
        let uid = value("UID");
        row.source_ref = if uid.is_empty() {
            identity_hash("vcard-hash:", &row)
        } else {
            format!("vcard-uid:{uid}")
        };
        rows.push(row);
    }
    Ok((
        (!rows.is_empty()).then_some(rows),
        (!issues.is_empty()).then_some(issues),
    ))
}
fn parse_preference(s: &str) -> i64 {
    if let Ok(n) = s.parse::<i64>() {
        return n;
    }
    let digits = s.strip_prefix(['+', '-']).unwrap_or(s);
    if !digits.is_empty() && digits.bytes().all(|b| b.is_ascii_digit()) {
        if s.starts_with('-') {
            i64::MIN
        } else {
            i64::MAX
        }
    } else {
        0
    }
}

fn preferred(card: &Card, key: &str) -> String {
    let Some(fields) = card.get(key) else {
        return String::new();
    };
    let mut best = &fields[0];
    let mut min = 100;
    for field in fields {
        let pref = field
            .params
            .get("PREF")
            .and_then(|vs| vs.first())
            .map(String::as_str)
            .unwrap_or_default();
        let n = if !pref.is_empty() {
            parse_preference(pref)
        } else if field
            .params
            .get("TYPE")
            .is_some_and(|vs| vs.iter().any(|v| v.eq_ignore_ascii_case("pref")))
        {
            1
        } else {
            100
        };
        if n < min {
            min = n;
            best = field;
        }
    }
    best.value.clone()
}
struct Decoder<'a> {
    lines: Vec<&'a str>,
    next: usize,
}
impl Decoder<'_> {
    fn read_line(&mut self) -> Option<String> {
        let raw = *self.lines.get(self.next)?;
        self.next += 1;
        let mut line = raw.trim_end_matches(['\r', '\n']).to_owned();
        if !raw.ends_with('\n') {
            return (!line.is_empty()).then_some(line);
        }
        while let Some(raw) = self
            .lines
            .get(self.next)
            .copied()
            .filter(|s| s.starts_with([' ', '\t']))
        {
            self.next += 1;
            if !raw.ends_with('\n') {
                return None;
            }
            line.push_str(raw[1..].trim_end_matches(['\r', '\n']));
        }
        Some(line)
    }
    fn decode(&mut self) -> Option<Result<Card, String>> {
        let mut card = Card::new();
        let mut begin = false;
        while let Some(line) = self.read_line() {
            let Some((key, field)) = parse_line(&line) else {
                continue;
            };
            if !begin {
                if key != "BEGIN" {
                    return Some(Err("vcard: no BEGIN field found".into()));
                }
                if !field.value.eq_ignore_ascii_case("VCARD") {
                    return Some(Err("vcard: invalid BEGIN value".into()));
                }
                begin = true;
                continue;
            } else if key == "END" {
                return Some(if field.value.eq_ignore_ascii_case("VCARD") {
                    Ok(card)
                } else {
                    Err("vcard: invalid END value".into())
                });
            }
            card.entry(key).or_default().push(field);
        }
        begin.then(|| Err("vcard: no END field found".into()))
    }
}
fn parse_line(mut s: &str) -> Option<(String, Field)> {
    if let Some(i) = s.find(['.', ';', ':']).filter(|i| s.as_bytes()[*i] == b'.') {
        s = &s[i + 1..];
    }
    let i = s.find([';', ':'])?;
    let key = s[..i].to_uppercase();
    let has_params = s.as_bytes()[i] == b';';
    s = &s[i + 1..];
    let mut field = Field::default();
    if has_params {
        while !s.is_empty() {
            let i = s.find(['=', ';', ':'])?;
            if s.as_bytes()[i] == b';' {
                s = &s[i + 1..];
                continue;
            }
            let key = s[..i].to_uppercase();
            s = &s[i + 1..];
            let (v, more, tail) = param_values(s)?;
            field.params.entry(key).or_default().extend(v);
            s = tail;
            if !more {
                break;
            }
        }
    }
    field.value = parse_value(s);
    Some((key, field))
}
fn param_values(s: &str) -> Option<(Vec<String>, bool, &str)> {
    if s.is_empty() {
        return Some((Vec::new(), false, ""));
    }
    let (value, more, tail) = if let Some(quoted) = s.strip_prefix('"') {
        let (v, tail) = parse_quoted(quoted)?;
        let more = match tail.as_bytes().first()? {
            b';' => true,
            b':' => false,
            _ => return None,
        };
        (v, more, &tail[1..])
    } else if let Some(i) = s.find([';', ':']) {
        (s[..i].to_owned(), s.as_bytes()[i] != b':', &s[i + 1..])
    } else {
        (s.to_owned(), false, "")
    };
    Some((value.split(',').map(parse_value).collect(), more, tail))
}
fn parse_quoted(mut s: &str) -> Option<(String, &str)> {
    let mut out = String::new();
    while !s.is_empty() {
        let c = s.chars().next()?;
        s = &s[c.len_utf8()..];
        if c == '"' {
            return Some((out, s));
        }
        if c == '\\' {
            let escaped = s.chars().next()?;
            s = &s[escaped.len_utf8()..];
            match escaped {
                'a' => out.push('\x07'),
                'b' => out.push('\x08'),
                'f' => out.push('\x0c'),
                'n' => out.push('\n'),
                'r' => out.push('\r'),
                't' => out.push('\t'),
                'v' => out.push('\x0b'),
                '\\' => out.push('\\'),
                '"' => out.push('"'),
                'x' | 'u' | 'U' => {
                    let n = match escaped {
                        'x' => 2,
                        'u' => 4,
                        _ => 8,
                    };
                    let hex = s.get(..n)?;
                    let value = u32::from_str_radix(hex, 16).ok()?;
                    out.push(char::from_u32(value)?);
                    s = &s[n..];
                }
                '0'..='7' => {
                    let tail = s.get(..2)?;
                    if !tail.bytes().all(|b| (b'0'..=b'7').contains(&b)) {
                        return None;
                    }
                    let v = u32::from_str_radix(&format!("{escaped}{tail}"), 8).ok()?;
                    if v > 255 {
                        return None;
                    }
                    out.push(char::from_u32(v)?);
                    s = &s[2..];
                }
                _ => return None,
            }
        } else if c == '\n' {
            return None;
        } else {
            out.push(c);
        }
    }
    Some((out, s))
}
fn parse_value(s: &str) -> String {
    let mut out = String::new();
    let mut chars = s.chars().peekable();
    while let Some(c) = chars.next() {
        if c == '\\' {
            match chars.peek() {
                Some('\\') => {
                    chars.next();
                    out.push('\\');
                }
                Some('n') => {
                    chars.next();
                    out.push('\n');
                }
                Some(',') => {
                    chars.next();
                    out.push(',');
                }
                _ => out.push(c),
            }
        } else {
            out.push(c)
        }
    }
    out
}
