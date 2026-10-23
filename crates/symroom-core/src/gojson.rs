#![deny(unsafe_code)]

//! Go `encoding/json.Unmarshal` semantics for a flat struct of string fields.
//!
//! `serde_json` differs from Go in four observable ways that reach the user
//! through the identity file: syntax-error wording, duplicate keys (Go keeps
//! the last, serde rejects), case-insensitive field matching (Go folds, serde
//! is exact) and the type-error text. Go validates the whole input first
//! (`checkValid`) and only then walks the object, saving the *first* type error
//! and continuing, so a syntax error anywhere wins over an earlier type error.

const MAX_JSON_DEPTH: usize = 10_000;

/// Decoded string fields, in the order of the `fields` slice. `None` means the
/// key was absent or `null` (Go leaves the zero value untouched).
pub type Fields = Vec<Option<String>>;

/// Decode `data` into a struct named `struct_name` (Go type
/// `<package_type>`) whose string fields are `fields` (JSON names).
///
/// The error is the exact `err.Error()` Go returns from `json.Unmarshal`.
pub fn decode_string_struct(
    data: &[u8],
    struct_name: &str,
    package_type: &str,
    fields: &[&str],
) -> Result<Fields, String> {
    let mut syntax = Syntax::new(data);
    syntax.skip_space();
    syntax.value(0)?;
    syntax.skip_space();
    if let Some(character) = syntax.peek() {
        return Err(format!(
            "invalid character {} after top-level value",
            quote_char(character)
        ));
    }

    let mut walker = Syntax::new(data);
    walker.skip_space();
    let mut values: Fields = vec![None; fields.len()];
    match walker.peek() {
        Some(b'{') => {}
        Some(b'n') => return Ok(values),
        Some(first) => {
            let kind = match first {
                b'[' => "array",
                b'"' => "string",
                b't' | b'f' => "bool",
                _ => "number",
            };
            return Err(format!(
                "json: cannot unmarshal {kind} into Go value of type {package_type}"
            ));
        }
        None => return Err("unexpected end of JSON input".to_owned()),
    }
    walker.offset += 1;
    walker.skip_space();
    let mut first_type_error: Option<String> = None;
    if walker.peek() == Some(b'}') {
        return Ok(values);
    }
    loop {
        walker.skip_space();
        walker.offset += 1; // opening quote of the key
        let key = walker.read_string()?;
        walker.skip_space();
        walker.offset += 1; // ':'
        walker.skip_space();
        let slot = fields.iter().position(|field| *field == key).or_else(|| {
            fields
                .iter()
                .position(|field| folded(field) == folded(&key))
        });
        match (slot, walker.peek()) {
            (Some(slot), Some(b'"')) => {
                walker.offset += 1;
                values[slot] = Some(walker.read_string()?);
            }
            (Some(_), Some(b'n')) => walker.value(0)?,
            (Some(slot), Some(first)) => {
                if first_type_error.is_none() {
                    let kind = match first {
                        b'{' => "object",
                        b'[' => "array",
                        b't' | b'f' => "bool",
                        _ => "number",
                    };
                    first_type_error = Some(format!(
                        "json: cannot unmarshal {kind} into Go struct field {struct_name}.{} of type string",
                        fields[slot]
                    ));
                }
                walker.value(0)?;
            }
            (None, _) => walker.value(0)?,
            (Some(_), None) => return Err("unexpected end of JSON input".to_owned()),
        }
        walker.skip_space();
        match walker.next() {
            Some(b',') => {}
            _ => break,
        }
    }
    match first_type_error {
        Some(error) => Err(error),
        None => Ok(values),
    }
}

/// Go's `foldName`: ASCII upper-casing plus the two non-ASCII simple folds.
fn folded(name: &str) -> String {
    name.chars()
        .map(|character| match character {
            'a'..='z' => character.to_ascii_uppercase(),
            '\u{17f}' => 'S',
            '\u{212a}' => 'K',
            other => other,
        })
        .collect()
}

/// Go `scanner.quoteChar`: `strconv.Quote(string(rune(c)))` without the double
/// quotes, so bytes above 0x7f are Latin-1 code points.
fn quote_char(character: u8) -> String {
    match character {
        b'\'' => "'\\''".to_owned(),
        b'"' => "'\"'".to_owned(),
        b'\\' => "'\\\\'".to_owned(),
        0x07 => "'\\a'".to_owned(),
        0x08 => "'\\b'".to_owned(),
        0x0c => "'\\f'".to_owned(),
        b'\n' => "'\\n'".to_owned(),
        b'\r' => "'\\r'".to_owned(),
        b'\t' => "'\\t'".to_owned(),
        0x0b => "'\\v'".to_owned(),
        0x00..=0x1f | 0x7f => format!("'\\x{character:02x}'"),
        0x80..=0x9f | 0xa0 | 0xad => format!("'\\u{character:04x}'"),
        other => format!("'{}'", char::from(other)),
    }
}

struct Syntax<'a> {
    data: &'a [u8],
    offset: usize,
}

impl<'a> Syntax<'a> {
    fn new(data: &'a [u8]) -> Self {
        Self { data, offset: 0 }
    }

    fn peek(&self) -> Option<u8> {
        self.data.get(self.offset).copied()
    }

    fn next(&mut self) -> Option<u8> {
        let byte = self.peek()?;
        self.offset += 1;
        Some(byte)
    }

    fn skip_space(&mut self) {
        while matches!(self.peek(), Some(b' ' | b'\t' | b'\r' | b'\n')) {
            self.offset += 1;
        }
    }

    fn end<T>() -> Result<T, String> {
        Err("unexpected end of JSON input".to_owned())
    }

    fn value(&mut self, depth: usize) -> Result<(), String> {
        let Some(character) = self.next() else {
            return Self::end();
        };
        match character {
            b'{' => self.object(depth + 1),
            b'[' => self.array(depth + 1),
            b'"' => self.string(),
            b't' => self.literal(b"rue", "true"),
            b'f' => self.literal(b"alse", "false"),
            b'n' => self.literal(b"ull", "null"),
            b'-' => match self.next() {
                Some(b'0') => self.fraction_or_exponent(),
                Some(b'1'..=b'9') => self.integer(),
                Some(other) => Err(format!(
                    "invalid character {} in numeric literal",
                    quote_char(other)
                )),
                None => Self::end(),
            },
            b'0' => self.fraction_or_exponent(),
            b'1'..=b'9' => self.integer(),
            other => Err(format!(
                "invalid character {} looking for beginning of value",
                quote_char(other)
            )),
        }
    }

    fn depth(depth: usize, character: u8) -> Result<(), String> {
        if depth > MAX_JSON_DEPTH {
            return Err(format!(
                "invalid character {} exceeded max depth",
                quote_char(character)
            ));
        }
        Ok(())
    }

    fn object(&mut self, depth: usize) -> Result<(), String> {
        Self::depth(depth, b'{')?;
        self.skip_space();
        if self.peek() == Some(b'}') {
            self.offset += 1;
            return Ok(());
        }
        loop {
            let Some(character) = self.next() else {
                return Self::end();
            };
            if character != b'"' {
                return Err(format!(
                    "invalid character {} looking for beginning of object key string",
                    quote_char(character)
                ));
            }
            self.string()?;
            self.skip_space();
            match self.next() {
                Some(b':') => {}
                Some(character) => {
                    return Err(format!(
                        "invalid character {} after object key",
                        quote_char(character)
                    ));
                }
                None => return Self::end(),
            }
            self.skip_space();
            self.value(depth)?;
            self.skip_space();
            match self.next() {
                Some(b'}') => return Ok(()),
                Some(b',') => self.skip_space(),
                Some(character) => {
                    return Err(format!(
                        "invalid character {} after object key:value pair",
                        quote_char(character)
                    ));
                }
                None => return Self::end(),
            }
        }
    }

    fn array(&mut self, depth: usize) -> Result<(), String> {
        Self::depth(depth, b'[')?;
        self.skip_space();
        if self.peek() == Some(b']') {
            self.offset += 1;
            return Ok(());
        }
        loop {
            self.value(depth)?;
            self.skip_space();
            match self.next() {
                Some(b']') => return Ok(()),
                Some(b',') => self.skip_space(),
                Some(character) => {
                    return Err(format!(
                        "invalid character {} after array element",
                        quote_char(character)
                    ));
                }
                None => return Self::end(),
            }
        }
    }

    fn string(&mut self) -> Result<(), String> {
        loop {
            let Some(character) = self.next() else {
                return Self::end();
            };
            match character {
                b'"' => return Ok(()),
                b'\\' => {
                    let Some(escape) = self.next() else {
                        return Self::end();
                    };
                    match escape {
                        b'b' | b'f' | b'n' | b'r' | b't' | b'\\' | b'/' | b'"' => {}
                        b'u' => {
                            for _ in 0..4 {
                                let Some(hex) = self.next() else {
                                    return Self::end();
                                };
                                if !hex.is_ascii_hexdigit() {
                                    return Err(format!(
                                        "invalid character {} in \\u hexadecimal character escape",
                                        quote_char(hex)
                                    ));
                                }
                            }
                        }
                        other => {
                            return Err(format!(
                                "invalid character {} in string escape code",
                                quote_char(other)
                            ));
                        }
                    }
                }
                0x00..=0x1f => {
                    return Err(format!(
                        "invalid character {} in string literal",
                        quote_char(character)
                    ));
                }
                _ => {}
            }
        }
    }

    fn literal(&mut self, suffix: &[u8], name: &str) -> Result<(), String> {
        for expected in suffix {
            let Some(actual) = self.next() else {
                return Self::end();
            };
            if actual != *expected {
                return Err(format!(
                    "invalid character {} in literal {name} (expecting {})",
                    quote_char(actual),
                    quote_char(*expected)
                ));
            }
        }
        Ok(())
    }

    fn integer(&mut self) -> Result<(), String> {
        while self.peek().is_some_and(|byte| byte.is_ascii_digit()) {
            self.offset += 1;
        }
        self.fraction_or_exponent()
    }

    fn digits_after(&mut self, context: &str) -> Result<(), String> {
        match self.next() {
            Some(character) if character.is_ascii_digit() => {}
            Some(character) => {
                return Err(format!(
                    "invalid character {} {context}",
                    quote_char(character)
                ));
            }
            None => return Self::end(),
        }
        while self.peek().is_some_and(|byte| byte.is_ascii_digit()) {
            self.offset += 1;
        }
        Ok(())
    }

    fn fraction_or_exponent(&mut self) -> Result<(), String> {
        if self.peek() == Some(b'.') {
            self.offset += 1;
            self.digits_after("after decimal point in numeric literal")?;
        }
        if matches!(self.peek(), Some(b'e' | b'E')) {
            self.offset += 1;
            if matches!(self.peek(), Some(b'+' | b'-')) {
                self.offset += 1;
            }
            self.digits_after("in exponent of numeric literal")?;
        }
        Ok(())
    }

    /// Read the remainder of an already syntax-checked string literal (the
    /// opening quote is consumed) like Go's `unquote`: escapes are resolved and
    /// every invalid UTF-8 byte becomes U+FFFD.
    fn read_string(&mut self) -> Result<String, String> {
        let mut output = String::new();
        let mut plain = self.offset;
        loop {
            let Some(character) = self.next() else {
                return Self::end();
            };
            match character {
                b'"' => {
                    push_go_utf8(&mut output, &self.data[plain..self.offset - 1]);
                    return Ok(output);
                }
                b'\\' => {
                    push_go_utf8(&mut output, &self.data[plain..self.offset - 1]);
                    let escape = self.next().ok_or("unexpected end of JSON input")?;
                    match escape {
                        b'b' => output.push('\u{8}'),
                        b'f' => output.push('\u{c}'),
                        b'n' => output.push('\n'),
                        b'r' => output.push('\r'),
                        b't' => output.push('\t'),
                        b'u' => {
                            let first = self.hex4()?;
                            output.push(self.unicode_escape(first));
                        }
                        other => output.push(char::from(other)),
                    }
                    plain = self.offset;
                }
                _ => {}
            }
        }
    }

    fn hex4(&mut self) -> Result<u32, String> {
        let mut value = 0_u32;
        for _ in 0..4 {
            let digit = self.next().ok_or("unexpected end of JSON input")?;
            value = value * 16 + char::from(digit).to_digit(16).unwrap_or(0);
        }
        Ok(value)
    }

    /// A `\uXXXX` escape: a valid surrogate pair is combined, a lone surrogate
    /// is U+FFFD and consumes only its own escape (Go `utf16.DecodeRune`).
    fn unicode_escape(&mut self, first: u32) -> char {
        if !(0xd800..0xe000).contains(&first) {
            return char::from_u32(first).unwrap_or('\u{fffd}');
        }
        if (0xd800..0xdc00).contains(&first)
            && self.data.get(self.offset) == Some(&b'\\')
            && self.data.get(self.offset + 1) == Some(&b'u')
        {
            let saved = self.offset;
            self.offset += 2;
            if let Ok(second) = self.hex4()
                && (0xdc00..0xe000).contains(&second)
            {
                let combined = 0x10000 + ((first - 0xd800) << 10) + (second - 0xdc00);
                return char::from_u32(combined).unwrap_or('\u{fffd}');
            }
            self.offset = saved;
        }
        '\u{fffd}'
    }
}

/// Go's `unquoteBytes` copies valid UTF-8 and turns each invalid byte into
/// U+FFFD (`utf8.DecodeRune` consumes one byte per error), unlike
/// `String::from_utf8_lossy`, which replaces a whole maximal subpart.
fn push_go_utf8(output: &mut String, mut bytes: &[u8]) {
    loop {
        match std::str::from_utf8(bytes) {
            Ok(text) => {
                output.push_str(text);
                return;
            }
            Err(error) => {
                let valid = error.valid_up_to();
                output.push_str(std::str::from_utf8(&bytes[..valid]).unwrap_or_default());
                output.push('\u{fffd}');
                bytes = &bytes[valid + 1..];
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    const FIELDS: [&str; 2] = ["name", "private_key"];

    fn decode(text: &str) -> Result<Fields, String> {
        decode_string_struct(text.as_bytes(), "S", "pkg.S", &FIELDS)
    }

    #[test]
    fn duplicate_keys_keep_the_last_and_fold_case() {
        let fields = decode(r#"{"NAME":"a","name":"b","Private_Key":"k"}"#).unwrap();
        assert_eq!(fields, vec![Some("b".into()), Some("k".into())]);
    }

    #[test]
    fn null_and_unknown_keys_leave_the_zero_value() {
        let fields = decode(r#"{"name":null,"extra":[1,{"a":true}]}"#).unwrap();
        assert_eq!(fields, vec![None, None]);
        assert_eq!(decode("null").unwrap(), vec![None, None]);
    }

    #[test]
    fn syntax_errors_win_over_type_errors() {
        assert_eq!(
            decode(r#"{"name":7,"x":}"#).unwrap_err(),
            "invalid character '}' looking for beginning of value"
        );
    }

    #[test]
    fn first_type_error_is_reported() {
        assert_eq!(
            decode(r#"{"name":[],"private_key":7}"#).unwrap_err(),
            "json: cannot unmarshal array into Go struct field S.name of type string"
        );
        assert_eq!(
            decode("[1]").unwrap_err(),
            "json: cannot unmarshal array into Go value of type pkg.S"
        );
    }

    #[test]
    fn strings_decode_like_go() {
        let input = b"{\"name\":\"\\ud83d\\ude00\\ud83dx\\u00e9\xff\"}";
        let fields = decode_string_struct(input, "S", "pkg.S", &FIELDS).unwrap();
        assert_eq!(
            fields[0].as_deref(),
            Some("\u{1f600}\u{fffd}x\u{e9}\u{fffd}")
        );
    }

    #[test]
    fn quote_char_matches_strconv_quote() {
        assert_eq!(quote_char(0xef), "'ï'");
        assert_eq!(quote_char(0x80), "'\\u0080'");
        assert_eq!(quote_char(0xa0), "'\\u00a0'");
        assert_eq!(quote_char(0x01), "'\\x01'");
    }
}
