#![deny(unsafe_code)]

//! A port of the parsing rules of Go's standard `flag` package
//! (`FlagSet.parseOne` with `flag.ExitOnError`), shared by every `symroom`
//! command so that the hand-written parsers cannot drift from Go again
//! (ROOMCLI-001).
//!
//! Rules (Go `src/flag/flag.go`):
//! - parsing stops at the first argument that is not a flag: an empty string,
//!   a lone `-`, or anything not starting with `-`; that argument and all
//!   later ones are positional (so a flag after a positional is a positional);
//! - `--` ends flag parsing and is consumed;
//! - one or two leading dashes are equivalent; `---x`, `-=x` and `--=x` are
//!   `bad flag syntax`;
//! - `-name=value` carries the value inline (`=` splits on its first
//!   occurrence), for undefined flags the name excludes the value;
//! - a bool flag takes no separate value; `-b=false` goes through
//!   `strconv.ParseBool`;
//! - any other flag takes the next argument as its value, even `--` or `-`;
//! - `-h`/`-help` (any dash count, any `=value`) that is not a defined flag
//!   prints the usage and exits 0; every other error prints the message and
//!   the usage and exits 2.

use std::{collections::BTreeMap, ffi::OsString, time::Duration};

/// How a flag consumes its value.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Kind {
    Bool,
    /// String, int or duration: takes a value, validated by the caller with
    /// [`parse_go_int`] / the duration parser (Go validates while parsing, so
    /// [`Spec::check`] runs in flag order).
    Value,
}

/// Validates a value at parse time, as Go's `flag.Value.Set` does.
pub type Check = fn(&str) -> Result<(), &'static str>;

#[derive(Clone, Copy)]
pub struct Spec<'a> {
    pub name: &'a str,
    pub kind: Kind,
    /// Go's `flag.Value.Set` error text, `None` for a string flag.
    pub check: Option<Check>,
}

impl<'a> Spec<'a> {
    pub const fn boolean(name: &'a str) -> Self {
        Self {
            name,
            kind: Kind::Bool,
            check: None,
        }
    }

    pub const fn string(name: &'a str) -> Self {
        Self {
            name,
            kind: Kind::Value,
            check: None,
        }
    }

    pub const fn checked(name: &'a str, check: Check) -> Self {
        Self {
            name,
            kind: Kind::Value,
            check: Some(check),
        }
    }
}

/// What a failed parse must print and exit with.
#[derive(Debug, PartialEq, Eq)]
pub enum Failure {
    /// `-h`/`-help`: usage on stderr, exit 0.
    Help,
    /// Message line (without the usage), exit 2.
    Error(String),
}

#[derive(Debug, Default)]
pub struct Parsed {
    /// Last value of every flag that was set; bool flags hold `"true"` or
    /// `"false"`.
    pub values: BTreeMap<String, String>,
    pub positionals: Vec<String>,
}

impl Parsed {
    pub fn string(&self, name: &str) -> &str {
        self.values.get(name).map_or("", String::as_str)
    }

    pub fn is_set(&self, name: &str) -> bool {
        self.values.contains_key(name)
    }

    pub fn boolean(&self, name: &str) -> bool {
        self.values.get(name).is_some_and(|value| value == "true")
    }
}

/// `strconv.ParseBool`.
pub fn parse_go_bool(value: &str) -> Option<bool> {
    match value {
        "1" | "t" | "T" | "TRUE" | "true" | "True" => Some(true),
        "0" | "f" | "F" | "FALSE" | "false" | "False" => Some(false),
        _ => None,
    }
}

/// `flag.intValue.Set`: `strconv.ParseInt(s, 0, 64)`; a syntax error is
/// `parse error`, an overflow `value out of range`.
pub fn check_go_int(value: &str) -> Result<(), &'static str> {
    parse_go_int(value).map(|_| ())
}

/// `flag.durationValue.Set`: every `time.ParseDuration` failure is
/// `parse error`.
pub fn check_go_duration(value: &str) -> Result<(), &'static str> {
    parse_go_duration(value).map(|_| ()).ok_or("parse error")
}

/// `time.ParseDuration` (a negative duration is reported as zero).
pub fn parse_go_duration(input: &str) -> Option<Duration> {
    // Go's `time.ParseDuration` accepts a single optional sign.
    let (negative, input) = match input.as_bytes().first() {
        Some(b'-') => (true, &input[1..]),
        Some(b'+') => (false, &input[1..]),
        _ => (false, input),
    };
    if input == "0" {
        return Some(Duration::ZERO);
    }
    if input.is_empty() {
        return None;
    }
    let mut rest = input;
    let mut total_nanos = 0_u128;
    while !rest.is_empty() {
        let count = rest
            .bytes()
            .take_while(|b| b.is_ascii_digit() || *b == b'.')
            .count();
        if count == 0 {
            return None;
        }
        let amount = &rest[..count];
        rest = &rest[count..];
        let (unit, scale) = [
            ("ns", 1_u128),
            ("us", 1_000),
            ("µs", 1_000),
            ("μs", 1_000),
            ("ms", 1_000_000),
            ("s", 1_000_000_000),
            ("m", 60_000_000_000),
            ("h", 3_600_000_000_000),
        ]
        .into_iter()
        .find(|(unit, _)| rest.starts_with(unit))?;
        let (whole, fraction) = amount
            .split_once('.')
            .map_or((amount, ""), |(whole, fraction)| (whole, fraction));
        if whole.is_empty() && fraction.is_empty()
            || !whole.bytes().all(|byte| byte.is_ascii_digit())
            || !fraction.bytes().all(|byte| byte.is_ascii_digit())
        {
            return None;
        }
        let whole = if whole.is_empty() {
            0
        } else {
            whole.parse::<u128>().ok()?
        };
        total_nanos = total_nanos.checked_add(whole.checked_mul(scale)?)?;
        let fractional_digits = fraction.len().min(19);
        if fractional_digits > 0 {
            let numerator = fraction[..fractional_digits].parse::<u128>().ok()?;
            let denominator = 10_u128.checked_pow(fractional_digits as u32)?;
            total_nanos = total_nanos.checked_add(numerator.checked_mul(scale)? / denominator)?;
        }
        rest = &rest[unit.len()..];
    }
    let maximum = i64::MAX as u128 + u128::from(negative);
    if total_nanos > maximum {
        return None;
    }
    Some(if negative || total_nanos == 0 {
        Duration::ZERO
    } else {
        Duration::from_nanos(total_nanos as u64)
    })
}

/// `strconv.ParseInt(s, 0, 64)` (base prefix, signs and underscores as in Go).
/// Like Go, an overflow is reported at the digit that overflows, before any
/// later invalid character is looked at.
pub fn parse_go_int(value: &str) -> Result<i64, &'static str> {
    let (negative, unsigned) = match value.as_bytes().first() {
        Some(b'+') => (false, &value[1..]),
        Some(b'-') => (true, &value[1..]),
        _ => (false, value),
    };
    if unsigned.is_empty() {
        return Err("parse error");
    }
    let bytes = unsigned.as_bytes();
    let lower = |index: usize| bytes[index].to_ascii_lowercase();
    let (base, digits): (u32, &[u8]) = if bytes[0] == b'0' {
        if bytes.len() >= 3 && lower(1) == b'b' {
            (2, &bytes[2..])
        } else if bytes.len() >= 3 && lower(1) == b'o' {
            (8, &bytes[2..])
        } else if bytes.len() >= 3 && lower(1) == b'x' {
            (16, &bytes[2..])
        } else {
            (8, &bytes[1..])
        }
    } else {
        (10, bytes)
    };
    let mut magnitude: u64 = 0;
    let mut underscores = false;
    for &byte in digits {
        let digit = match byte {
            b'_' => {
                underscores = true;
                continue;
            }
            b'0'..=b'9' => u32::from(byte - b'0'),
            b'a'..=b'z' => u32::from(byte - b'a') + 10,
            b'A'..=b'Z' => u32::from(byte - b'A') + 10,
            _ => return Err("parse error"),
        };
        if digit >= base {
            return Err("parse error");
        }
        magnitude = magnitude
            .checked_mul(u64::from(base))
            .and_then(|scaled| scaled.checked_add(u64::from(digit)))
            .ok_or("value out of range")?;
    }
    if underscores && !underscore_ok(unsigned) {
        return Err("parse error");
    }
    let limit = if negative {
        1_u64 << 63
    } else {
        (1_u64 << 63) - 1
    };
    if magnitude > limit {
        return Err("value out of range");
    }
    Ok(if negative {
        (magnitude as i64).wrapping_neg()
    } else {
        magnitude as i64
    })
}

/// `strconv.underscoreOK`: underscores must separate digits or follow a base
/// prefix.
fn underscore_ok(text: &str) -> bool {
    let bytes = text.as_bytes();
    let mut saw = b'^';
    let mut index = 0;
    let mut hex = false;
    if bytes.len() >= 2
        && bytes[0] == b'0'
        && matches!(bytes[1].to_ascii_lowercase(), b'b' | b'o' | b'x')
    {
        index = 2;
        saw = b'0';
        hex = bytes[1].eq_ignore_ascii_case(&b'x');
    }
    for &byte in &bytes[index..] {
        if byte.is_ascii_digit() || hex && byte.to_ascii_lowercase().is_ascii_hexdigit() {
            saw = b'0';
            continue;
        }
        if byte == b'_' {
            if saw != b'0' {
                return false;
            }
            saw = b'_';
            continue;
        }
        if saw == b'_' {
            return false;
        }
        saw = b'!';
    }
    saw != b'_'
}

/// `fmt`'s `%q` for strings (`strconv.Quote`).
pub fn go_quote(text: &str) -> String {
    let mut quoted = String::with_capacity(text.len() + 2);
    quoted.push('"');
    for ch in text.chars() {
        match ch {
            '"' => quoted.push_str("\\\""),
            '\\' => quoted.push_str("\\\\"),
            '\u{7}' => quoted.push_str("\\a"),
            '\u{8}' => quoted.push_str("\\b"),
            '\u{c}' => quoted.push_str("\\f"),
            '\n' => quoted.push_str("\\n"),
            '\r' => quoted.push_str("\\r"),
            '\t' => quoted.push_str("\\t"),
            '\u{b}' => quoted.push_str("\\v"),
            ch if (ch as u32) < 0x20 || ch as u32 == 0x7f => {
                quoted.push_str(&format!("\\x{:02x}", ch as u32));
            }
            ch if ch.is_control() => {
                if (ch as u32) < 0x10000 {
                    quoted.push_str(&format!("\\u{:04x}", ch as u32));
                } else {
                    quoted.push_str(&format!("\\U{:08x}", ch as u32));
                }
            }
            ch => quoted.push(ch),
        }
    }
    quoted.push('"');
    quoted
}

/// Parses `args` like [`parse`] and, as `flag.ExitOnError` does, prints a
/// failure (message line unless `-h`/`-help`, then `usage`) to stderr, with
/// exit code 2 for a parse error and 0 for help.
pub fn parse_or_exit(
    args: &[OsString],
    specs: &[Spec<'_>],
    usage: &str,
) -> Result<Parsed, std::process::ExitCode> {
    parse(args, specs).map_err(|failure| {
        let (text, code) = match failure {
            Failure::Help => (usage.to_owned(), 0),
            Failure::Error(message) => (format!("{message}\n{usage}"), 2),
        };
        if std::io::Write::write_all(&mut std::io::stderr(), text.as_bytes()).is_err() {
            return std::process::ExitCode::from(1);
        }
        std::process::ExitCode::from(code)
    })
}

/// Parses `args` (everything after the command name) against `specs`.
pub fn parse(args: &[OsString], specs: &[Spec<'_>]) -> Result<Parsed, Failure> {
    let mut parsed = Parsed::default();
    let mut index = 0;
    while index < args.len() {
        let argument = args[index].to_string_lossy();
        let bytes = argument.as_bytes();
        if bytes.len() < 2 || bytes[0] != b'-' {
            break;
        }
        index += 1;
        let mut minuses = 1;
        if bytes[1] == b'-' {
            minuses = 2;
            if bytes.len() == 2 {
                break;
            }
        }
        let body = &argument[minuses..];
        if body.is_empty() || body.starts_with('-') || body.starts_with('=') {
            return Err(Failure::Error(format!("bad flag syntax: {argument}")));
        }
        let (name, inline) = match body.find('=') {
            Some(at) => (&body[..at], Some(&body[at + 1..])),
            None => (body, None),
        };
        let Some(spec) = specs.iter().find(|spec| spec.name == name) else {
            if name == "help" || name == "h" {
                return Err(Failure::Help);
            }
            return Err(Failure::Error(format!(
                "flag provided but not defined: -{name}"
            )));
        };
        match spec.kind {
            Kind::Bool => {
                let value = match inline {
                    Some(value) => parse_go_bool(value).ok_or_else(|| {
                        Failure::Error(format!(
                            "invalid boolean value {} for -{name}: parse error",
                            go_quote(value)
                        ))
                    })?,
                    None => true,
                };
                parsed.values.insert(name.to_owned(), value.to_string());
            }
            Kind::Value => {
                let value = if let Some(value) = inline {
                    value.to_owned()
                } else if let Some(next) = args.get(index) {
                    index += 1;
                    next.to_string_lossy().into_owned()
                } else {
                    return Err(Failure::Error(format!("flag needs an argument: -{name}")));
                };
                if let Some(check) = spec.check
                    && let Err(reason) = check(&value)
                {
                    return Err(Failure::Error(format!(
                        "invalid value {} for flag -{name}: {reason}",
                        go_quote(&value)
                    )));
                }
                parsed.values.insert(name.to_owned(), value);
            }
        }
    }
    parsed.positionals = args[index..]
        .iter()
        .map(|argument| argument.to_string_lossy().into_owned())
        .collect();
    Ok(parsed)
}

#[cfg(test)]
mod tests {
    use super::*;

    const SPECS: &[Spec] = &[
        Spec::boolean("json"),
        Spec::string("identity"),
        Spec::checked("limit", check_go_int),
    ];

    fn args(items: &[&str]) -> Vec<OsString> {
        items.iter().map(OsString::from).collect()
    }

    fn error(items: &[&str]) -> String {
        match parse(&args(items), SPECS) {
            Err(Failure::Error(message)) => message,
            other => panic!("expected an error, got {other:?}"),
        }
    }

    #[test]
    fn stops_at_lone_dash_empty_and_positional() {
        for first in ["-", "", "x"] {
            let parsed = parse(&args(&[first, "--json"]), SPECS).expect("parse");
            assert_eq!(parsed.positionals, [first, "--json"]);
            assert!(!parsed.is_set("json"));
        }
    }

    #[test]
    fn terminator_is_consumed() {
        let parsed = parse(&args(&["--json", "--", "--json"]), SPECS).expect("parse");
        assert_eq!(parsed.positionals, ["--json"]);
        assert!(parsed.boolean("json"));
    }

    #[test]
    fn inline_values_and_bool_forms() {
        let parsed = parse(&args(&["-identity=a=b", "--json=F", "x"]), SPECS).expect("parse");
        assert_eq!(parsed.string("identity"), "a=b");
        assert!(!parsed.boolean("json"));
        assert!(parsed.is_set("json"));
        assert_eq!(
            error(&["--json=x"]),
            "invalid boolean value \"x\" for -json: parse error"
        );
        assert_eq!(
            error(&["--json="]),
            "invalid boolean value \"\" for -json: parse error"
        );
    }

    #[test]
    fn value_flag_takes_next_argument_even_a_terminator() {
        let parsed = parse(&args(&["--identity", "--", "y"]), SPECS).expect("parse");
        assert_eq!(parsed.string("identity"), "--");
        assert_eq!(parsed.positionals, ["y"]);
        assert_eq!(error(&["--identity"]), "flag needs an argument: -identity");
    }

    #[test]
    fn undefined_and_malformed() {
        assert_eq!(error(&["--zzz=1"]), "flag provided but not defined: -zzz");
        assert_eq!(error(&["---x"]), "bad flag syntax: ---x");
        assert_eq!(error(&["-=x"]), "bad flag syntax: -=x");
        assert_eq!(error(&["--=x"]), "bad flag syntax: --=x");
        for help in ["-h", "--help", "-help=1", "-h=x"] {
            assert_eq!(parse(&args(&[help]), SPECS).unwrap_err(), Failure::Help);
        }
    }

    #[test]
    fn go_int_rules() {
        for (text, want) in [
            ("0", Ok(0)),
            ("+3", Ok(3)),
            ("-3", Ok(-3)),
            ("0x10", Ok(16)),
            ("010", Ok(8)),
            ("0b11", Ok(3)),
            ("0o17", Ok(15)),
            ("1_0", Ok(10)),
            ("_1", Err("parse error")),
            ("1__0", Err("parse error")),
            ("1_", Err("parse error")),
            ("0x1_f", Ok(31)),
            ("0b_1", Ok(1)),
            ("0b2", Err("parse error")),
            ("-0x10", Ok(-16)),
            ("+-3", Err("parse error")),
            ("99999999999999999999x", Err("value out of range")),
            ("0x_1", Ok(1)),
            ("08", Err("parse error")),
            ("0_7", Ok(7)),
            ("", Err("parse error")),
            ("1.5", Err("parse error")),
            ("x", Err("parse error")),
            ("0x", Err("parse error")),
            ("9223372036854775807", Ok(i64::MAX)),
            ("9223372036854775808", Err("value out of range")),
            ("-9223372036854775808", Ok(i64::MIN)),
            (
                "99999999999999999999999999999999999999999",
                Err("value out of range"),
            ),
        ] {
            assert_eq!(parse_go_int(text), want, "{text:?}");
        }
    }
}
