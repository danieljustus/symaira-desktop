use std::{cmp::Ordering, fmt::Write as _};

use noyalib::{Mapping, Number, Value};
use unicode_general_category::{GeneralCategory, get_general_category};

const INDENT: usize = 4;
const MAX_DEPTH: usize = 128;

pub(super) fn render_mapping(mapping: &Mapping) -> Result<String, String> {
    render_mapping_at(mapping, 0)
}

pub(super) fn format_scalar_number(number: &Number, nested: bool) -> Result<String, String> {
    match number {
        Number::Integer(value) => Ok(value.to_string()),
        Number::Unsigned(value) => Ok(value.to_string()),
        Number::Float(value) => Ok(format_float(*value, if nested { 'g' } else { 'f' })),
        _ => Err("unsupported noyalib number variant".to_owned()),
    }
}

fn render_mapping_at(mapping: &Mapping, depth: usize) -> Result<String, String> {
    ensure_depth(depth)?;
    if mapping.is_empty() {
        return Ok("{}".to_owned());
    }

    let mut entries: Vec<_> = mapping.iter().collect();
    if let Some(field_order) = struct_field_order(mapping) {
        entries.sort_by_key(|(key, _)| {
            field_order
                .iter()
                .position(|field| *field == key.as_str())
                .unwrap_or(usize::MAX)
        });
    } else {
        entries.sort_by(|(left, _), (right, _)| natural_cmp(left, right));
    }

    let mut lines = Vec::new();
    for (key, value) in entries {
        if key.contains('\n') {
            let rendered_key = render_string(key, (depth + 1) * INDENT, false)?;
            let mut key_lines = rendered_key.split('\n');
            let first_key = key_lines.next().unwrap_or_default();
            lines.push(format!("{}? {first_key}", spaces(depth)));
            lines.extend(key_lines.map(str::to_owned));
            let val_prefix = format!("{}:", spaces(depth));
            append_mapping_value(&mut lines, &val_prefix, value, depth)?;
            continue;
        }

        let prefix = format!(
            "{}{}:",
            spaces(depth),
            render_string(key, depth * INDENT, true)?
        );
        append_mapping_value(&mut lines, &prefix, value, depth)?;
    }
    Ok(lines.join("\n"))
}

fn struct_field_order(mapping: &Mapping) -> Option<&'static [&'static str]> {
    const PROPERTY_CONFIG: &[&str] = &["type", "label", "options", "description", "default"];
    const VIEW: &[&str] = &[
        "id",
        "name",
        "type",
        "group_by",
        "date_property",
        "computed",
        "filters",
        "filter_group",
        "sorts",
        "columns",
        "source",
        "template",
    ];
    const FILTER: &[&str] = &["key", "operator", "value"];
    const FILTER_GROUP: &[&str] = &["operator", "filters", "groups"];
    const SORT: &[&str] = &["key", "ascending"];
    const COMPUTED_COLUMN: &[&str] = &["formula", "rollup"];
    const TEMPLATE: &[&str] = &["ref", "defaults"];

    let has_only_fields = |fields: &[&str]| {
        mapping
            .iter()
            .all(|(key, _)| fields.contains(&key.as_str()))
    };
    if mapping.contains_key("id") && mapping.contains_key("name") && has_only_fields(VIEW) {
        Some(VIEW)
    } else if mapping.len() > 1 && has_only_fields(PROPERTY_CONFIG) {
        Some(PROPERTY_CONFIG)
    } else if mapping.contains_key("key")
        && mapping.contains_key("value")
        && has_only_fields(FILTER)
    {
        Some(FILTER)
    } else if mapping.contains_key("operator")
        && (mapping.contains_key("filters") || mapping.contains_key("groups"))
        && has_only_fields(FILTER_GROUP)
    {
        Some(FILTER_GROUP)
    } else if mapping.contains_key("key")
        && mapping.contains_key("ascending")
        && has_only_fields(SORT)
    {
        Some(SORT)
    } else if (mapping.contains_key("formula") || mapping.contains_key("rollup"))
        && has_only_fields(COMPUTED_COLUMN)
    {
        Some(COMPUTED_COLUMN)
    } else if (mapping.contains_key("ref") || mapping.contains_key("defaults"))
        && has_only_fields(TEMPLATE)
    {
        Some(TEMPLATE)
    } else {
        None
    }
}

fn append_mapping_value(
    lines: &mut Vec<String>,
    prefix: &str,
    value: &Value,
    depth: usize,
) -> Result<(), String> {
    match value {
        Value::Mapping(child) => {
            if child.is_empty() {
                lines.push(format!("{prefix} {{}}"));
            } else {
                lines.push(prefix.to_owned());
                lines.extend(
                    render_mapping_at(child, depth + 1)?
                        .lines()
                        .map(str::to_owned),
                );
            }
        }
        Value::Sequence(items) => {
            if items.is_empty() {
                lines.push(format!("{prefix} []"));
            } else {
                lines.push(prefix.to_owned());
                lines.extend(render_sequence(items, depth + 1)?);
            }
        }
        _ => {
            let rendered = render_value_at_indent(value, depth, (depth + 1) * INDENT)?;
            let mut val_lines = rendered.split('\n');
            let first = val_lines.next().unwrap_or_default();
            lines.push(format!("{prefix} {first}"));
            lines.extend(val_lines.map(str::to_owned));
        }
    }
    Ok(())
}

fn render_sequence(items: &[Value], depth: usize) -> Result<Vec<String>, String> {
    ensure_depth(depth)?;
    let mut lines = Vec::new();
    let prefix = spaces(depth);
    for item in items {
        match item {
            Value::Mapping(mapping) => {
                if mapping.is_empty() {
                    lines.push(format!("{prefix}- {{}}"));
                    continue;
                }
                let mut entries: Vec<_> = mapping.iter().collect();
                if let Some(field_order) = struct_field_order(mapping) {
                    entries.sort_by_key(|(key, _)| {
                        field_order
                            .iter()
                            .position(|field| *field == key.as_str())
                            .unwrap_or(usize::MAX)
                    });
                } else {
                    entries.sort_by(|(left, _), (right, _)| natural_cmp(left, right));
                }

                let (first_key, first_val) = entries[0];
                if first_key.contains('\n') {
                    let rendered_key = render_string(first_key, (depth + 1) * INDENT, false)?;
                    let mut key_lines = rendered_key.split('\n');
                    let first_key_line = key_lines.next().unwrap_or_default();
                    lines.push(format!("{prefix}- ? {first_key_line}"));
                    lines.extend(key_lines.map(str::to_owned));
                    let val_prefix = format!("{prefix}  :");
                    append_mapping_value(&mut lines, &val_prefix, first_val, depth)?;
                } else {
                    let rendered_first_key = render_string(first_key, depth * INDENT + 2, true)?;
                    let prefix_with_key = format!("{prefix}- {rendered_first_key}:");
                    append_mapping_value(&mut lines, &prefix_with_key, first_val, depth)?;
                }

                for (key, val) in &entries[1..] {
                    if key.contains('\n') {
                        let rendered_key = render_string(key, (depth + 1) * INDENT, false)?;
                        let mut key_lines = rendered_key.split('\n');
                        let first_key_line = key_lines.next().unwrap_or_default();
                        lines.push(format!("{prefix}  ? {first_key_line}"));
                        lines.extend(key_lines.map(str::to_owned));
                        let val_prefix = format!("{prefix}  :");
                        append_mapping_value(&mut lines, &val_prefix, val, depth)?;
                    } else {
                        let rendered_k = render_string(key, depth * INDENT + 2, true)?;
                        let prefix_with_key = format!("{prefix}  {rendered_k}:");
                        append_mapping_value(&mut lines, &prefix_with_key, val, depth)?;
                    }
                }
            }
            Value::Sequence(nested) => {
                if nested.is_empty() {
                    lines.push(format!("{prefix}- []"));
                } else {
                    let nested_lines = render_sequence(nested, depth + 1)?;
                    let mut nested_lines = nested_lines.into_iter();
                    let first = nested_lines.next().unwrap_or_default();
                    let first = first.strip_prefix(&spaces(depth + 1)).unwrap_or(&first);
                    lines.push(format!("{prefix}- {first}"));
                    let child_prefix = spaces(depth + 1);
                    lines.extend(nested_lines.map(|line| {
                        if line.is_empty() {
                            line
                        } else {
                            let stripped = line.strip_prefix(&child_prefix).unwrap_or(&line);
                            format!("{prefix}  {stripped}")
                        }
                    }));
                }
            }
            _ => {
                let rendered = render_value_at_indent(item, depth, depth * INDENT + 2)?;
                let mut rendered_lines = rendered.split('\n');
                let first = rendered_lines.next().unwrap_or_default();
                lines.push(format!("{prefix}- {first}"));
                lines.extend(rendered_lines.map(str::to_owned));
            }
        }
    }
    Ok(lines)
}

fn render_value(value: &Value, depth: usize, nested_number: bool) -> Result<String, String> {
    render_value_at_indent(value, depth, (depth + 1) * INDENT).map(|rendered| {
        if let Value::Number(num) = value {
            format_scalar_number(num, nested_number).unwrap_or(rendered)
        } else {
            rendered
        }
    })
}

fn render_value_at_indent(
    value: &Value,
    depth: usize,
    indent_spaces: usize,
) -> Result<String, String> {
    ensure_depth(depth)?;
    match value {
        Value::Null => Ok("null".to_owned()),
        Value::Bool(value) => Ok(value.to_string()),
        Value::Number(value) => format_scalar_number(value, true),
        Value::String(value) => render_string(value, indent_spaces, false),
        Value::Mapping(value) => render_mapping_at(value, depth),
        Value::Sequence(value) => {
            let parts = value
                .iter()
                .map(|item| render_inline_sequence_value(item, depth))
                .collect::<Result<Vec<_>, _>>()?;
            Ok(format!("[{}]", parts.join(", ")))
        }
        Value::Tagged(_) => Err("tagged values are not supported in Go-shaped mappings".to_owned()),
    }
}

fn render_inline_sequence_value(value: &Value, depth: usize) -> Result<String, String> {
    match value {
        Value::Mapping(value) => render_mapping_at(value, depth),
        Value::Sequence(value) => {
            let parts = value
                .iter()
                .map(|item| render_inline_sequence_value(item, depth))
                .collect::<Result<Vec<_>, _>>()?;
            Ok(format!("[{}]", parts.join(", ")))
        }
        _ => render_value(value, depth, true),
    }
}

fn is_yaml_break(c: char) -> bool {
    matches!(c, '\r' | '\n' | '\u{0085}' | '\u{2028}' | '\u{2029}')
}

fn is_printable_yaml(c: char) -> bool {
    let u = c as u32;
    u == 0x0A
        || (0x20..=0x7E).contains(&u)
        || (0x00A0..=0xD7FF).contains(&u)
        || (0xE000..=0xFFFD).contains(&u) && u != 0xFEFF && u != 0xFFFE && u != 0xFFFF
        || (0x10000..=0x10FFFF).contains(&u) && (u & 0xFFFF) != 0xFFFE && (u & 0xFFFF) != 0xFFFF
}

fn is_yaml_keyword_or_number(s: &str) -> bool {
    if s.is_empty()
        || s == "~"
        || s == "<<"
        || matches!(
            s,
            "null"
                | "Null"
                | "NULL"
                | "true"
                | "True"
                | "TRUE"
                | "false"
                | "False"
                | "FALSE"
                | ".nan"
                | ".NaN"
                | ".NAN"
                | ".inf"
                | ".Inf"
                | ".INF"
                | "+.inf"
                | "+.Inf"
                | "+.INF"
                | "-.inf"
                | "-.Inf"
                | "-.INF"
        )
    {
        return true;
    }

    let plain = s.replace('_', "");
    if plain.is_empty() {
        return false;
    }

    if plain.parse::<i64>().is_ok() || plain.parse::<u64>().is_ok() {
        return true;
    }

    let digits = if let Some(stripped) = plain.strip_prefix(['+', '-']) {
        stripped
    } else {
        plain.as_str()
    };

    if let Some(hex) = digits
        .strip_prefix("0x")
        .or_else(|| digits.strip_prefix("0X"))
        && !hex.is_empty()
        && i64::from_str_radix(hex, 16).is_ok()
    {
        return true;
    }
    if let Some(oct) = digits
        .strip_prefix("0o")
        .or_else(|| digits.strip_prefix("0O"))
        && !oct.is_empty()
        && i64::from_str_radix(oct, 8).is_ok()
    {
        return true;
    }
    if digits.starts_with('0') && digits.len() > 1 && digits.chars().all(|c| matches!(c, '0'..='7'))
    {
        return true;
    }
    if let Some(bin) = digits
        .strip_prefix("0b")
        .or_else(|| digits.strip_prefix("0B"))
        && !bin.is_empty()
        && i64::from_str_radix(bin, 2).is_ok()
    {
        return true;
    }

    if is_yaml_style_float(&plain) {
        return true;
    }

    if is_yaml_timestamp(s) {
        return true;
    }

    false
}

fn is_yaml_style_float(s: &str) -> bool {
    let s = s.strip_prefix(['+', '-']).unwrap_or(s);
    if s.is_empty() {
        return false;
    }
    let (mantissa, has_exp) = match s.split_once(['e', 'E']) {
        Some((m, e)) => {
            let e = e.strip_prefix(['+', '-']).unwrap_or(e);
            if e.is_empty() || !e.chars().all(|c| c.is_ascii_digit()) {
                return false;
            }
            (m, true)
        }
        None => (s, false),
    };

    if let Some((int_part, frac_part)) = mantissa.split_once('.') {
        if int_part.is_empty() && frac_part.is_empty() {
            return false;
        }
        (int_part.is_empty() || int_part.chars().all(|c| c.is_ascii_digit()))
            && (frac_part.is_empty() || frac_part.chars().all(|c| c.is_ascii_digit()))
    } else if has_exp {
        !mantissa.is_empty() && mantissa.chars().all(|c| c.is_ascii_digit())
    } else {
        false
    }
}

fn is_yaml_timestamp(s: &str) -> bool {
    let bytes = s.as_bytes();
    bytes.len() >= 10
        && bytes[0..4].iter().all(u8::is_ascii_digit)
        && bytes[4] == b'-'
        && bytes[5..7].iter().all(u8::is_ascii_digit)
        && bytes[7] == b'-'
        && bytes[8..10].iter().all(u8::is_ascii_digit)
}

pub(super) fn render_string(
    value: &str,
    indent_spaces: usize,
    _key: bool,
) -> Result<String, String> {
    let has_newline = value.contains('\n');
    let line_breaks = value.chars().any(is_yaml_break);
    let tab_characters = value.contains('\t');
    let special_characters = value.chars().any(|c| !is_printable_yaml(c));
    let leading_space = value.starts_with(' ');
    let trailing_space = value.ends_with(' ');
    let leading_break = value.chars().next().is_some_and(is_yaml_break);
    let trailing_break = value.chars().next_back().is_some_and(is_yaml_break);

    let mut break_space = false;
    let mut space_break = false;
    let chars: Vec<char> = value.chars().collect();
    if chars.len() > 1 {
        for i in 0..chars.len() - 1 {
            if is_yaml_break(chars[i]) && chars[i + 1] == ' ' {
                break_space = true;
            }
            if chars[i] == ' ' && is_yaml_break(chars[i + 1]) {
                space_break = true;
            }
        }
    }

    let block_allowed = !trailing_space && !space_break && !special_characters;
    let single_quoted_allowed =
        !break_space && !space_break && !tab_characters && !special_characters;

    let has_indicator_start = value.starts_with([
        '#', ',', '[', ']', '{', '}', '&', '*', '!', '|', '>', '\'', '"', '%', '@', '`',
    ]) || value.starts_with("- ")
        || value.starts_with(": ")
        || value.starts_with("? ");
    let has_inner_indicator =
        value.contains(": ") || value.contains(" #") || value == "---" || value == "...";

    let can_use_plain = !is_old_bool(value)
        && !is_base60_float(value)
        && !is_yaml_keyword_or_number(value)
        && !leading_space
        && !trailing_space
        && !leading_break
        && !trailing_break
        && !line_breaks
        && !tab_characters
        && !special_characters
        && !has_indicator_start
        && !has_inner_indicator;

    if has_newline {
        if block_allowed {
            return Ok(render_literal_scalar(value, indent_spaces));
        }
        return Ok(double_quote(value));
    }

    if can_use_plain {
        return Ok(value.to_owned());
    }

    if !is_old_bool(value)
        && !is_base60_float(value)
        && !is_yaml_keyword_or_number(value)
        && single_quoted_allowed
        && (leading_space || trailing_space || line_breaks || has_indicator_start)
    {
        return Ok(single_quote(value, indent_spaces));
    }

    Ok(double_quote(value))
}

fn render_literal_scalar(value: &str, indent_spaces: usize) -> String {
    let mut header = String::from("|");
    if !value.is_empty()
        && (value.starts_with(' ') || value.chars().next().is_some_and(is_yaml_break))
    {
        header.push('4');
    }
    if value.is_empty() {
        header.push('-');
    } else {
        let chars: Vec<char> = value.chars().collect();
        let last = chars[chars.len() - 1];
        if !is_yaml_break(last) {
            header.push('-');
        } else if chars.len() == 1 {
            header.push('+');
        } else {
            let second_last = chars[chars.len() - 2];
            if is_yaml_break(second_last) {
                header.push('+');
            }
        }
    }

    let val_to_render = value.strip_suffix('\n').unwrap_or(value);
    let mut output = header.clone();
    let mut breaks = true;

    for character in val_to_render.chars() {
        if character == '\n' {
            output.push('\n');
            breaks = true;
        } else if is_yaml_break(character) {
            output.push(character);
            breaks = true;
        } else {
            if breaks {
                if output == header {
                    output.push('\n');
                }
                for _ in 0..indent_spaces {
                    output.push(' ');
                }
            }
            output.push(character);
            breaks = false;
        }
    }
    output
}

fn single_quote(value: &str, indent_spaces: usize) -> String {
    let mut output = String::with_capacity(value.len() + 10);
    output.push('\'');
    let mut breaks = false;
    for character in value.chars() {
        if character == ' ' {
            output.push(' ');
            breaks = false;
        } else if is_yaml_break(character) {
            if !breaks && character == '\n' {
                output.push('\n');
            }
            output.push(character);
            breaks = true;
        } else {
            if breaks {
                for _ in 0..indent_spaces {
                    output.push(' ');
                }
            }
            if character == '\'' {
                output.push('\'');
            }
            output.push(character);
            breaks = false;
        }
    }
    output.push('\'');
    output
}

fn double_quote(value: &str) -> String {
    let mut output = String::with_capacity(value.len() + 2);
    output.push('"');
    for character in value.chars() {
        match character {
            '"' => output.push_str("\\\""),
            '\\' => output.push_str("\\\\"),
            '\n' => output.push_str("\\n"),
            '\r' => output.push_str("\\r"),
            '\t' => output.push_str("\\t"),
            '\0' => output.push_str("\\0"),
            '\x07' => output.push_str("\\a"),
            '\x08' => output.push_str("\\b"),
            '\x0B' => output.push_str("\\v"),
            '\x0C' => output.push_str("\\f"),
            '\x1B' => output.push_str("\\e"),
            '\u{0085}' => output.push_str("\\N"),
            '\u{2028}' => output.push_str("\\L"),
            '\u{2029}' => output.push_str("\\P"),
            character if (character as u32) < 32 || character as u32 == 127 => {
                let _ = write!(output, "\\x{:02X}", character as u32);
            }
            character => output.push(character),
        }
    }
    output.push('"');
    output
}

fn format_float(value: f64, format: char) -> String {
    if value.is_nan() {
        return if format == 'f' { "NaN" } else { ".nan" }.to_owned();
    }
    if value == f64::INFINITY {
        return if format == 'f' { "+Inf" } else { ".inf" }.to_owned();
    }
    if value == f64::NEG_INFINITY {
        return if format == 'f' { "-Inf" } else { "-.inf" }.to_owned();
    }

    let shortest = format!("{value:?}");
    let decimal = Decimal::parse(&shortest);
    if decimal.is_zero() {
        return if decimal.negative { "-0" } else { "0" }.to_owned();
    }
    match format {
        'f' => decimal.fixed(),
        'g' => decimal.general(),
        _ => unreachable!("only Go f and g formats are used"),
    }
}

#[derive(Debug)]
struct Decimal {
    negative: bool,
    digits: String,
    scale: i32,
}

impl Decimal {
    fn parse(value: &str) -> Self {
        let negative = value.starts_with('-');
        let value = value.trim_start_matches(['-', '+']);
        let (mantissa, exponent) = value
            .split_once(['e', 'E'])
            .map_or((value, 0), |(mantissa, exponent)| {
                (mantissa, exponent.parse::<i32>().unwrap_or(0))
            });
        let fractional_digits = mantissa
            .find('.')
            .map_or(0, |index| mantissa.len() - index - 1);
        let mut digits: String = mantissa
            .chars()
            .filter(|&character| character != '.')
            .collect();
        let leading_zeroes = digits.bytes().take_while(|&byte| byte == b'0').count();
        if leading_zeroes == digits.len() {
            return Self {
                negative,
                digits: "0".to_owned(),
                scale: 0,
            };
        }
        if leading_zeroes > 0 {
            digits.drain(..leading_zeroes);
        }
        let mut scale = exponent - fractional_digits as i32;
        while digits.len() > 1 && digits.ends_with('0') {
            digits.pop();
            scale += 1;
        }
        Self {
            negative,
            digits,
            scale,
        }
    }

    fn is_zero(&self) -> bool {
        self.digits == "0"
    }

    fn fixed(&self) -> String {
        let mut output = String::new();
        if self.negative {
            output.push('-');
        }
        let decimal_point = self.digits.len() as i32 + self.scale;
        if decimal_point <= 0 {
            output.push_str("0.");
            for _ in 0..-decimal_point {
                output.push('0');
            }
            output.push_str(&self.digits);
        } else if decimal_point >= self.digits.len() as i32 {
            output.push_str(&self.digits);
            for _ in 0..decimal_point - self.digits.len() as i32 {
                output.push('0');
            }
        } else {
            let index = decimal_point as usize;
            output.push_str(&self.digits[..index]);
            output.push('.');
            output.push_str(&self.digits[index..]);
        }
        output
    }

    fn general(&self) -> String {
        let exponent = self.digits.len() as i32 + self.scale - 1;
        if !(-4..6).contains(&exponent) {
            let mut output = String::new();
            if self.negative {
                output.push('-');
            }
            output.push(self.digits.as_bytes()[0] as char);
            if self.digits.len() > 1 {
                output.push('.');
                output.push_str(&self.digits[1..]);
            }
            output.push('e');
            if exponent >= 0 {
                output.push('+');
            } else {
                output.push('-');
            }
            let magnitude = exponent.unsigned_abs();
            if magnitude < 10 {
                output.push('0');
            }
            let _ = write!(output, "{magnitude}");
            output
        } else {
            self.fixed()
        }
    }
}

fn spaces(depth: usize) -> String {
    " ".repeat(depth * INDENT)
}

fn ensure_depth(depth: usize) -> Result<(), String> {
    if depth > MAX_DEPTH {
        Err(format!("YAML value exceeds recursion limit ({MAX_DEPTH})"))
    } else {
        Ok(())
    }
}

fn is_old_bool(value: &str) -> bool {
    matches!(
        value,
        "y" | "Y"
            | "yes"
            | "Yes"
            | "YES"
            | "n"
            | "N"
            | "no"
            | "No"
            | "NO"
            | "on"
            | "On"
            | "ON"
            | "off"
            | "Off"
            | "OFF"
    )
}

fn is_base60_float(value: &str) -> bool {
    let value = value.strip_prefix(['+', '-']).unwrap_or(value);
    let mut chars = value.chars().peekable();
    let Some(first) = chars.next() else {
        return false;
    };
    if !first.is_ascii_digit() {
        return false;
    }
    while chars
        .peek()
        .is_some_and(|&c| c.is_ascii_digit() || c == '_')
    {
        chars.next();
    }
    let mut chunks = 0;
    while chars.peek() == Some(&':') {
        chars.next();
        let Some(d1) = chars.next() else {
            return false;
        };
        if !d1.is_ascii_digit() {
            return false;
        }
        if matches!(d1, '0'..='5') && chars.peek().is_some_and(|c| c.is_ascii_digit()) {
            chars.next();
        }
        if chars
            .peek()
            .is_some_and(|&c| c.is_ascii_digit() || c == '_')
        {
            return false;
        }
        chunks += 1;
    }
    if chunks == 0 {
        return false;
    }
    if chars.peek() == Some(&'.') {
        chars.next();
        while chars
            .peek()
            .is_some_and(|&c| c.is_ascii_digit() || c == '_')
        {
            chars.next();
        }
    }
    chars.next().is_none()
}

fn is_go_digit(character: char) -> bool {
    get_general_category(character) == GeneralCategory::DecimalNumber
}

fn is_go_letter(character: char) -> bool {
    matches!(
        get_general_category(character),
        GeneralCategory::UppercaseLetter
            | GeneralCategory::LowercaseLetter
            | GeneralCategory::TitlecaseLetter
            | GeneralCategory::ModifierLetter
            | GeneralCategory::OtherLetter
    )
}

// Ported from gopkg.in/yaml.v3 sorter.go (keyList.Less for strings):
//
// Copyright (c) 2011-2019 Canonical Ltd
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
fn natural_cmp(left: &str, right: &str) -> Ordering {
    let ar: Vec<char> = left.chars().collect();
    let br: Vec<char> = right.chars().collect();
    let mut digits = false;
    for i in 0..ar.len().min(br.len()) {
        if ar[i] == br[i] {
            digits = is_go_digit(ar[i]);
            continue;
        }
        let al = is_go_letter(ar[i]);
        let bl = is_go_letter(br[i]);
        if al && bl {
            return ar[i].cmp(&br[i]);
        }
        if al || bl {
            return if digits {
                if al {
                    Ordering::Less
                } else {
                    Ordering::Greater
                }
            } else if bl {
                Ordering::Less
            } else {
                Ordering::Greater
            };
        }
        let mut ai = i;
        let mut bi = i;
        let mut an: i64 = 0;
        let mut bn: i64 = 0;
        if ar[i] == '0' || br[i] == '0' {
            let mut j = i as isize - 1;
            while j >= 0 && is_go_digit(ar[j as usize]) {
                if ar[j as usize] != '0' {
                    an = 1;
                    bn = 1;
                    break;
                }
                j -= 1;
            }
        }
        while ai < ar.len() && is_go_digit(ar[ai]) {
            an = an
                .wrapping_mul(10)
                .wrapping_add((ar[ai] as i64).wrapping_sub('0' as i64));
            ai += 1;
        }
        while bi < br.len() && is_go_digit(br[bi]) {
            bn = bn
                .wrapping_mul(10)
                .wrapping_add((br[bi] as i64).wrapping_sub('0' as i64));
            bi += 1;
        }
        if an != bn {
            return an.cmp(&bn);
        }
        if ai != bi {
            return ai.cmp(&bi);
        }
        return ar[i].cmp(&br[i]);
    }
    ar.len().cmp(&br.len())
}

#[cfg(test)]
mod tests {
    use super::*;
    use noyalib::{Mapping, Value};

    #[test]
    fn format_float_root_and_nested_special_values() {
        assert_eq!(format_float(f64::NAN, 'f'), "NaN");
        assert_eq!(format_float(f64::INFINITY, 'f'), "+Inf");
        assert_eq!(format_float(f64::NEG_INFINITY, 'f'), "-Inf");

        assert_eq!(format_float(f64::NAN, 'g'), ".nan");
        assert_eq!(format_float(f64::INFINITY, 'g'), ".inf");
        assert_eq!(format_float(f64::NEG_INFINITY, 'g'), "-.inf");
    }

    #[test]
    fn decimal_parse_leading_zeroes_scale_fix() {
        assert_eq!(format_float(0.125, 'f'), "0.125");
        assert_eq!(format_float(-0.125, 'f'), "-0.125");
        assert_eq!(format_float(0.00125, 'f'), "0.00125");
        assert_eq!(format_float(-0.00125, 'f'), "-0.00125");

        assert_eq!(format_float(0.125, 'g'), "0.125");
        assert_eq!(format_float(-0.125, 'g'), "-0.125");
        assert_eq!(format_float(0.00125, 'g'), "0.00125");
        assert_eq!(format_float(-0.00125, 'g'), "-0.00125");

        assert_eq!(format_float(1.25, 'f'), "1.25");
        assert_eq!(format_float(1e-5, 'g'), "1e-05");
        assert_eq!(format_float(1e6, 'g'), "1e+06");
        assert_eq!(format_float(1e20, 'g'), "1e+20");
    }

    #[test]
    fn render_mapping_multiline_key() {
        let mut mapping = Mapping::new();
        mapping.insert(
            "line one\nline two".to_owned(),
            Value::from("multiline key"),
        );
        let rendered = render_mapping(&mapping).expect("render");
        assert_eq!(
            rendered,
            "? |-\n    line one\n    line two\n: multiline key"
        );
    }

    #[test]
    fn render_mapping_tab_quoting() {
        let mut mapping = Mapping::new();
        mapping.insert("tabs".to_owned(), Value::from("tab\tvalue"));
        let rendered = render_mapping(&mapping).expect("render");
        assert_eq!(rendered, "tabs: \"tab\\tvalue\"");
    }

    #[test]
    fn render_mapping_trailing_newline_block_string() {
        let mut mapping = Mapping::new();
        mapping.insert("trailing_newline".to_owned(), Value::from("line\n"));
        mapping.insert("yes".to_owned(), Value::from("yes"));
        let rendered = render_mapping(&mapping).expect("render");
        assert_eq!(rendered, "trailing_newline: |\n    line\n\"yes\": \"yes\"");
    }

    #[test]
    fn render_mapping_nested_empty_map() {
        let mut mapping = Mapping::new();
        mapping.insert("empty".to_owned(), Value::Mapping(Mapping::new()));
        let mut populated = Mapping::new();
        populated.insert("key".to_owned(), Value::from("value"));
        mapping.insert("populated".to_owned(), Value::Mapping(populated));
        let rendered = render_mapping(&mapping).expect("render");
        assert_eq!(rendered, "empty: {}\npopulated:\n    key: value");
    }

    #[test]
    fn render_mapping_sequence_of_maps() {
        let mut mapping = Mapping::new();
        let mut item1 = Mapping::new();
        item1.insert("first".to_owned(), Value::from("one"));
        item1.insert("second".to_owned(), Value::from("two"));
        let mut item2 = Mapping::new();
        item2.insert("name".to_owned(), Value::from("second"));
        mapping.insert(
            "items".to_owned(),
            Value::Sequence(vec![Value::Mapping(item1), Value::Mapping(item2)]),
        );
        let rendered = render_mapping(&mapping).expect("render");
        assert_eq!(
            rendered,
            "items:\n    - first: one\n      second: two\n    - name: second"
        );
    }

    #[test]
    fn render_mapping_nested_sequences() {
        let mut mapping = Mapping::new();
        mapping.insert(
            "matrix".to_owned(),
            Value::Sequence(vec![
                Value::Sequence(vec![Value::from("a"), Value::from("b")]),
                Value::Sequence(vec![Value::from("c"), Value::from("d")]),
            ]),
        );
        let rendered = render_mapping(&mapping).expect("render");
        assert_eq!(
            rendered,
            "matrix:\n    - - a\n      - b\n    - - c\n      - d"
        );
    }

    #[test]
    fn natural_cmp_multi_digit_prefix_and_transitivity() {
        assert_eq!(natural_cmp("a12b", "a100b"), Ordering::Less);
        assert_eq!(natural_cmp("item19", "item20"), Ordering::Less);
        assert_eq!(natural_cmp("item20", "item100"), Ordering::Less);
        assert_eq!(natural_cmp("item19", "item100"), Ordering::Less);
    }

    #[test]
    fn natural_cmp_letter_vs_digit_after_digits() {
        assert_eq!(natural_cmp("item1a", "item10"), Ordering::Less);
        assert_eq!(natural_cmp("item1a", "item1b"), Ordering::Less);
        assert_eq!(natural_cmp("item-0", "item-a"), Ordering::Less);
    }

    #[test]
    fn natural_cmp_leading_zeros() {
        assert_eq!(natural_cmp("item1", "item01"), Ordering::Less);
        assert_eq!(natural_cmp("item01", "item001"), Ordering::Less);
        assert_eq!(natural_cmp("item2", "item02"), Ordering::Less);
    }

    #[test]
    fn natural_cmp_unicode_nd_and_marks() {
        assert_eq!(natural_cmp("item\u{FF11}", "item\u{FF12}"), Ordering::Less);
        assert_eq!(natural_cmp("item\u{0661}", "item\u{0662}"), Ordering::Less);
    }

    #[test]
    fn is_base60_float_exact_cases() {
        assert!(is_base60_float("1:59"));
        assert!(is_base60_float("1:20."));
        assert!(is_base60_float("1_:2"));
        assert!(is_base60_float("+1:20"));
        assert!(is_base60_float("-0:00"));
        assert!(is_base60_float("1:20:30.4_5"));

        assert!(!is_base60_float("1:70"));
        assert!(!is_base60_float("1:2.5:3"));
        assert!(!is_base60_float("1:0_"));
        assert!(!is_base60_float("1:"));
        assert!(!is_base60_float(":20"));
        assert!(!is_base60_float("1:000"));
    }

    #[test]
    fn double_quote_escapes_expected_characters() {
        assert_eq!(double_quote("pre\x00suf"), "\"pre\\0suf\"");
        assert_eq!(double_quote("bell\x07sound"), "\"bell\\asound\"");
        assert_eq!(double_quote("back\x08space"), "\"back\\bspace\"");
        assert_eq!(double_quote("esc\x1bcode"), "\"esc\\ecode\"");
        assert_eq!(double_quote("next\u{0085}line"), "\"next\\Nline\"");
        assert_eq!(double_quote("cr\rline"), "\"cr\\rline\"");
        assert_eq!(double_quote("tab\tvalue"), "\"tab\\tvalue\"");
        assert_eq!(double_quote("ctrl\x01test"), "\"ctrl\\x01test\"");
        assert_eq!(double_quote("u2028\u{2028}line"), "\"u2028\\Lline\"");
        assert_eq!(double_quote("u2029\u{2029}para"), "\"u2029\\Ppara\"");
        assert_eq!(
            double_quote("non\u{00A0}breaking"),
            "\"non\u{00A0}breaking\""
        );
    }

    #[test]
    fn single_quote_whitespace_and_line_separators() {
        assert_eq!(single_quote("  leading", 4), "'  leading'");
        assert_eq!(single_quote("trailing  ", 4), "'trailing  '");
        assert_eq!(single_quote("line\u{2028}sep", 4), "'line\u{2028}    sep'");
        assert_eq!(single_quote("para\u{2029}sep", 4), "'para\u{2029}    sep'");
        assert_eq!(
            single_quote("one\u{2028}\u{2028}two", 4),
            "'one\u{2028}\u{2028}    two'"
        );
    }

    #[test]
    fn render_mapping_blank_line_block_string() {
        let mut mapping = Mapping::new();
        mapping.insert(
            "description".to_owned(),
            Value::from("line one\n\nline two\n"),
        );
        let rendered = render_mapping(&mapping).expect("render");
        assert_eq!(rendered, "description: |\n    line one\n\n    line two");
    }

    #[test]
    fn render_mapping_string_rendering_counterexamples() {
        let mut labels = Mapping::new();
        labels.insert("NO".to_owned(), Value::from("NO"));
        labels.insert("No".to_owned(), Value::from("No"));
        labels.insert("OFF".to_owned(), Value::from("OFF"));
        labels.insert("ON".to_owned(), Value::from("ON"));
        labels.insert("Off".to_owned(), Value::from("Off"));
        labels.insert("On".to_owned(), Value::from("On"));
        labels.insert("YES".to_owned(), Value::from("YES"));
        labels.insert("Yes".to_owned(), Value::from("Yes"));
        labels.insert("backslashes".to_owned(), Value::from("C:\\tmp\\path"));
        labels.insert("base60".to_owned(), Value::from("1:20"));
        labels.insert("newlines".to_owned(), Value::from("line one\nline two"));
        labels.insert("no".to_owned(), Value::from("no"));
        labels.insert("off".to_owned(), Value::from("off"));
        labels.insert("on".to_owned(), Value::from("on"));
        labels.insert("tabs".to_owned(), Value::from("tab\tvalue"));
        labels.insert("trailing_newline".to_owned(), Value::from("line\n"));
        labels.insert("yes".to_owned(), Value::from("yes"));

        let rendered = render_mapping(&labels).expect("render");
        let expected = "\"NO\": \"NO\"\n\"No\": \"No\"\n\"OFF\": \"OFF\"\n\"ON\": \"ON\"\n\"Off\": \"Off\"\n\"On\": \"On\"\n\"YES\": \"YES\"\n\"Yes\": \"Yes\"\nbackslashes: C:\\tmp\\path\nbase60: \"1:20\"\nnewlines: |-\n    line one\n    line two\n\"no\": \"no\"\n\"off\": \"off\"\n\"on\": \"on\"\ntabs: \"tab\\tvalue\"\ntrailing_newline: |\n    line\n\"yes\": \"yes\"";
        assert_eq!(rendered, expected);
    }

    #[test]
    fn render_mapping_string_escape_and_whitespace_corpus() {
        let mut corpus = Mapping::new();
        corpus.insert("bel".to_owned(), Value::from("bell\x07sound"));
        corpus.insert("blank".to_owned(), Value::from(""));
        corpus.insert("bs".to_owned(), Value::from("back\x08space"));
        corpus.insert("cr".to_owned(), Value::from("cr\rline"));
        corpus.insert("esc".to_owned(), Value::from("esc\x1bcode"));
        corpus.insert("leading_spaces".to_owned(), Value::from("  leading"));
        corpus.insert(
            "multiple_newlines".to_owned(),
            Value::from("multi\n\n\nnewlines\n"),
        );
        corpus.insert("nbsp".to_owned(), Value::from("non\u{00A0}breaking"));
        corpus.insert("nel".to_owned(), Value::from("next\u{0085}line"));
        corpus.insert("nul".to_owned(), Value::from("pre\x00suf"));
        corpus.insert("tab".to_owned(), Value::from("tab\tvalue"));
        corpus.insert("trailing_spaces".to_owned(), Value::from("trailing  "));
        corpus.insert("u2028".to_owned(), Value::from("line\u{2028}sep"));
        corpus.insert("u2029".to_owned(), Value::from("para\u{2029}sep"));

        let rendered = render_mapping(&corpus).expect("render");
        let expected = "bel: \"bell\\asound\"\nblank: \"\"\nbs: \"back\\bspace\"\ncr: \"cr\\rline\"\nesc: \"esc\\ecode\"\nleading_spaces: '  leading'\nmultiple_newlines: |\n    multi\n\n\n    newlines\nnbsp: non\u{00A0}breaking\nnel: \"next\\Nline\"\nnul: \"pre\\0suf\"\ntab: \"tab\\tvalue\"\ntrailing_spaces: 'trailing  '\nu2028: 'line\u{2028}    sep'\nu2029: 'para\u{2029}    sep'";
        assert_eq!(rendered, expected);
    }

    #[test]
    fn render_mapping_tab_unicode_whitespace_combinations() {
        let mut corpus = Mapping::new();
        corpus.insert(
            "tab_all_mixed".to_owned(),
            Value::from("tab\t\u{00a0}\u{2028}\u{2029}all"),
        );
        corpus.insert("tab_ls".to_owned(), Value::from("tab\t\u{2028}line"));
        corpus.insert(
            "tab_ls_ps".to_owned(),
            Value::from("tab\t\u{2028}\u{2029}mixed"),
        );
        corpus.insert("tab_nbsp".to_owned(), Value::from("tab\t\u{00a0}nbsp"));
        corpus.insert(
            "tab_nbsp_ls".to_owned(),
            Value::from("tab\t\u{00a0}\u{2028}mixed"),
        );
        corpus.insert(
            "tab_nbsp_ps".to_owned(),
            Value::from("tab\t\u{00a0}\u{2029}mixed"),
        );
        corpus.insert("tab_ps".to_owned(), Value::from("tab\t\u{2029}para"));

        let rendered = render_mapping(&corpus).expect("render");
        let expected = "tab_all_mixed: \"tab\\t\u{00a0}\\L\\Pall\"\ntab_ls: \"tab\\t\\Lline\"\ntab_ls_ps: \"tab\\t\\L\\Pmixed\"\ntab_nbsp: \"tab\\t\u{00a0}nbsp\"\ntab_nbsp_ls: \"tab\\t\u{00a0}\\Lmixed\"\ntab_nbsp_ps: \"tab\\t\u{00a0}\\Pmixed\"\ntab_ps: \"tab\\t\\Ppara\"";
        assert_eq!(rendered, expected);
    }

    #[test]
    fn render_mapping_multiline_whitespace_quotes_separators() {
        let mut corpus = Mapping::new();
        corpus.insert(
            "consecutive_ls".to_owned(),
            Value::from("one\u{2028}\u{2028}two"),
        );
        corpus.insert(
            "consecutive_ls_ps".to_owned(),
            Value::from("one\u{2028}\u{2029}two"),
        );
        corpus.insert(
            "consecutive_ps".to_owned(),
            Value::from("one\u{2029}\u{2029}two"),
        );
        corpus.insert(
            "double_quotes".to_owned(),
            Value::from("line one \"quoted\"\nline two"),
        );
        corpus.insert(
            "leading_spaces".to_owned(),
            Value::from("  line one\nline two"),
        );
        corpus.insert(
            "line_trailing_spaces".to_owned(),
            Value::from("line one  \nline two"),
        );
        corpus.insert("newline_ls".to_owned(), Value::from("one\n\u{2028}two"));
        corpus.insert("newline_ps".to_owned(), Value::from("one\n\u{2029}two"));
        corpus.insert(
            "single_and_double_quotes".to_owned(),
            Value::from("line 'one'\n\"line two\""),
        );
        corpus.insert(
            "trailing_spaces".to_owned(),
            Value::from("line one\nline two  "),
        );

        let rendered = render_mapping(&corpus).expect("render");
        let expected = "consecutive_ls: 'one\u{2028}\u{2028}    two'\nconsecutive_ls_ps: 'one\u{2028}\u{2029}    two'\nconsecutive_ps: 'one\u{2029}\u{2029}    two'\ndouble_quotes: |-\n    line one \"quoted\"\n    line two\nleading_spaces: |4-\n      line one\n    line two\nline_trailing_spaces: \"line one  \\nline two\"\nnewline_ls: |-\n    one\n\u{2028}    two\nnewline_ps: |-\n    one\n\u{2029}    two\nsingle_and_double_quotes: |-\n    line 'one'\n    \"line two\"\ntrailing_spaces: \"line one\\nline two  \"";
        assert_eq!(rendered, expected);
    }

    #[test]
    fn render_mapping_nested_sequences_multiline_block_values() {
        let mut details = Mapping::new();
        let mut nested_map = Mapping::new();
        nested_map.insert(
            "leading_space_multiline".to_owned(),
            Value::from("  line one\nline two"),
        );
        nested_map.insert("normal".to_owned(), Value::from("value"));
        details.insert(
            "nested_mapping_leading_space".to_owned(),
            Value::Mapping(nested_map),
        );

        details.insert(
            "sequence_block_strings".to_owned(),
            Value::Sequence(vec![
                Value::from("line one\nline two"),
                Value::from("  leading space"),
                Value::from("  line one\nline two"),
            ]),
        );

        let mut multiline_key_map = Mapping::new();
        multiline_key_map.insert(
            "line one\nline two".to_owned(),
            Value::from("multiline key in sequence mapping"),
        );
        details.insert(
            "sequence_with_mapping_multiline_key".to_owned(),
            Value::Sequence(vec![Value::Mapping(multiline_key_map)]),
        );

        let rendered = render_mapping(&details).expect("render");
        let expected = "nested_mapping_leading_space:\n    leading_space_multiline: |4-\n          line one\n        line two\n    normal: value\nsequence_block_strings:\n    - |-\n      line one\n      line two\n    - '  leading space'\n    - |4-\n        line one\n      line two\nsequence_with_mapping_multiline_key:\n    - ? |-\n        line one\n        line two\n      : multiline key in sequence mapping";
        assert_eq!(rendered, expected);
    }

    #[test]
    fn render_mapping_sequence_of_nested_map_sequence() {
        let mut level1 = Mapping::new();
        let mut nested = Mapping::new();
        nested.insert(
            "inner_list".to_owned(),
            Value::Sequence(vec![Value::from("elem1"), Value::from("elem2")]),
        );
        let mut inner_map = Mapping::new();
        inner_map.insert("leaf".to_owned(), Value::from("value"));
        nested.insert("inner_map".to_owned(), Value::Mapping(inner_map));

        let mut item = Mapping::new();
        item.insert("nested".to_owned(), Value::Mapping(nested));

        level1.insert(
            "items".to_owned(),
            Value::Sequence(vec![Value::Mapping(item)]),
        );

        let mut root = Mapping::new();
        root.insert("level1".to_owned(), Value::Mapping(level1));

        let rendered = render_mapping(&root).expect("render");
        let expected = "level1:\n    items:\n        - nested:\n            inner_list:\n                - elem1\n                - elem2\n            inner_map:\n                leaf: value";
        assert_eq!(rendered, expected);
    }

    #[test]
    fn render_mapping_sequence_of_maps_multiline_keys_depth() {
        let mut level1 = Mapping::new();
        let mut item1 = Mapping::new();
        item1.insert("line one\nline two".to_owned(), Value::from("scalar value"));
        let mut item2 = Mapping::new();
        item2.insert("first_normal".to_owned(), Value::from("normal value"));
        item2.insert(
            "second\nmultiline".to_owned(),
            Value::from("multiline value"),
        );
        level1.insert(
            "items".to_owned(),
            Value::Sequence(vec![Value::Mapping(item1), Value::Mapping(item2)]),
        );

        let mut root = Mapping::new();
        root.insert("level1".to_owned(), Value::Mapping(level1));

        let rendered = render_mapping(&root).expect("render");
        let expected = "level1:\n    items:\n        - ? |-\n            line one\n            line two\n          : scalar value\n        - first_normal: normal value\n          ? |-\n            second\n            multiline\n          : multiline value";
        assert_eq!(rendered, expected);
    }
}
