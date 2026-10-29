//! Archive/XML rules: archive order is significant except EPUB chapter names.
use super::retrieval_extract::{contract, html_text, normalize};
use crate::SidecarError;
use quick_xml::{Reader, events::Event};
use std::io::{Cursor, Read};
const ENTRY_LIMIT: usize = 10 << 20;
const BUDGET: usize = 100 << 20;

// Deliberate safety divergence: Go strings.Repeat accepts arbitrary text:c.
fn bounded_odf_spaces(requested: usize) -> usize {
    requested.min(BUDGET)
}

#[derive(Clone, Copy, PartialEq)]
enum Mode {
    Office,
    Odf,
    Shared,
    Sheet,
}
fn xml_text(bytes: &[u8], mode: Mode, shared: &[String]) -> (String, Vec<String>) {
    let mut reader = Reader::from_reader(bytes);
    reader.config_mut().expand_empty_elements = true;
    let (mut out, mut current, mut cell_type) = (String::new(), String::new(), String::new());
    let (mut active, mut in_block, mut row_has_cell) = (false, 0i32, false);
    let mut strings = Vec::new();
    loop {
        match reader.read_event() {
            Ok(Event::Start(e)) => {
                let name = e.local_name();
                match (mode, name.as_ref()) {
                    (Mode::Office | Mode::Shared, b"t") => active = true,
                    (Mode::Office, b"br" | b"cr") => out.push('\n'),
                    (Mode::Office | Mode::Odf, b"tab") => out.push('\t'),
                    (Mode::Odf, b"p" | b"h" | b"value") => in_block += 1,
                    (Mode::Odf, b"line-break") => out.push('\n'),
                    (Mode::Odf, b"s") => {
                        let n = e
                            .attributes()
                            .flatten()
                            .filter(|a| a.key.local_name().as_ref() == b"c")
                            .filter_map(|a| {
                                std::str::from_utf8(&a.value).ok()?.parse::<usize>().ok()
                            })
                            .filter(|n| *n > 0)
                            .last()
                            .unwrap_or(1);
                        // Untrusted repetition counts must not allocate unbounded memory.
                        out.extend(std::iter::repeat_n(' ', bounded_odf_spaces(n)));
                    }
                    (Mode::Sheet, b"c") => {
                        cell_type = e
                            .attributes()
                            .flatten()
                            .find(|a| a.key.local_name().as_ref() == b"t")
                            .map(|a| String::from_utf8_lossy(&a.value).into_owned())
                            .unwrap_or_default();
                    }
                    (Mode::Sheet, b"v") => {
                        active = true;
                        current.clear();
                    }
                    _ => {}
                }
            }
            Ok(Event::End(e)) => {
                match (mode, e.local_name().as_ref()) {
                    (Mode::Office | Mode::Shared, b"t") | (Mode::Sheet, b"v") => active = false,
                    (Mode::Office, b"p" | b"sp") => out.push_str("\n\n"),
                    (Mode::Odf, b"p" | b"h") => {
                        out.push_str("\n\n");
                        in_block -= 1;
                    }
                    (Mode::Odf, b"value") => in_block -= 1,
                    (Mode::Shared, b"si") => {
                        strings.push(std::mem::take(&mut current));
                    }
                    (Mode::Sheet, b"c") => {
                        let mut value = current.clone();
                        if cell_type == "s" {
                            // fmt.Sscanf accepts a decimal prefix followed by junk.
                            let s = value.trim_start();
                            let end = s
                                .char_indices()
                                .take_while(|(i, c)| {
                                    c.is_ascii_digit() || (*i == 0 && matches!(c, '+' | '-'))
                                })
                                .last()
                                .map(|(i, c)| i + c.len_utf8())
                                .unwrap_or(0);
                            if let Ok(index) = s[..end].parse::<usize>()
                                && let Some(s) = shared.get(index)
                            {
                                value.clone_from(s);
                            }
                        }
                        if !value.is_empty() {
                            if row_has_cell {
                                out.push('\t');
                            }
                            out.push_str(&value);
                            row_has_cell = true;
                        }
                    }
                    (Mode::Sheet, b"row") => {
                        out.push('\n');
                        row_has_cell = false;
                    }
                    _ => {}
                }
            }
            Ok(Event::Text(e)) => {
                let Ok(text) = e.xml11_content() else { break };
                if (mode == Mode::Odf && in_block > 0) || (mode == Mode::Office && active) {
                    out.push_str(&text);
                }
                if matches!(mode, Mode::Shared | Mode::Sheet) && active {
                    current.push_str(&text);
                }
            }
            Ok(Event::CData(e)) => {
                let Ok(text) = e.decode() else { break };
                if (mode == Mode::Odf && in_block > 0) || (mode == Mode::Office && active) {
                    out.push_str(&text);
                }
                if matches!(mode, Mode::Shared | Mode::Sheet) && active {
                    current.push_str(&text);
                }
            }
            Ok(Event::GeneralRef(e)) => {
                let Ok(name) = e.decode() else { break };
                let entity = format!("&{name};");
                let Ok(text) = quick_xml::escape::unescape(&entity) else {
                    break;
                };
                if (mode == Mode::Odf && in_block > 0) || (mode == Mode::Office && active) {
                    out.push_str(&text);
                }
                if matches!(mode, Mode::Shared | Mode::Sheet) && active {
                    current.push_str(&text);
                }
            }
            Ok(Event::Eof) | Err(_) => break,
            _ => {}
        }
    }
    (
        if mode == Mode::Sheet {
            out
        } else {
            normalize(&out)
        },
        strings,
    )
}

fn read_part(
    zip: &mut zip::ZipArchive<Cursor<&[u8]>>,
    index: usize,
    remaining: &mut usize,
) -> Result<Vec<u8>, SidecarError> {
    let mut file = zip.by_index(index).map_err(|e| contract(e.to_string()))?;
    let mut data = Vec::new();
    file.by_ref()
        .take((*remaining).min(ENTRY_LIMIT) as u64)
        .read_to_end(&mut data)
        .map_err(|e| contract(e.to_string()))?;
    *remaining -= data.len();
    Ok(data)
}
pub(super) fn archive_text(
    path: &str,
    extension: &str,
    bytes: &[u8],
) -> Result<Vec<u8>, SidecarError> {
    let family = match extension {
        ".epub" => "EPUB",
        ".xlsx" => "XLSX",
        ".pptx" => "PPTX",
        _ => "Office XML",
    };
    let mut zip = zip::ZipArchive::new(Cursor::new(bytes)).map_err(|_| {
        contract(format!(
            "failed to open {family}: zip: not a valid zip file"
        ))
    })?;
    if zip.len() > 10000 {
        return Err(contract(format!(
            "archive {path} has {} entries, exceeding the 10000 entry limit",
            zip.len()
        )));
    }
    let names = (0..zip.len())
        .map(|i| {
            zip.by_index(i)
                .and_then(|f| f.name().map(|name| name.into_owned()))
                .map_err(|e| contract(e.to_string()))
        })
        .collect::<Result<Vec<_>, _>>()?;
    let mut remaining = BUDGET;
    let exceeded = || {
        contract(format!(
            "archive {path} exceeds the {BUDGET} byte decompression budget"
        ))
    };
    if matches!(extension, ".docx" | ".odt" | ".ods" | ".odp") {
        let part = if extension == ".docx" {
            "word/document.xml"
        } else {
            "content.xml"
        };
        let i = names
            .iter()
            .position(|n| n == part)
            .ok_or_else(|| contract(format!("entry {part} not found in archive")))?;
        let data = read_part(&mut zip, i, &mut remaining)?;
        return Ok(xml_text(
            &data,
            if extension == ".docx" {
                Mode::Office
            } else {
                Mode::Odf
            },
            &[],
        )
        .0
        .into_bytes());
    }
    let mut shared = Vec::new();
    if extension == ".xlsx" {
        if let Some(i) = names.iter().position(|n| n == "xl/sharedStrings.xml")
            && let Ok(data) = read_part(&mut zip, i, &mut remaining)
        {
            shared = xml_text(&data, Mode::Shared, &[]).1;
        }
        if remaining == 0 {
            return Err(exceeded());
        }
    }
    if extension == ".epub"
        && names
            .iter()
            .any(|n| n == "META-INF/encryption.xml" || n == "META-INF/rights.xml")
    {
        return Err(contract(
            "document is DRM-protected: EPUB rights or encryption metadata is present",
        ));
    }
    let mut selected = names
        .iter()
        .enumerate()
        .filter(|(_, n)| match extension {
            ".xlsx" => n.starts_with("xl/worksheets/sheet") && n.ends_with(".xml"),
            ".pptx" => n.starts_with("ppt/slides/slide") && n.ends_with(".xml"),
            ".epub" => {
                !n.starts_with("META-INF/")
                    && !n.ends_with("toc.ncx")
                    && [".html", ".htm", ".xhtml"]
                        .iter()
                        .any(|ext| n.to_ascii_lowercase().ends_with(ext))
            }
            _ => false,
        })
        .map(|(i, n)| (i, n.clone()))
        .collect::<Vec<_>>();
    if extension == ".epub" {
        selected.sort_by(|a, b| a.1.cmp(&b.1));
    }
    let mut out = String::new();
    for (index, name) in selected {
        if remaining == 0 {
            return Err(exceeded());
        }
        let data = match read_part(&mut zip, index, &mut remaining) {
            Ok(data) => data,
            Err(e) if extension == ".epub" => return Err(e),
            Err(_) => continue,
        };
        if remaining == 0 {
            return Err(if extension == ".epub" {
                contract(format!(
                    "archive {name} exceeds the {BUDGET} byte decompression budget"
                ))
            } else {
                exceeded()
            });
        }
        let text = if extension == ".epub" {
            html_text(&data)
        } else {
            xml_text(
                &data,
                if extension == ".xlsx" {
                    Mode::Sheet
                } else {
                    Mode::Office
                },
                &shared,
            )
            .0
        };
        if !text.trim().is_empty() {
            if extension == ".epub" && !out.is_empty() {
                out.push_str("\n\n");
            }
            out.push_str(&text);
            if extension != ".epub" {
                out.push('\n');
            }
        }
    }
    let text = out.trim().to_owned();
    if text.is_empty() && extension != ".epub" {
        return Err(contract(format!("{family} contains no extractable text")));
    }
    Ok(text.into_bytes())
}

#[cfg(test)]
mod tests {
    #[test]
    fn retrieval_extract_odf_repetition_safety_divergence() {
        assert_eq!(super::bounded_odf_spaces(3), 3);
        assert_eq!(super::bounded_odf_spaces(super::BUDGET), super::BUDGET);
        assert_eq!(super::bounded_odf_spaces(usize::MAX), super::BUDGET);
    }
}
