//! Strict decoder for the dataset tool arguments: Go `json.Decoder` with
//! `DisallowUnknownFields` over nested structs (`decodeDatasetInput`).
//!
//! Go keeps decoding after a type mismatch or an unknown field and reports the
//! first problem in document order. Field names match exactly first, then
//! ignoring case; a repeated key overwrites the earlier value. The result is a
//! normalized `serde_json::Value` (canonical keys, last value wins, every
//! declared field present with its zero value) that serde can read directly.
use serde_json::{Map, Number, Value};

use super::{go_json_type, go_raw_fields};

pub(super) enum Ty {
    Str,
    Int,
    /// `interface{}` / `map[string]interface{}` leaves: kept as parsed.
    Any,
    Strs,
    List(Box<Ty>, &'static str),
    Struct(fn() -> Shape),
    /// `*T` for a struct `T`.
    Ptr(fn() -> Shape),
    Map(Box<Ty>, &'static str),
}

pub(super) struct Shape {
    /// Go type name used in `Go struct field <Name>.<path>` (empty when anonymous).
    pub name: &'static str,
    /// Go type string as `reflect.Type.String()` prints it.
    pub qualified: &'static str,
    pub fields: Vec<(&'static str, Ty)>,
}

impl Ty {
    fn type_string(&self) -> String {
        match self {
            Self::Str => "string".into(),
            Self::Int => "int".into(),
            Self::Any => "interface {}".into(),
            Self::Strs => "[]string".into(),
            Self::List(_, name) | Self::Map(_, name) => (*name).into(),
            Self::Struct(shape) => shape().qualified.into(),
            Self::Ptr(shape) => format!("*{}", shape().qualified),
        }
    }

    fn zero(&self) -> Value {
        match self {
            Self::Str => Value::String(String::new()),
            Self::Int => Value::from(0),
            Self::Any | Self::Ptr(_) => Value::Null,
            Self::Strs | Self::List(..) => Value::Array(Vec::new()),
            Self::Map(..) => Value::Object(Map::new()),
            Self::Struct(shape) => shape().zero(),
        }
    }
}

impl Shape {
    fn zero(&self) -> Value {
        Value::Object(
            self.fields
                .iter()
                .map(|(key, ty)| ((*key).to_owned(), ty.zero()))
                .collect(),
        )
    }
}

struct Context {
    error: Option<String>,
    stack: Vec<&'static str>,
    struct_name: &'static str,
}

impl Context {
    fn fail(&mut self, message: String) {
        self.error.get_or_insert(message);
    }

    fn mismatch(&mut self, value: &str, target: &str) {
        let field = self.stack.join(".");
        self.fail(format!(
            "json: cannot unmarshal {value} into Go struct field {}.{field} of type {target}",
            self.struct_name
        ));
    }
}

/// Decodes `raw` (`None` = absent arguments) into the shape of `shape`.
pub(super) fn decode(raw: Option<&str>, shape: &Shape) -> Result<Value, String> {
    let raw = raw.ok_or_else(|| "EOF".to_owned())?;
    if raw == "null" {
        return Ok(shape.zero());
    }
    if !raw.starts_with('{') {
        return Err(format!(
            "json: cannot unmarshal {} into Go value of type {}",
            go_json_type(raw),
            shape.qualified
        ));
    }
    let mut context = Context {
        error: None,
        stack: Vec::new(),
        struct_name: "",
    };
    let value = decode_struct(raw, shape, &mut context);
    match context.error {
        Some(error) => Err(error),
        None => Ok(value),
    }
}

fn decode_struct(raw: &str, shape: &Shape, context: &mut Context) -> Value {
    let mut out = match shape.zero() {
        Value::Object(map) => map,
        _ => Map::new(),
    };
    let fields = match go_raw_fields(raw) {
        Ok(fields) => fields.0,
        Err(error) => {
            context.fail(error);
            return Value::Object(out);
        }
    };
    let outer = context.struct_name;
    for (key, token) in fields {
        let index = shape
            .fields
            .iter()
            .position(|(name, _)| *name == key)
            .or_else(|| {
                shape
                    .fields
                    .iter()
                    .position(|(name, _)| symdesk_vault::dataset::go_equal_fold(name, &key))
            });
        let Some(index) = index else {
            context.fail(format!(
                "json: unknown field {}",
                symdesk_vault::go_quote(&key)
            ));
            continue;
        };
        let (name, ty) = &shape.fields[index];
        context.stack.push(name);
        context.struct_name = shape.name;
        let value = decode_value(token.get(), ty, context);
        context.stack.pop();
        context.struct_name = outer;
        if let Some(value) = value {
            out.insert((*name).to_owned(), value);
        }
    }
    Value::Object(out)
}

/// `None` leaves the field unchanged (JSON `null`).
fn decode_value(token: &str, ty: &Ty, context: &mut Context) -> Option<Value> {
    if token == "null" {
        return None;
    }
    let kind = go_json_type(token);
    match ty {
        Ty::Str => {
            if kind != "string" {
                context.mismatch(kind, "string");
                return None;
            }
            serde_json::from_str(token).ok()
        }
        Ty::Int => {
            if kind != "number" {
                context.mismatch(kind, "int");
                return None;
            }
            match token.parse::<i64>() {
                Ok(number) => Some(Value::Number(Number::from(number))),
                Err(_) => {
                    context.mismatch(&format!("number {token}"), "int");
                    None
                }
            }
        }
        Ty::Any => serde_json::from_str(token).ok(),
        Ty::Strs => {
            if kind != "array" {
                context.mismatch(kind, "[]string");
                return None;
            }
            let items: Vec<Box<serde_json::value::RawValue>> = serde_json::from_str(token).ok()?;
            let mut out = Vec::new();
            for item in &items {
                let item = item.get();
                if item == "null" {
                    out.push(Value::String(String::new()));
                } else if go_json_type(item) == "string" {
                    out.push(serde_json::from_str(item).unwrap_or_default());
                } else {
                    context.mismatch(go_json_type(item), "string");
                }
            }
            Some(Value::Array(out))
        }
        Ty::List(element, name) => {
            if kind != "array" {
                context.mismatch(kind, name);
                return None;
            }
            let items: Vec<Box<serde_json::value::RawValue>> = serde_json::from_str(token).ok()?;
            let mut out = Vec::new();
            for item in &items {
                out.push(
                    decode_value(item.get(), element, context).unwrap_or_else(|| element.zero()),
                );
            }
            Some(Value::Array(out))
        }
        Ty::Struct(shape) | Ty::Ptr(shape) => {
            if kind != "object" {
                context.mismatch(kind, &ty.type_string());
                return None;
            }
            Some(decode_struct(token, &shape(), context))
        }
        Ty::Map(element, name) => {
            if kind != "object" {
                context.mismatch(kind, name);
                return None;
            }
            let fields = match go_raw_fields(token) {
                Ok(fields) => fields.0,
                Err(error) => {
                    context.fail(error);
                    return None;
                }
            };
            let mut out = Map::new();
            for (key, item) in fields {
                let value =
                    decode_value(item.get(), element, context).unwrap_or_else(|| element.zero());
                out.insert(key, value);
            }
            Some(Value::Object(out))
        }
    }
}

// ---- shapes of the dataset tools --------------------------------------------

fn filter_shape() -> Shape {
    Shape {
        name: "Filter",
        qualified: "dbviews.Filter",
        fields: vec![("key", Ty::Str), ("operator", Ty::Str), ("value", Ty::Str)],
    }
}

fn filter_group_shape() -> Shape {
    Shape {
        name: "FilterGroup",
        qualified: "dbviews.FilterGroup",
        fields: vec![
            ("operator", Ty::Str),
            (
                "filters",
                Ty::List(Box::new(Ty::Struct(filter_shape)), "[]dbviews.Filter"),
            ),
            (
                "groups",
                Ty::List(
                    Box::new(Ty::Struct(filter_group_shape)),
                    "[]dbviews.FilterGroup",
                ),
            ),
        ],
    }
}

fn aggregate_shape() -> Shape {
    Shape {
        name: "DatasetAggregate",
        qualified: "service.DatasetAggregate",
        fields: vec![("column", Ty::Str), ("function", Ty::Str), ("as", Ty::Str)],
    }
}

fn property_shape() -> Shape {
    Shape {
        name: "PropertyConfig",
        qualified: "dbviews.PropertyConfig",
        fields: vec![
            ("type", Ty::Str),
            ("label", Ty::Str),
            ("options", Ty::Strs),
            ("description", Ty::Str),
            ("default", Ty::Str),
        ],
    }
}

fn provenance_shape() -> Shape {
    Shape {
        name: "datasetProvenance",
        qualified: "tools.datasetProvenance",
        fields: vec![
            ("imported_at", Ty::Str),
            ("source_name", Ty::Str),
            ("source_sha256", Ty::Str),
        ],
    }
}

fn row_shape() -> Shape {
    Shape {
        name: "DatasetSyncRow",
        qualified: "service.DatasetSyncRow",
        fields: vec![
            ("identity", Ty::Str),
            (
                "values",
                Ty::Map(Box::new(Ty::Any), "map[string]interface {}"),
            ),
        ],
    }
}

pub(super) fn describe_shape() -> Shape {
    Shape {
        name: "",
        qualified: r#"struct { Dataset string "json:\"dataset\"" }"#,
        fields: vec![("dataset", Ty::Str)],
    }
}

pub(super) fn query_shape() -> Shape {
    Shape {
        name: "",
        qualified: r#"struct { Dataset string "json:\"dataset\""; Columns []string "json:\"columns\""; Filters []dbviews.Filter "json:\"filters\""; FilterGroup *dbviews.FilterGroup "json:\"filter_group\""; GroupBy string "json:\"group_by\""; Aggregates []service.DatasetAggregate "json:\"aggregates\""; Limit int "json:\"limit\"" }"#,
        fields: vec![
            ("dataset", Ty::Str),
            ("columns", Ty::Strs),
            (
                "filters",
                Ty::List(Box::new(Ty::Struct(filter_shape)), "[]dbviews.Filter"),
            ),
            ("filter_group", Ty::Ptr(filter_group_shape)),
            ("group_by", Ty::Str),
            (
                "aggregates",
                Ty::List(
                    Box::new(Ty::Struct(aggregate_shape)),
                    "[]service.DatasetAggregate",
                ),
            ),
            ("limit", Ty::Int),
        ],
    }
}

pub(super) fn sync_shape() -> Shape {
    Shape {
        name: "",
        qualified: r#"struct { Dataset string "json:\"dataset\""; Title string "json:\"title\""; IdentityField string "json:\"identity_field\""; Sensitivity string "json:\"sensitivity\""; RetentionRule string "json:\"retention_rule\""; Schema map[string]dbviews.PropertyConfig "json:\"schema\""; Provenance tools.datasetProvenance "json:\"provenance\""; Rows []service.DatasetSyncRow "json:\"rows\"" }"#,
        fields: vec![
            ("dataset", Ty::Str),
            ("title", Ty::Str),
            ("identity_field", Ty::Str),
            ("sensitivity", Ty::Str),
            ("retention_rule", Ty::Str),
            (
                "schema",
                Ty::Map(
                    Box::new(Ty::Struct(property_shape)),
                    "map[string]dbviews.PropertyConfig",
                ),
            ),
            ("provenance", Ty::Struct(provenance_shape)),
            (
                "rows",
                Ty::List(Box::new(Ty::Struct(row_shape)), "[]service.DatasetSyncRow"),
            ),
        ],
    }
}
