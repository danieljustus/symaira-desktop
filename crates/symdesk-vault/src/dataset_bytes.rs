//! Byte-preserving Go CSV import values. JSON is a projection, never the source
//! of CSV hashes, identities, SQLite TEXT values or binary YAML scalars.

use std::collections::{BTreeMap, HashSet};

use base64::{Engine as _, engine::general_purpose::STANDARD as BASE64};

use super::DatasetError;

#[derive(Clone, Debug)]
pub struct Property {
    pub config: crate::PropertyConfig,
    pub label: Vec<u8>,
}

pub type Schema = BTreeMap<Vec<u8>, Property>;

#[derive(Clone, Debug)]
pub enum Value {
    Text(Vec<u8>),
    Number(f64),
    Boolean(bool),
}

#[derive(Clone, Debug)]
pub struct Row {
    pub key: Vec<u8>,
    pub identity: Vec<u8>,
    pub values: BTreeMap<Vec<u8>, Value>,
    pub source_path: String,
    pub row_number: usize,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct SidecarRow {
    pub dataset_slug: String,
    pub row_key: Vec<u8>,
    pub identity: Vec<u8>,
    pub values_json: String,
    pub source_path: String,
    pub row_number: usize,
}

/// Go consumes one byte for every invalid UTF-8 rune, including incomplete
/// multi-byte sequences. Rust's lossy conversion groups some of those bytes.
fn segments(mut input: &[u8], mut valid: impl FnMut(&str), mut invalid: impl FnMut(u8)) {
    while !input.is_empty() {
        match std::str::from_utf8(input) {
            Ok(text) => {
                valid(text);
                break;
            }
            Err(error) => {
                let count = error.valid_up_to();
                valid(std::str::from_utf8(&input[..count]).expect("validated prefix"));
                invalid(input[count]);
                input = &input[count + 1..];
            }
        }
    }
}

#[must_use]
pub fn text(input: &[u8]) -> String {
    let output = std::cell::RefCell::new(String::new());
    segments(
        input,
        |part| output.borrow_mut().push_str(part),
        |_| output.borrow_mut().push('\u{fffd}'),
    );
    output.into_inner()
}

fn escaped(input: &[u8], json: bool) -> String {
    let output = std::cell::RefCell::new(String::from("\""));
    segments(
        input,
        |part| {
            let part = if json {
                super::go_json_string(part)
            } else {
                super::go_quote(part)
            };
            output.borrow_mut().push_str(&part[1..part.len() - 1]);
        },
        |byte| {
            if json {
                output.borrow_mut().push_str("\\ufffd");
            } else {
                output.borrow_mut().push_str(&format!("\\x{byte:02x}"));
            }
        },
    );
    output.borrow_mut().push('"');
    output.into_inner()
}

fn quote(input: &[u8]) -> String {
    escaped(input, false)
}

fn trim(input: &[u8]) -> &[u8] {
    fn space(character: char) -> bool {
        matches!(character, '\u{0009}'..='\u{000d}' | '\u{0020}' | '\u{0085}' | '\u{00a0}' | '\u{1680}' | '\u{2000}'..='\u{200a}' | '\u{2028}' | '\u{2029}' | '\u{202f}' | '\u{205f}' | '\u{3000}')
    }
    let mut start = 0;
    while start < input.len() {
        let width = (1..=4).find(|width| {
            input
                .get(start..start + width)
                .is_some_and(|part| std::str::from_utf8(part).is_ok())
        });
        let Some(width) = width else {
            break;
        };
        let part = std::str::from_utf8(&input[start..start + width]).expect("validated rune");
        if !space(part.chars().next().expect("one rune")) {
            break;
        }
        start += width;
    }
    let mut end = input.len();
    while end > start {
        let width = (1..=4).find(|width| {
            *width <= end - start && std::str::from_utf8(&input[end - width..end]).is_ok()
        });
        let Some(width) = width else {
            break;
        };
        let part = std::str::from_utf8(&input[end - width..end]).expect("validated rune");
        if !space(part.chars().next().expect("one rune")) {
            break;
        }
        end -= width;
    }
    &input[start..end]
}

pub fn declared(schema: &BTreeMap<String, crate::PropertyConfig>) -> Schema {
    schema
        .iter()
        .map(|(key, config)| {
            (
                key.as_bytes().to_vec(),
                Property {
                    config: config.clone(),
                    label: config.label.as_bytes().to_vec(),
                },
            )
        })
        .collect()
}

pub fn projected_schema(schema: &Schema) -> BTreeMap<String, crate::PropertyConfig> {
    schema
        .iter()
        .map(|(key, property)| {
            let mut config = property.config.clone();
            config.label = text(&property.label);
            (text(key), config)
        })
        .collect()
}

pub fn parse_csv(
    input: &[u8],
    declared: &Schema,
    identity_field: &str,
) -> Result<(Vec<Row>, Schema), DatasetError> {
    let records = super::read_all_csv_bytes(input)?;
    let Some(first) = records.first() else {
        return Err(DatasetError::EmptyCsv);
    };
    let mut headers = Vec::with_capacity(first.len());
    let mut seen = HashSet::new();
    for (index, raw) in first.iter().enumerate() {
        let header = trim(raw).to_vec();
        if header.is_empty() {
            return Err(DatasetError::EmptyColumnName(index + 1));
        }
        if !seen.insert(super::go_lowercase(&text(&header))) {
            return Err(DatasetError::HandleRender(format!(
                "csv has duplicate column {}",
                quote(&header)
            )));
        }
        headers.push(header);
    }
    if !identity_field.is_empty()
        && !headers
            .iter()
            .any(|header| super::go_equal_fold(&text(header), identity_field))
    {
        return Err(DatasetError::MissingIdentity(identity_field.to_owned()));
    }
    let mut schema = Schema::new();
    for (column, header) in headers.iter().enumerate() {
        let mut property = declared.get(header).cloned().unwrap_or_else(|| Property {
            config: crate::PropertyConfig::default(),
            label: Vec::new(),
        });
        if property.config.r#type.is_empty() {
            let values = records
                .iter()
                .skip(1)
                .map(|record| text(trim(&record[column])))
                .collect::<Vec<_>>();
            property.config.r#type = super::infer_type(&values);
        }
        if property.label.is_empty() {
            property.label.clone_from(header);
        }
        schema.insert(header.clone(), property);
    }
    let mut rows = Vec::new();
    for (offset, record) in records.iter().enumerate().skip(1) {
        let mut values = BTreeMap::new();
        let mut canonical = BTreeMap::new();
        let mut identity = Vec::new();
        for (column, header) in headers.iter().enumerate() {
            let raw = trim(&record[column]);
            let kind = schema[header].config.r#type.trim().to_lowercase();
            let value = if raw.is_empty() {
                Value::Text(Vec::new())
            } else {
                let utf8 = std::str::from_utf8(raw).ok();
                let converted = match kind.as_str() {
                    "number" => utf8.and_then(super::parse_go_float).map(Value::Number),
                    "checkbox" | "boolean" | "bool" => {
                        utf8.and_then(super::parse_go_bool).map(Value::Boolean)
                    }
                    "date" => utf8
                        .filter(|value| super::parse_date(value))
                        .map(|_| Value::Text(raw.to_vec())),
                    _ => Some(Value::Text(raw.to_vec())),
                };
                converted.ok_or_else(|| {
                    DatasetError::HandleRender(format!(
                        "row {} column {}: invalid {} {}",
                        offset + 1,
                        quote(header),
                        if matches!(kind.as_str(), "checkbox" | "bool") {
                            "boolean"
                        } else {
                            kind.as_str()
                        },
                        quote(raw)
                    ))
                })?
            };
            values.insert(header.clone(), value);
            canonical.insert(header.clone(), raw.to_vec());
            if !identity_field.is_empty() && super::go_equal_fold(&text(header), identity_field) {
                identity = raw.to_vec();
            }
        }
        let mut digest_input = Vec::new();
        for (column, value) in &canonical {
            digest_input.extend_from_slice(format!("{}:", column.len()).as_bytes());
            digest_input.extend_from_slice(column);
            digest_input.extend_from_slice(format!("={}:", value.len()).as_bytes());
            digest_input.extend_from_slice(value);
            digest_input.push(b'\n');
        }
        let key = if identity.is_empty() {
            format!("hash:{}", crate::sha256_hex(&digest_input)).into_bytes()
        } else {
            [b"identity:".as_slice(), &identity].concat()
        };
        rows.push(Row {
            key,
            identity,
            values,
            source_path: String::new(),
            row_number: offset + 1,
        });
    }
    Ok((rows, schema))
}

pub fn project_rows(slug: &str, rows: &[Row]) -> Result<Vec<SidecarRow>, DatasetError> {
    let by_key = rows
        .iter()
        .map(|row| (&row.key, row))
        .collect::<BTreeMap<_, _>>();
    by_key
        .values()
        .map(|row| {
            let mut fields = Vec::new();
            for (key, value) in &row.values {
                let encoded = match value {
                    Value::Text(bytes) => escaped(bytes, true),
                    Value::Number(number) => super::go_json_float(*number)?,
                    Value::Boolean(value) => value.to_string(),
                };
                fields.push(format!("{}:{encoded}", escaped(key, true)));
            }
            Ok(SidecarRow {
                dataset_slug: slug.to_owned(),
                row_key: row.key.clone(),
                identity: row.identity.clone(),
                values_json: format!("{{{}}}", fields.join(",")),
                source_path: row.source_path.clone(),
                row_number: row.row_number,
            })
        })
        .collect()
}

pub fn coverage(rows: &[Row], schema: &Schema) -> crate::Coverage {
    let columns = schema
        .iter()
        .filter(|(_, property)| property.config.r#type == "date")
        .map(|(column, _)| column)
        .collect::<Vec<_>>();
    let mut dates = rows
        .iter()
        .filter_map(|row| {
            columns
                .iter()
                .find_map(|column| match row.values.get(*column) {
                    Some(Value::Text(bytes)) if !bytes.is_empty() => Some(text(bytes)),
                    _ => None,
                })
        })
        .collect::<Vec<_>>();
    dates.sort();
    match (dates.first(), dates.last()) {
        (Some(from), Some(to)) => crate::Coverage {
            from: from.clone(),
            to: to.clone(),
        },
        _ => crate::Coverage::default(),
    }
}

pub(super) fn yaml_scalar(bytes: &[u8], indent: usize, key: bool) -> Result<String, DatasetError> {
    match std::str::from_utf8(bytes) {
        Ok(value) => crate::mutations::render_go_yaml_string(value, indent, key)
            .map_err(|error| DatasetError::HandleRender(error.to_string())),
        Err(_) => {
            let encoded = BASE64.encode(bytes);
            if encoded.len() < 70 {
                Ok(format!("!!binary {encoded}"))
            } else {
                let chunks = encoded
                    .as_bytes()
                    .chunks(70)
                    .map(|chunk| {
                        format!(
                            "{}{}",
                            " ".repeat(indent),
                            std::str::from_utf8(chunk).expect("base64 is ASCII")
                        )
                    })
                    .collect::<Vec<_>>();
                Ok(format!("!!binary |\n{}", chunks.join("\n")))
            }
        }
    }
}

/// yaml.v3's string-key comparator groups nonletters before letters and uses
/// natural digit runs, independently of JSON's raw-byte key ordering.
pub(super) fn yaml_key_order(left: &[u8], right: &[u8]) -> std::cmp::Ordering {
    use unicode_general_category::{GeneralCategory as Category, get_general_category as category};
    fn digit(c: char) -> bool {
        category(c) == Category::DecimalNumber
    }
    fn letter(c: char) -> bool {
        matches!(
            category(c),
            Category::UppercaseLetter
                | Category::LowercaseLetter
                | Category::TitlecaseLetter
                | Category::ModifierLetter
                | Category::OtherLetter
        )
    }
    let left = text(left).chars().collect::<Vec<_>>();
    let right = text(right).chars().collect::<Vec<_>>();
    let mut digits = false;
    for index in 0..left.len().min(right.len()) {
        let (a, b) = (left[index], right[index]);
        if a == b {
            digits = digit(a);
            continue;
        }
        let (al, bl) = (letter(a), letter(b));
        if al && bl {
            return a.cmp(&b);
        }
        if al || bl {
            return if digits { bl.cmp(&al) } else { al.cmp(&bl) };
        }
        let mut initial = 0i64;
        if (a == '0' || b == '0')
            && left[..index]
                .iter()
                .rev()
                .take_while(|c| digit(**c))
                .any(|c| *c != '0')
        {
            initial = 1;
        }
        let run = |runes: &[char]| {
            let mut end = index;
            let mut number = initial;
            while end < runes.len() && digit(runes[end]) {
                number = number
                    .wrapping_mul(10)
                    .wrapping_add(i64::from(runes[end] as u32) - i64::from('0' as u32));
                end += 1;
            }
            (number, end)
        };
        let (an, ai) = run(&left);
        let (bn, bi) = run(&right);
        return an.cmp(&bn).then(ai.cmp(&bi)).then(a.cmp(&b));
    }
    left.len().cmp(&right.len())
}

#[derive(Default)]
struct YamlBytes(Vec<u8>);

impl<'de> serde::Deserialize<'de> for YamlBytes {
    fn deserialize<D: serde::Deserializer<'de>>(deserializer: D) -> Result<Self, D::Error> {
        struct Visitor;
        impl<'de> serde::de::Visitor<'de> for Visitor {
            type Value = YamlBytes;
            fn expecting(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
                formatter.write_str("a YAML string or binary scalar")
            }
            fn visit_bytes<E: serde::de::Error>(self, value: &[u8]) -> Result<Self::Value, E> {
                Ok(YamlBytes(value.to_vec()))
            }
            fn visit_byte_buf<E: serde::de::Error>(self, value: Vec<u8>) -> Result<Self::Value, E> {
                Ok(YamlBytes(value))
            }
        }
        deserializer.deserialize_byte_buf(Visitor)
    }
}

#[derive(Default, serde::Deserialize)]
struct YamlProperty {
    #[serde(default)]
    r#type: String,
    #[serde(default)]
    label: YamlBytes,
    #[serde(default)]
    options: Vec<String>,
    #[serde(default)]
    description: String,
    #[serde(default)]
    default: String,
}

pub(crate) fn deserialize_schema<'de, D: serde::Deserializer<'de>>(
    deserializer: D,
) -> Result<BTreeMap<String, crate::PropertyConfig>, D::Error> {
    struct Visitor;
    impl<'de> serde::de::Visitor<'de> for Visitor {
        type Value = BTreeMap<String, crate::PropertyConfig>;
        fn expecting(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
            formatter.write_str("a dataset schema map")
        }
        fn visit_map<A: serde::de::MapAccess<'de>>(
            self,
            mut map: A,
        ) -> Result<Self::Value, A::Error> {
            let mut schema = BTreeMap::new();
            while let Some((key, property)) = map.next_entry::<YamlBytes, YamlProperty>()? {
                schema.insert(
                    text(&key.0),
                    crate::PropertyConfig {
                        r#type: property.r#type,
                        label: text(&property.label.0),
                        options: property.options,
                        description: property.description,
                        default: property.default,
                    },
                );
            }
            Ok(schema)
        }
    }
    deserializer.deserialize_map(Visitor)
}
