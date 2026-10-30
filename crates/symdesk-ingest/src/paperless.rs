//! Paperless-ngx HTTP adapter. All contract tests use a scripted loopback
//! server; none read operator credentials or contact a deployed service.
use crate::Result;
use serde::{
    Deserialize, Serialize,
    de::{DeserializeOwned, Error as _},
};
use std::{
    collections::BTreeMap,
    io::{Read, Write},
    time::Duration,
};

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize)]
pub struct Ref {
    #[serde(rename = "ID")]
    pub id: i64,
    #[serde(rename = "Name")]
    pub name: String,
}
impl<'de> Deserialize<'de> for Ref {
    fn deserialize<D: serde::Deserializer<'de>>(d: D) -> std::result::Result<Self, D::Error> {
        let v = serde_json::Value::deserialize(d)?;
        if v.is_null() {
            return Ok(Self::default());
        }
        if let Some(id) = v.as_i64() {
            return Ok(Self {
                id,
                name: String::new(),
            });
        }
        #[derive(Deserialize, Default)]
        #[serde(default)]
        struct Object {
            id: i64,
            name: String,
        }
        let r: Object = serde_json::from_value(v).map_err(D::Error::custom)?;
        Ok(Self {
            id: r.id,
            name: r.name,
        })
    }
}
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize)]
pub struct FlexDate(pub Option<String>);
impl<'de> Deserialize<'de> for FlexDate {
    fn deserialize<D: serde::Deserializer<'de>>(d: D) -> std::result::Result<Self, D::Error> {
        let s = Option::<String>::deserialize(d)?.unwrap_or_default();
        if s.is_empty() {
            return Ok(Self(None));
        }
        if let Ok(stamp) =
            time::OffsetDateTime::parse(&s, &time::format_description::well_known::Rfc3339)
        {
            if stamp == time::macros::datetime!(0001-01-01 0:00 UTC) {
                return Ok(Self(None));
            }
            let stamp = stamp.replace_nanosecond(0).map_err(D::Error::custom)?;
            return Ok(Self(Some(
                stamp
                    .format(&time::format_description::well_known::Rfc3339)
                    .map_err(D::Error::custom)?,
            )));
        }
        let format = time::macros::format_description!("[year]-[month]-[day]");
        let date = time::Date::parse(&s, format).map_err(D::Error::custom)?;
        if date == time::macros::date!(0001 - 01 - 01) {
            return Ok(Self(None));
        }
        Ok(Self(Some(format!("{date}T00:00:00Z"))))
    }
}
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct Document {
    pub id: i64,
    pub title: String,
    pub content: String,
    pub created_date: FlexDate,
    pub created: FlexDate,
    pub added: FlexDate,
    pub modified: FlexDate,
    pub correspondent: Option<Ref>,
    pub tags: Option<Vec<Ref>>,
    pub document_type: Option<Ref>,
    pub storage_path: Option<Ref>,
    pub file_type: String,
    pub mime_type: String,
    pub original_file_name: String,
    pub archived_file_name: String,
    pub page_count: i64,
}
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct Entity {
    pub id: i64,
    pub name: String,
    pub slug: String,
}
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct DownloadMetadata {
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub content_type: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub content_disposition: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub filename: String,
}
#[derive(Deserialize)]
struct Page<T> {
    results: Option<Vec<T>>,
    next: Option<String>,
}

pub struct Client {
    base_url: String,
    token: String,
    agent: ureq::Agent,
}
impl Client {
    /// Explicit constructor. Environment fallback is separate so replay never
    /// consults credentials or deployment URLs from the operator environment.
    pub fn new(base_url: impl Into<String>, token: impl Into<String>) -> Self {
        let config = ureq::Agent::config_builder()
            .http_status_as_error(false)
            .accept(ureq::config::AutoHeaderValue::None)
            .timeout_connect(Some(Duration::from_secs(30)))
            .timeout_recv_response(Some(Duration::from_secs(30)))
            .build();
        Self {
            base_url: base_url.into(),
            token: token.into(),
            agent: config.into(),
        }
    }
    pub fn from_environment(base_url: &str, token: &str) -> Self {
        Self::new(
            if base_url.is_empty() {
                std::env::var("PAPERLESS_URL").unwrap_or_default()
            } else {
                base_url.into()
            },
            if token.is_empty() {
                std::env::var("PAPERLESS_TOKEN").unwrap_or_default()
            } else {
                token.into()
            },
        )
    }
    pub fn document_url(&self, id: i64) -> String {
        format!("{}/documents/{id}", self.base_url)
    }
    pub fn resolve_next_url(&self, next: &str) -> Result<String> {
        if next.is_empty() {
            return Ok(String::new());
        }
        let base = match url::Url::parse(&self.base_url) {
            Ok(b) => b,
            Err(_) => return Ok(next.into()),
        };
        let mut resolved = base
            .join(next)
            .map_err(|e| format!("parse next link {next:?}: {e}"))?;
        if resolved.host_str() == base.host_str() && resolved.port() != base.port() {
            resolved
                .set_port(base.port())
                .map_err(|()| "invalid next link port".to_owned())?;
        }
        Ok(resolved.into())
    }
    fn request(&self, url: &str, json: bool) -> Result<ureq::http::Response<ureq::Body>> {
        let request = self
            .agent
            .get(url)
            .header("Authorization", format!("Token {}", self.token))
            .header("User-Agent", "Go-http-client/1.1")
            .header("Accept-Encoding", "gzip");
        let request = if json {
            request
                .header("Accept", "application/json")
                .config()
                .timeout_global(Some(Duration::from_secs(30)))
                .build()
        } else {
            request
        };
        let mut response = request
            .call()
            .map_err(|e| format!("execute request: {e}"))?;
        if response.status().as_u16() != 200 {
            let status = response.status();
            let mut bytes = Vec::new();
            let _ = response
                .body_mut()
                .as_reader()
                .take(513)
                .read_to_end(&mut bytes);
            return Err(api_error(
                status.as_u16(),
                status.canonical_reason().unwrap_or_default(),
                &bytes,
            ));
        }
        Ok(response)
    }
    fn get_json<T: DeserializeOwned>(&self, url: &str) -> Result<T> {
        let mut response = self.request(url, true)?;
        let mut bytes = Vec::new();
        response
            .body_mut()
            .as_reader()
            .read_to_end(&mut bytes)
            .map_err(|e| format!("decode response: {e}"))?;
        // json.Decoder.Decode consumes one value, tolerating trailing JSON.
        let mut decoder = serde_json::Deserializer::from_slice(&bytes);
        T::deserialize(&mut decoder).map_err(|e| {
            format!(
                "decode response: {}",
                if e.is_eof() {
                    if bytes.iter().all(u8::is_ascii_whitespace) {
                        "EOF".into()
                    } else {
                        "unexpected EOF".into()
                    }
                } else {
                    e.to_string()
                }
            )
        })
    }
    pub fn get_document(&self, id: i64) -> Result<Document> {
        self.get_json(&format!(
            "{}/api/documents/{id}/?format=json",
            self.base_url
        ))
    }
    pub fn list_documents(
        &self,
        since: &str,
        max_results: usize,
        filters: &[String],
    ) -> Result<Option<Vec<Document>>> {
        let mut url = format!(
            "{}/api/documents/?format=json&ordering=-created_date",
            self.base_url
        );
        if !since.is_empty() {
            url.push_str(&format!("&created__date__gte={since}"));
        }
        if max_results > 0 {
            url.push_str(&format!("&page_size={max_results}"));
        }
        for filter in filters {
            url.push('&');
            url.push_str(filter);
        }
        self.list(&url, max_results)
    }
    fn list<T: DeserializeOwned>(&self, initial: &str, limit: usize) -> Result<Option<Vec<T>>> {
        let mut url = initial.to_owned();
        let mut all = Vec::new();
        while !url.is_empty() {
            let page: Page<T> = self.get_json(&url)?;
            all.extend(page.results.unwrap_or_default());
            if limit > 0 && all.len() >= limit {
                all.truncate(limit);
                break;
            }
            url = self.resolve_next_url(page.next.as_deref().unwrap_or_default())?;
        }
        Ok(if all.is_empty() { None } else { Some(all) })
    }
    pub fn list_tags(&self) -> Result<Option<Vec<Entity>>> {
        self.list_entities("tags")
    }
    pub fn list_correspondents(&self) -> Result<Option<Vec<Entity>>> {
        self.list_entities("correspondents")
    }
    pub fn list_document_types(&self) -> Result<Option<Vec<Entity>>> {
        self.list_entities("document_types")
    }
    pub fn list_storage_paths(&self) -> Result<Option<Vec<Entity>>> {
        self.list_entities("storage_paths")
    }
    fn list_entities(&self, kind: &str) -> Result<Option<Vec<Entity>>> {
        self.list(&format!("{}/api/{kind}/?format=json", self.base_url), 0)
    }
    pub fn download_document(&self, id: i64, dst: &mut impl Write) -> Result<DownloadMetadata> {
        let mut response = self.request(
            &format!("{}/api/documents/{id}/download/", self.base_url),
            false,
        )?;
        let header = |name: &str| {
            response
                .headers()
                .get(name)
                .and_then(|v| v.to_str().ok())
                .unwrap_or_default()
                .to_owned()
        };
        let content_type = header("content-type");
        let content_disposition = header("content-disposition");
        let filename = content_disposition_filename(&content_disposition);
        let meta = DownloadMetadata {
            content_type,
            content_disposition,
            filename,
        };
        std::io::copy(&mut response.body_mut().as_reader(), dst).map_err(|e| e.to_string())?;
        Ok(meta)
    }
}
fn api_error(code: u16, reason: &str, body: &[u8]) -> String {
    let truncated = body.len() > 512;
    let bytes = &body[..body.len().min(512)];
    let preview = String::from_utf8_lossy(bytes)
        .split_whitespace()
        .collect::<Vec<_>>()
        .join(" ");
    let preview = if truncated {
        format!("{}…", preview.trim())
    } else {
        preview
    };
    if preview.is_empty() {
        format!("API error {code} {reason}")
    } else {
        format!("API error {code} {reason}: {preview}")
    }
}
/// MIME parameter parser for filename and RFC 5987 extended filename. Invalid
/// quoted parameters fail closed, as mime.ParseMediaType does.
pub fn content_disposition_filename(value: &str) -> String {
    let Some(params) = mime_parameters(value) else {
        return String::new();
    };
    let name = if let Some(v) = params.get("filename*") {
        let mut parts = v.splitn(3, '\'');
        let charset = parts.next().unwrap_or_default();
        let _language = parts.next();
        let Some(encoded) = parts.next() else {
            return String::new();
        };
        if !charset.eq_ignore_ascii_case("utf-8") && !charset.eq_ignore_ascii_case("us-ascii") {
            return String::new();
        }
        let mut bytes = Vec::new();
        let mut chars = encoded.bytes();
        while let Some(b) = chars.next() {
            if b == b'%' {
                let (Some(a), Some(b)) = (chars.next(), chars.next()) else {
                    return String::new();
                };
                let Some(a) = (a as char).to_digit(16) else {
                    return String::new();
                };
                let Some(b) = (b as char).to_digit(16) else {
                    return String::new();
                };
                bytes.push((a * 16 + b) as u8);
            } else {
                bytes.push(b)
            }
        }
        String::from_utf8_lossy(&bytes).into_owned()
    } else {
        params.get("filename").cloned().unwrap_or_default()
    };
    let name = name.trim().replace('\\', "/");
    let name = name
        .trim_end_matches('/')
        .rsplit('/')
        .next()
        .unwrap_or_default();
    if name == "." {
        String::new()
    } else {
        name.into()
    }
}
fn mime_parameters(value: &str) -> Option<BTreeMap<String, String>> {
    let (_, tail) = value.split_once(';')?;
    let mut input = tail.trim();
    let mut out = BTreeMap::new();
    while !input.is_empty() {
        let (key, rest) = input.split_once('=')?;
        let key = key.trim().to_ascii_lowercase();
        if key.is_empty() {
            return None;
        }
        let rest = rest.trim_start();
        let value;
        if let Some(rest) = rest.strip_prefix('"') {
            let mut s = String::new();
            let mut chars = rest.char_indices();
            let mut end = None;
            while let Some((i, c)) = chars.next() {
                match c {
                    '"' => {
                        end = Some(i + 1);
                        break;
                    }
                    '\\' => s.push(chars.next()?.1),
                    _ => s.push(c),
                }
            }
            input = rest[end?..].trim_start();
            value = s;
            if !input.is_empty() {
                input = input.strip_prefix(';')?.trim_start();
            }
        } else {
            let (s, tail) = rest.split_once(';').unwrap_or((rest, ""));
            value = s.trim().into();
            input = tail.trim_start();
        }
        if let Some(previous) = out.insert(key, value.clone())
            && previous != value
        {
            return None;
        }
    }
    Some(out)
}
