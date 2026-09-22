//! Ordered retention-state JSON decoding with Go `encoding/json` semantics.
//!
//! `serde_json::Value` cannot be used at this boundary: it discards duplicate
//! object keys and their input order before the retention structs are decoded.
//! The Go oracle validates the complete JSON syntax first, then walks object
//! members in wire order and reports the first type error it encounters.

use super::{HistoryEntry, Proposal, ProposalItem};
use time::{Date, Month, OffsetDateTime, PrimitiveDateTime, Time, UtcOffset};

const RFC3339_LAYOUT: &str = "2006-01-02T15:04:05Z07:00";
const MAX_JSON_DEPTH: usize = 10_000;

pub(super) fn decode_proposal(data: &[u8]) -> Result<Proposal, String> {
    validate_syntax(data)?;
    let mut cursor = Cursor::new(data);
    cursor.proposal()
}

pub(super) fn decode_history(data: &[u8]) -> Result<Option<Vec<HistoryEntry>>, String> {
    validate_syntax(data)?;
    let mut cursor = Cursor::new(data);
    cursor.history()
}

fn validate_syntax(data: &[u8]) -> Result<(), String> {
    let mut parser = SyntaxParser::new(data);
    parser.skip_space();
    parser.value(0)?;
    parser.skip_space();
    if let Some(character) = parser.peek() {
        return Err(format!(
            "invalid character {} after top-level value",
            quote_json_character(character)
        ));
    }
    Ok(())
}

struct SyntaxParser<'a> {
    data: &'a [u8],
    offset: usize,
}

impl<'a> SyntaxParser<'a> {
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
        while self.peek().is_some_and(is_json_space) {
            self.offset += 1;
        }
    }

    fn unexpected_end<T>(&self) -> Result<T, String> {
        Err("unexpected end of JSON input".to_owned())
    }

    fn value(&mut self, depth: usize) -> Result<(), String> {
        let Some(character) = self.next() else {
            return self.unexpected_end();
        };
        match character {
            b'{' => self.object(depth + 1),
            b'[' => self.array(depth + 1),
            b'"' => self.string(),
            b't' => self.literal(b"rue", "true"),
            b'f' => self.literal(b"alse", "false"),
            b'n' => self.literal(b"ull", "null"),
            b'-' => self.number_after_minus(),
            b'0' => self.number_after_zero(),
            b'1'..=b'9' => self.number_after_integer(),
            other => Err(format!(
                "invalid character {} looking for beginning of value",
                quote_json_character(other)
            )),
        }
    }

    fn object(&mut self, depth: usize) -> Result<(), String> {
        self.check_depth(depth, b'{')?;
        self.skip_space();
        if self.peek() == Some(b'}') {
            self.offset += 1;
            return Ok(());
        }
        loop {
            let Some(character) = self.next() else {
                return self.unexpected_end();
            };
            if character != b'"' {
                return Err(format!(
                    "invalid character {} looking for beginning of object key string",
                    quote_json_character(character)
                ));
            }
            self.string()?;
            self.skip_space();
            match self.next() {
                Some(b':') => {}
                Some(character) => {
                    return Err(format!(
                        "invalid character {} after object key",
                        quote_json_character(character)
                    ));
                }
                None => return self.unexpected_end(),
            }
            self.skip_space();
            self.value(depth)?;
            self.skip_space();
            match self.next() {
                Some(b'}') => return Ok(()),
                Some(b',') => {
                    self.skip_space();
                }
                Some(character) => {
                    return Err(format!(
                        "invalid character {} after object key:value pair",
                        quote_json_character(character)
                    ));
                }
                None => return self.unexpected_end(),
            }
        }
    }

    fn array(&mut self, depth: usize) -> Result<(), String> {
        self.check_depth(depth, b'[')?;
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
                        quote_json_character(character)
                    ));
                }
                None => return self.unexpected_end(),
            }
        }
    }

    fn check_depth(&self, depth: usize, character: u8) -> Result<(), String> {
        if depth > MAX_JSON_DEPTH {
            return Err(format!(
                "invalid character {} exceeded max depth",
                quote_json_character(character)
            ));
        }
        Ok(())
    }

    fn string(&mut self) -> Result<(), String> {
        loop {
            let Some(character) = self.next() else {
                return self.unexpected_end();
            };
            match character {
                b'"' => return Ok(()),
                b'\\' => self.string_escape()?,
                0x00..=0x1f => {
                    return Err(format!(
                        "invalid character {} in string literal",
                        quote_json_character(character)
                    ));
                }
                _ => {}
            }
        }
    }

    fn string_escape(&mut self) -> Result<(), String> {
        let Some(character) = self.next() else {
            return self.unexpected_end();
        };
        match character {
            b'b' | b'f' | b'n' | b'r' | b't' | b'\\' | b'/' | b'"' => Ok(()),
            b'u' => {
                for _ in 0..4 {
                    let Some(hex) = self.next() else {
                        return self.unexpected_end();
                    };
                    if !hex.is_ascii_hexdigit() {
                        return Err(format!(
                            "invalid character {} in \\u hexadecimal character escape",
                            quote_json_character(hex)
                        ));
                    }
                }
                Ok(())
            }
            other => Err(format!(
                "invalid character {} in string escape code",
                quote_json_character(other)
            )),
        }
    }

    fn literal(&mut self, suffix: &[u8], name: &str) -> Result<(), String> {
        for (index, expected) in suffix.iter().enumerate() {
            let Some(actual) = self.next() else {
                return self.unexpected_end();
            };
            if actual != *expected {
                return Err(format!(
                    "invalid character {} in literal {name} (expecting {})",
                    quote_json_character(actual),
                    quote_json_character(*expected)
                ));
            }
            debug_assert!(index < suffix.len());
        }
        Ok(())
    }

    fn number_after_minus(&mut self) -> Result<(), String> {
        match self.next() {
            Some(b'0') => self.number_after_zero(),
            Some(b'1'..=b'9') => self.number_after_integer(),
            Some(character) => Err(format!(
                "invalid character {} in numeric literal",
                quote_json_character(character)
            )),
            None => self.unexpected_end(),
        }
    }

    fn number_after_integer(&mut self) -> Result<(), String> {
        while self.peek().is_some_and(|byte| byte.is_ascii_digit()) {
            self.offset += 1;
        }
        self.number_fraction_or_exponent()
    }

    fn number_after_zero(&mut self) -> Result<(), String> {
        self.number_fraction_or_exponent()
    }

    fn number_fraction_or_exponent(&mut self) -> Result<(), String> {
        if self.peek() == Some(b'.') {
            self.offset += 1;
            match self.next() {
                Some(character) if character.is_ascii_digit() => {}
                Some(character) => {
                    return Err(format!(
                        "invalid character {} after decimal point in numeric literal",
                        quote_json_character(character)
                    ));
                }
                None => return self.unexpected_end(),
            }
            while self.peek().is_some_and(|byte| byte.is_ascii_digit()) {
                self.offset += 1;
            }
        }
        if self.peek().is_some_and(|byte| matches!(byte, b'e' | b'E')) {
            self.offset += 1;
            if self.peek().is_some_and(|byte| matches!(byte, b'+' | b'-')) {
                self.offset += 1;
            }
            match self.next() {
                Some(character) if character.is_ascii_digit() => {}
                Some(character) => {
                    return Err(format!(
                        "invalid character {} in exponent of numeric literal",
                        quote_json_character(character)
                    ));
                }
                None => return self.unexpected_end(),
            }
            while self.peek().is_some_and(|byte| byte.is_ascii_digit()) {
                self.offset += 1;
            }
        }
        Ok(())
    }
}

struct Cursor<'a> {
    data: &'a [u8],
    offset: usize,
    first_type_error: Option<String>,
}

impl<'a> Cursor<'a> {
    fn new(data: &'a [u8]) -> Self {
        Self {
            data,
            offset: 0,
            first_type_error: None,
        }
    }

    fn record_type_error(&mut self, error: String) {
        if self.first_type_error.is_none() {
            self.first_type_error = Some(error);
        }
    }

    fn finish<T>(&mut self, value: T) -> Result<T, String> {
        match self.first_type_error.take() {
            Some(error) => Err(error),
            None => Ok(value),
        }
    }

    fn peek(&self) -> u8 {
        self.data[self.offset]
    }

    fn skip_space(&mut self) {
        while self
            .data
            .get(self.offset)
            .is_some_and(|byte| is_json_space(*byte))
        {
            self.offset += 1;
        }
    }

    fn consume(&mut self, expected: u8) {
        self.skip_space();
        debug_assert_eq!(self.data[self.offset], expected);
        self.offset += 1;
    }

    fn proposal(&mut self) -> Result<Proposal, String> {
        self.skip_space();
        match self.peek() {
            b'n' => {
                self.skip_value();
                Ok(Proposal::default())
            }
            b'{' => {
                let proposal = self.proposal_object()?;
                self.finish(proposal)
            }
            _ => Err(go_unmarshal_value_error(
                self.value_kind(),
                "retention.Proposal",
            )),
        }
    }

    fn proposal_object(&mut self) -> Result<Proposal, String> {
        self.consume(b'{');
        let mut proposal = Proposal::default();
        self.skip_space();
        if self.peek() == b'}' {
            self.offset += 1;
            return Ok(proposal);
        }
        loop {
            let key = self.parse_string();
            self.consume(b':');
            if field_matches(&key, "run_id") {
                if let Some(value) = self.string_field("Proposal", "run_id", "string") {
                    proposal.run_id = value;
                }
            } else if field_matches(&key, "rule_name") {
                if let Some(value) = self.string_field("Proposal", "rule_name", "string") {
                    proposal.rule_name = value;
                }
            } else if field_matches(&key, "created") {
                if let Some(value) = self.time_field()? {
                    proposal.created = value;
                }
            } else if field_matches(&key, "items") {
                self.items_field(&mut proposal.items)?;
            } else if field_matches(&key, "status") {
                if let Some(value) = self.string_field("Proposal", "status", "string") {
                    proposal.status = value;
                }
            } else {
                self.skip_value();
            }
            if self.object_done() {
                return Ok(proposal);
            }
        }
    }

    fn items_field(&mut self, target: &mut Option<Vec<ProposalItem>>) -> Result<(), String> {
        self.skip_space();
        match self.peek() {
            b'n' => {
                self.skip_value();
                *target = None;
                Ok(())
            }
            b'[' => {
                self.offset += 1;
                let mut items = Vec::new();
                self.skip_space();
                if self.peek() == b']' {
                    self.offset += 1;
                    *target = Some(items);
                    return Ok(());
                }
                loop {
                    items.push(self.proposal_item()?);
                    self.skip_space();
                    match self.peek() {
                        b']' => {
                            self.offset += 1;
                            *target = Some(items);
                            return Ok(());
                        }
                        b',' => {
                            self.offset += 1;
                            self.skip_space();
                        }
                        _ => unreachable!("syntax was validated"),
                    }
                }
            }
            _ => {
                let error = go_unmarshal_field_error(
                    self.value_kind(),
                    "Proposal",
                    "items",
                    "[]retention.ProposalItem",
                );
                self.record_type_error(error);
                self.skip_value();
                Ok(())
            }
        }
    }

    fn proposal_item(&mut self) -> Result<ProposalItem, String> {
        self.skip_space();
        match self.peek() {
            b'n' => {
                self.skip_value();
                Ok(ProposalItem::default())
            }
            b'{' => self.proposal_item_object(),
            _ => {
                let error = go_unmarshal_field_error(
                    self.value_kind(),
                    "Proposal",
                    "items",
                    "retention.ProposalItem",
                );
                self.record_type_error(error);
                self.skip_value();
                Ok(ProposalItem::default())
            }
        }
    }

    fn proposal_item_object(&mut self) -> Result<ProposalItem, String> {
        self.consume(b'{');
        let mut item = ProposalItem::default();
        self.skip_space();
        if self.peek() == b'}' {
            self.offset += 1;
            return Ok(item);
        }
        loop {
            let key = self.parse_string();
            self.consume(b':');
            if field_matches(&key, "path") {
                if let Some(value) = self.string_field("ProposalItem", "items.path", "string") {
                    item.path = value;
                }
            } else if field_matches(&key, "title") {
                if let Some(value) = self.string_field("ProposalItem", "items.title", "string") {
                    item.title = value;
                }
            } else if field_matches(&key, "reference_date") {
                if let Some(value) =
                    self.string_field("ProposalItem", "items.reference_date", "string")
                {
                    item.reference_date = value;
                }
            } else if field_matches(&key, "expires_at") {
                if let Some(value) = self.string_field("ProposalItem", "items.expires_at", "string")
                {
                    item.expires_at = value;
                }
            } else if field_matches(&key, "action") {
                if let Some(value) =
                    self.string_field("ProposalItem", "items.action", "retention.Action")
                {
                    item.action = value;
                }
            } else if field_matches(&key, "rule_name") {
                if let Some(value) = self.string_field("ProposalItem", "items.rule_name", "string")
                {
                    item.rule_name = value;
                }
            } else if field_matches(&key, "fingerprint") {
                if let Some(value) =
                    self.string_field("ProposalItem", "items.fingerprint", "string")
                {
                    item.fingerprint = value;
                }
            } else if field_matches(&key, "status") {
                if let Some(value) = self.string_field("ProposalItem", "items.status", "string") {
                    item.status = value;
                }
            } else if field_matches(&key, "failure") {
                if let Some(value) = self.string_field("ProposalItem", "items.failure", "string") {
                    item.failure = value;
                }
            } else {
                self.skip_value();
            }
            if self.object_done() {
                return Ok(item);
            }
        }
    }

    fn history(&mut self) -> Result<Option<Vec<HistoryEntry>>, String> {
        self.skip_space();
        match self.peek() {
            b'n' => {
                self.skip_value();
                Ok(None)
            }
            b'[' => {
                self.offset += 1;
                let mut entries = Vec::new();
                self.skip_space();
                if self.peek() == b']' {
                    self.offset += 1;
                    return self.finish(Some(entries));
                }
                loop {
                    entries.push(self.history_entry()?);
                    self.skip_space();
                    match self.peek() {
                        b']' => {
                            self.offset += 1;
                            return self.finish(Some(entries));
                        }
                        b',' => {
                            self.offset += 1;
                            self.skip_space();
                        }
                        _ => unreachable!("syntax was validated"),
                    }
                }
            }
            _ => Err(go_unmarshal_value_error(
                self.value_kind(),
                "[]retention.HistoryEntry",
            )),
        }
    }

    fn history_entry(&mut self) -> Result<HistoryEntry, String> {
        self.skip_space();
        match self.peek() {
            b'n' => {
                self.skip_value();
                Ok(HistoryEntry::default())
            }
            b'{' => self.history_entry_object(),
            _ => {
                let error = go_unmarshal_value_error(self.value_kind(), "retention.HistoryEntry");
                self.record_type_error(error);
                self.skip_value();
                Ok(HistoryEntry::default())
            }
        }
    }

    fn history_entry_object(&mut self) -> Result<HistoryEntry, String> {
        self.consume(b'{');
        let mut entry = HistoryEntry::default();
        self.skip_space();
        if self.peek() == b'}' {
            self.offset += 1;
            return Ok(entry);
        }
        loop {
            let key = self.parse_string();
            self.consume(b':');
            if field_matches(&key, "action_id") {
                if let Some(value) = self.string_field("HistoryEntry", "action_id", "string") {
                    entry.action_id = value;
                }
            } else if field_matches(&key, "timestamp") {
                if let Some(value) = self.time_field()? {
                    entry.timestamp = value;
                }
            } else if field_matches(&key, "rule_name") {
                if let Some(value) = self.string_field("HistoryEntry", "rule_name", "string") {
                    entry.rule_name = value;
                }
            } else if field_matches(&key, "action") {
                if let Some(value) = self.string_field("HistoryEntry", "action", "retention.Action")
                {
                    entry.action = value;
                }
            } else if field_matches(&key, "path") {
                if let Some(value) = self.string_field("HistoryEntry", "path", "string") {
                    entry.path = value;
                }
            } else if field_matches(&key, "title") {
                if let Some(value) = self.string_field("HistoryEntry", "title", "string") {
                    entry.title = value;
                }
            } else {
                self.skip_value();
            }
            if self.object_done() {
                return Ok(entry);
            }
        }
    }

    fn string_field(&mut self, structure: &str, field: &str, go_type: &str) -> Option<String> {
        self.skip_space();
        match self.peek() {
            b'n' => {
                self.skip_value();
                None
            }
            b'"' => Some(self.parse_string()),
            _ => {
                let error = go_unmarshal_field_error(self.value_kind(), structure, field, go_type);
                self.record_type_error(error);
                self.skip_value();
                None
            }
        }
    }

    fn time_field(&mut self) -> Result<Option<OffsetDateTime>, String> {
        self.skip_space();
        match self.peek() {
            b'n' => {
                self.skip_value();
                Ok(None)
            }
            b'"' => {
                let raw = self.parse_raw_string();
                parse_go_rfc3339(&raw).map(Some)
            }
            _ => Err("Time.UnmarshalJSON: input is not a JSON string".to_owned()),
        }
    }

    fn object_done(&mut self) -> bool {
        self.skip_space();
        match self.peek() {
            b'}' => {
                self.offset += 1;
                true
            }
            b',' => {
                self.offset += 1;
                self.skip_space();
                false
            }
            _ => unreachable!("syntax was validated"),
        }
    }

    fn parse_string(&mut self) -> String {
        let raw = self.raw_string_bytes();
        unescape_json_string(raw)
    }

    fn parse_raw_string(&mut self) -> String {
        String::from_utf8_lossy(self.raw_string_bytes()).into_owned()
    }

    fn raw_string_bytes(&mut self) -> &'a [u8] {
        self.skip_space();
        debug_assert_eq!(self.data[self.offset], b'"');
        self.offset += 1;
        let start = self.offset;
        let mut escaped = false;
        loop {
            let character = self.data[self.offset];
            self.offset += 1;
            if escaped {
                escaped = false;
                continue;
            }
            match character {
                b'\\' => escaped = true,
                b'"' => return &self.data[start..self.offset - 1],
                _ => {}
            }
        }
    }

    fn skip_value(&mut self) {
        self.skip_space();
        match self.peek() {
            b'"' => {
                let _ = self.raw_string_bytes();
            }
            b'{' => {
                self.offset += 1;
                self.skip_space();
                if self.peek() == b'}' {
                    self.offset += 1;
                    return;
                }
                loop {
                    let _ = self.raw_string_bytes();
                    self.consume(b':');
                    self.skip_value();
                    if self.object_done() {
                        return;
                    }
                }
            }
            b'[' => {
                self.offset += 1;
                self.skip_space();
                if self.peek() == b']' {
                    self.offset += 1;
                    return;
                }
                loop {
                    self.skip_value();
                    self.skip_space();
                    match self.peek() {
                        b']' => {
                            self.offset += 1;
                            return;
                        }
                        b',' => {
                            self.offset += 1;
                            self.skip_space();
                        }
                        _ => unreachable!("syntax was validated"),
                    }
                }
            }
            _ => {
                while self.data.get(self.offset).is_some_and(|byte| {
                    !is_json_space(*byte) && !matches!(*byte, b',' | b']' | b'}')
                }) {
                    self.offset += 1;
                }
            }
        }
    }

    fn value_kind(&self) -> &'static str {
        match self.data[self.offset] {
            b'n' => "null",
            b't' | b'f' => "bool",
            b'"' => "string",
            b'[' => "array",
            b'{' => "object",
            _ => "number",
        }
    }
}

fn is_json_space(character: u8) -> bool {
    matches!(character, b' ' | b'\t' | b'\r' | b'\n')
}

fn field_matches(input: &str, field: &str) -> bool {
    input == field || go_folded(input).as_bytes() == go_folded(field).as_bytes()
}

fn go_folded(value: &str) -> String {
    value
        .chars()
        .map(|character| match character {
            'a'..='z' => character.to_ascii_uppercase(),
            'ſ' => 'S',
            'K' => 'K',
            other => other,
        })
        .collect()
}

fn go_unmarshal_value_error(kind: &str, go_type: &str) -> String {
    format!("json: cannot unmarshal {kind} into Go value of type {go_type}")
}

fn go_unmarshal_field_error(kind: &str, structure: &str, field: &str, go_type: &str) -> String {
    format!(
        "json: cannot unmarshal {kind} into Go struct field {structure}.{field} of type {go_type}"
    )
}

fn quote_json_character(character: u8) -> String {
    match character {
        b'\'' => "'\\''".to_owned(),
        b'"' => "'\"'".to_owned(),
        b'\\' => "'\\\\'".to_owned(),
        b'\x07' => "'\\a'".to_owned(),
        b'\x08' => "'\\b'".to_owned(),
        b'\x0c' => "'\\f'".to_owned(),
        b'\n' => "'\\n'".to_owned(),
        b'\r' => "'\\r'".to_owned(),
        b'\t' => "'\\t'".to_owned(),
        b'\x0b' => "'\\v'".to_owned(),
        b' '..=b'~' => format!("'{}'", char::from(character)),
        other => format!("'\\x{other:02x}'"),
    }
}

fn unescape_json_string(raw: &[u8]) -> String {
    let mut output = String::with_capacity(raw.len());
    let mut offset = 0usize;
    let mut plain_start = 0usize;
    while offset < raw.len() {
        if raw[offset] != b'\\' {
            offset += 1;
            continue;
        }
        output.push_str(&String::from_utf8_lossy(&raw[plain_start..offset]));
        offset += 1;
        match raw[offset] {
            b'"' => output.push('"'),
            b'\\' => output.push('\\'),
            b'/' => output.push('/'),
            b'b' => output.push('\u{0008}'),
            b'f' => output.push('\u{000c}'),
            b'n' => output.push('\n'),
            b'r' => output.push('\r'),
            b't' => output.push('\t'),
            b'u' => {
                let first = parse_hex_quad(&raw[offset + 1..offset + 5]);
                offset += 4;
                let character = if (0xd800..=0xdbff).contains(&first)
                    && raw.get(offset + 1..offset + 3) == Some(&b"\\u"[..])
                {
                    let second = parse_hex_quad(&raw[offset + 3..offset + 7]);
                    if (0xdc00..=0xdfff).contains(&second) {
                        offset += 6;
                        char::from_u32(
                            0x1_0000
                                + ((u32::from(first) - 0xd800) << 10)
                                + (u32::from(second) - 0xdc00),
                        )
                        .unwrap_or('\u{fffd}')
                    } else {
                        '\u{fffd}'
                    }
                } else {
                    char::from_u32(u32::from(first)).unwrap_or('\u{fffd}')
                };
                output.push(character);
            }
            _ => unreachable!("syntax was validated"),
        }
        offset += 1;
        plain_start = offset;
    }
    output.push_str(&String::from_utf8_lossy(&raw[plain_start..]));
    output
}

fn parse_hex_quad(raw: &[u8]) -> u16 {
    raw.iter().fold(0u16, |value, byte| {
        value * 16
            + u16::from(match byte {
                b'0'..=b'9' => byte - b'0',
                b'a'..=b'f' => byte - b'a' + 10,
                b'A'..=b'F' => byte - b'A' + 10,
                _ => unreachable!("syntax was validated"),
            })
    })
}

fn parse_go_rfc3339(value: &str) -> Result<OffsetDateTime, String> {
    match parse_rfc3339_parts(value) {
        Ok(parts) => parts.into_datetime(),
        Err(error) => Err(error.render(value)),
    }
}

#[derive(Debug)]
struct Rfc3339Parts {
    year: i32,
    month: u8,
    day: u8,
    hour: u8,
    minute: u8,
    second: u8,
    nanosecond: u32,
    offset_seconds: i32,
}

impl Rfc3339Parts {
    fn into_datetime(self) -> Result<OffsetDateTime, String> {
        let month = Month::try_from(self.month).expect("validated month");
        let date = Date::from_calendar_date(self.year, month, self.day).expect("validated date");
        let time = Time::from_hms_nano(self.hour, self.minute, self.second, self.nanosecond)
            .expect("validated clock");
        let primitive = PrimitiveDateTime::new(date, time);
        if let Ok(offset) = UtcOffset::from_whole_seconds(self.offset_seconds) {
            return Ok(primitive.assume_offset(offset));
        }
        Ok(primitive.assume_utc() - time::Duration::seconds(i64::from(self.offset_seconds)))
    }
}

struct TimeParseFailure {
    layout_element: &'static str,
    value_element: String,
    message: Option<String>,
}

impl TimeParseFailure {
    fn element(layout_element: &'static str, value_element: &str) -> Self {
        Self {
            layout_element,
            value_element: value_element.to_owned(),
            message: None,
        }
    }

    fn message(message: impl Into<String>) -> Self {
        Self {
            layout_element: "",
            value_element: String::new(),
            message: Some(message.into()),
        }
    }

    fn render(self, value: &str) -> String {
        if let Some(message) = self.message {
            format!("parsing time {}{message}", go_time_quote(value))
        } else {
            format!(
                "parsing time {} as {}: cannot parse {} as {}",
                go_time_quote(value),
                go_time_quote(RFC3339_LAYOUT),
                go_time_quote(&self.value_element),
                go_time_quote(self.layout_element)
            )
        }
    }
}

fn parse_rfc3339_parts(value: &str) -> Result<Rfc3339Parts, TimeParseFailure> {
    let bytes = value.as_bytes();
    let mut offset = 0usize;
    let year = parse_fixed_number(bytes, &mut offset, 4, "2006", 0, 9999)? as i32;
    consume_time_literal(bytes, &mut offset, b'-', "-")?;
    let month = parse_fixed_number(bytes, &mut offset, 2, "01", 1, 12)? as u8;
    consume_time_literal(bytes, &mut offset, b'-', "-")?;
    let day = parse_fixed_number(bytes, &mut offset, 2, "02", 1, 31)? as u8;
    consume_time_literal(bytes, &mut offset, b'T', "T")?;
    let hour = parse_fixed_number(bytes, &mut offset, 2, "15", 0, 23)? as u8;
    consume_time_literal(bytes, &mut offset, b':', ":")?;
    let minute = parse_fixed_number(bytes, &mut offset, 2, "04", 0, 59)? as u8;
    consume_time_literal(bytes, &mut offset, b':', ":")?;
    let second = parse_fixed_number(bytes, &mut offset, 2, "05", 0, 59)? as u8;

    let month_enum = Month::try_from(month).expect("validated month");
    if Date::from_calendar_date(year, month_enum, day).is_err() {
        return Err(TimeParseFailure::message(": day out of range"));
    }

    let mut nanosecond = 0u32;
    if bytes
        .get(offset)
        .is_some_and(|byte| matches!(*byte, b'.' | b','))
        && bytes
            .get(offset + 1)
            .is_some_and(|byte| byte.is_ascii_digit())
    {
        offset += 1;
        let fraction_start = offset;
        while bytes.get(offset).is_some_and(|byte| byte.is_ascii_digit()) {
            offset += 1;
        }
        let fraction = &bytes[fraction_start..offset];
        for (index, digit) in fraction.iter().take(9).enumerate() {
            nanosecond += u32::from(*digit - b'0') * 10u32.pow(8 - index as u32);
        }
    }

    let offset_seconds = match bytes.get(offset) {
        Some(b'Z') => {
            offset += 1;
            0
        }
        _ => {
            let remaining = &value[offset..];
            if bytes.len().saturating_sub(offset) < 6 || bytes.get(offset + 3) != Some(&b':') {
                return Err(TimeParseFailure::element("Z07:00", remaining));
            }
            let sign = bytes[offset];
            if !matches!(sign, b'+' | b'-') {
                return Err(TimeParseFailure::element("Z07:00", remaining));
            }
            offset += 1;
            let zone_hour = parse_fixed_number(bytes, &mut offset, 2, "Z07:00", 0, 24)?;
            consume_time_literal(bytes, &mut offset, b':', "Z07:00")?;
            let zone_minute = parse_fixed_number(bytes, &mut offset, 2, "Z07:00", 0, 60)?;
            let seconds = i32::try_from((zone_hour * 60 + zone_minute) * 60)
                .expect("RFC3339 offset fits i32");
            if sign == b'-' { -seconds } else { seconds }
        }
    };

    if offset != bytes.len() {
        return Err(TimeParseFailure::message(format!(
            ": extra text: {}",
            go_time_quote(&value[offset..])
        )));
    }

    Ok(Rfc3339Parts {
        year,
        month,
        day,
        hour,
        minute,
        second,
        nanosecond,
        offset_seconds,
    })
}

fn parse_fixed_number(
    bytes: &[u8],
    offset: &mut usize,
    width: usize,
    layout_element: &'static str,
    minimum: u32,
    maximum: u32,
) -> Result<u32, TimeParseFailure> {
    let remaining = String::from_utf8_lossy(bytes.get(*offset..).unwrap_or_default()).into_owned();
    let Some(raw) = bytes.get(*offset..offset.saturating_add(width)) else {
        return Err(TimeParseFailure::element(layout_element, &remaining));
    };
    if !raw.iter().all(u8::is_ascii_digit) {
        return Err(TimeParseFailure::element(layout_element, &remaining));
    }
    let value = raw
        .iter()
        .fold(0u32, |number, digit| number * 10 + u32::from(*digit - b'0'));
    *offset += width;
    if value < minimum || value > maximum {
        let message = match layout_element {
            "01" => ": month out of range",
            "02" => ": day out of range",
            "15" => ": hour out of range",
            "04" => ": minute out of range",
            "05" => ": second out of range",
            "Z07:00" if width == 2 && maximum == 24 => ": time zone offset hour out of range",
            "Z07:00" => ": time zone offset minute out of range",
            _ => return Err(TimeParseFailure::element(layout_element, &remaining)),
        };
        return Err(TimeParseFailure::message(message));
    }
    Ok(value)
}

fn consume_time_literal(
    bytes: &[u8],
    offset: &mut usize,
    expected: u8,
    layout_element: &'static str,
) -> Result<(), TimeParseFailure> {
    if bytes.get(*offset) == Some(&expected) {
        *offset += 1;
        return Ok(());
    }
    Err(TimeParseFailure::element(
        layout_element,
        &String::from_utf8_lossy(bytes.get(*offset..).unwrap_or_default()),
    ))
}

fn go_time_quote(value: &str) -> String {
    let mut output = String::with_capacity(value.len() + 2);
    output.push('"');
    for byte in value.as_bytes() {
        match *byte {
            b'"' => output.push_str("\\\""),
            b'\\' => output.push_str("\\\\"),
            b' '..=b'~' => output.push(char::from(*byte)),
            other => output.push_str(&format!("\\x{other:02x}")),
        }
    }
    output.push('"');
    output
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn syntax_scanner_matches_go_counterexamples() {
        for (input, expected) in [
            (
                br#"{"run_id":truX}"#.as_slice(),
                "invalid character 'X' in literal true (expecting 'e')",
            ),
            (
                br#"{"run_id":"probe",}"#.as_slice(),
                "invalid character '}' looking for beginning of object key string",
            ),
            (
                br#"[{"timestamp":"2026-01-02T03:04:05Z"},]"#.as_slice(),
                "invalid character ']' looking for beginning of value",
            ),
        ] {
            assert_eq!(validate_syntax(input).unwrap_err(), expected);
        }
    }

    #[test]
    fn timestamp_diagnostics_match_go() {
        for (value, expected) in [
            (
                "not-a-time",
                "parsing time \"not-a-time\" as \"2006-01-02T15:04:05Z07:00\": cannot parse \"not-a-time\" as \"2006\"",
            ),
            (
                "2026-13-01T00:00:00Z",
                "parsing time \"2026-13-01T00:00:00Z\": month out of range",
            ),
            (
                "2026-01-32T00:00:00Z",
                "parsing time \"2026-01-32T00:00:00Z\": day out of range",
            ),
            (
                "2026-01-01 00:00:00Z",
                "parsing time \"2026-01-01 00:00:00Z\" as \"2006-01-02T15:04:05Z07:00\": cannot parse \" 00:00:00Z\" as \"T\"",
            ),
            (
                "2026-01-01T00:00:00",
                "parsing time \"2026-01-01T00:00:00\" as \"2006-01-02T15:04:05Z07:00\": cannot parse \"\" as \"Z07:00\"",
            ),
            (
                "2026-01-01T00:00:00+25:00",
                "parsing time \"2026-01-01T00:00:00+25:00\": time zone offset hour out of range",
            ),
            (
                "2026-01-01T00:00:00+01:99",
                "parsing time \"2026-01-01T00:00:00+01:99\": time zone offset minute out of range",
            ),
        ] {
            assert_eq!(parse_go_rfc3339(value).unwrap_err(), expected);
        }
    }

    #[test]
    fn json_string_unescape_handles_surrogates() {
        assert_eq!(unescape_json_string(br"a\n\u0062\ud83d\ude00"), "a\nb😀");
        assert_eq!(unescape_json_string(br"\ud800x"), "�x");
    }

    #[test]
    fn null_time_default_is_go_zero() {
        assert_eq!(super::super::go_zero_time().year(), 1);
    }
}
