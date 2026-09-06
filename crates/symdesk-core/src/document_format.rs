#![deny(unsafe_code)]

//! Shared supported/unsupported document-format registry.

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub struct Format {
    pub extension: &'static str,
    pub kind: &'static str,
    pub name: &'static str,
}

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub struct UnsupportedFormat {
    pub format: Format,
    pub reason: &'static str,
}

pub const SUPPORTED_FORMATS: &[Format] = &[
    Format {
        extension: ".pdf",
        kind: "application/pdf",
        name: "PDF",
    },
    Format {
        extension: ".txt",
        kind: "text/plain",
        name: "plain text",
    },
    Format {
        extension: ".text",
        kind: "text/plain",
        name: "plain text",
    },
    Format {
        extension: ".csv",
        kind: "text/csv",
        name: "CSV",
    },
    Format {
        extension: ".md",
        kind: "text/markdown",
        name: "Markdown",
    },
    Format {
        extension: ".markdown",
        kind: "text/markdown",
        name: "Markdown",
    },
    Format {
        extension: ".html",
        kind: "text/html",
        name: "HTML",
    },
    Format {
        extension: ".htm",
        kind: "text/html",
        name: "HTML",
    },
    Format {
        extension: ".rtf",
        kind: "application/rtf",
        name: "RTF",
    },
    Format {
        extension: ".docx",
        kind: "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
        name: "Office Open XML document",
    },
    Format {
        extension: ".xlsx",
        kind: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
        name: "Office Open XML spreadsheet",
    },
    Format {
        extension: ".pptx",
        kind: "application/vnd.openxmlformats-officedocument.presentationml.presentation",
        name: "Office Open XML presentation",
    },
    Format {
        extension: ".odt",
        kind: "application/vnd.oasis.opendocument.text",
        name: "OpenDocument text",
    },
    Format {
        extension: ".ods",
        kind: "application/vnd.oasis.opendocument.spreadsheet",
        name: "OpenDocument spreadsheet",
    },
    Format {
        extension: ".odp",
        kind: "application/vnd.oasis.opendocument.presentation",
        name: "OpenDocument presentation",
    },
    Format {
        extension: ".epub",
        kind: "application/epub+zip",
        name: "EPUB",
    },
];

pub const UNSUPPORTED_FORMATS: &[UnsupportedFormat] = &[
    make_unsupported(
        ".mobi",
        "application/x-mobipocket-ebook",
        "MOBI",
        "no bundled MOBI parser; DRM status cannot be determined",
    ),
    make_unsupported(
        ".azw3",
        "application/vnd.amazon.mobi8-ebook",
        "AZW3",
        "no bundled AZW3 parser; DRM status cannot be determined",
    ),
    make_unsupported(
        ".pages",
        "application/vnd.apple.pages",
        "iWork Pages",
        "iWork bundle parser is not available",
    ),
    make_unsupported(
        ".key",
        "application/vnd.apple.keynote",
        "iWork Keynote",
        "iWork bundle parser is not available",
    ),
    make_unsupported(
        ".numbers",
        "application/vnd.apple.numbers",
        "iWork Numbers",
        "iWork bundle parser is not available",
    ),
    make_unsupported(
        ".doc",
        "application/msword",
        "legacy Word",
        "legacy binary Office parser is not available",
    ),
    make_unsupported(
        ".xls",
        "application/vnd.ms-excel",
        "legacy Excel",
        "legacy binary Office parser is not available",
    ),
    make_unsupported(
        ".ppt",
        "application/vnd.ms-powerpoint",
        "legacy PowerPoint",
        "legacy binary Office parser is not available",
    ),
    make_unsupported(
        ".djvu",
        "image/vnd.djvu",
        "DjVu",
        "DjVu parser is not available",
    ),
    make_unsupported(
        ".odg",
        "application/vnd.oasis.opendocument.graphics",
        "OpenDocument drawing",
        "OpenDocument drawing parser is not available",
    ),
];

pub const DRM_PROTECTED_ERROR: &str = "document is DRM-protected";

const fn make_unsupported(
    extension: &'static str,
    kind: &'static str,
    name: &'static str,
    reason: &'static str,
) -> UnsupportedFormat {
    UnsupportedFormat {
        format: Format {
            extension,
            kind,
            name,
        },
        reason,
    }
}

#[must_use]
pub fn normalize_extension(extension: &str) -> String {
    let normalized = extension.trim().to_lowercase();
    if normalized.is_empty() || normalized.starts_with('.') {
        normalized
    } else {
        format!(".{normalized}")
    }
}

#[must_use]
pub fn lookup(extension: &str) -> Option<Format> {
    let normalized = normalize_extension(extension);
    SUPPORTED_FORMATS
        .iter()
        .copied()
        .find(|format| format.extension == normalized)
}

#[must_use]
pub fn unsupported(extension: &str) -> Option<UnsupportedFormat> {
    let normalized = normalize_extension(extension);
    UNSUPPORTED_FORMATS
        .iter()
        .copied()
        .find(|format| format.format.extension == normalized)
}

#[must_use]
pub fn kind_for_extension(extension: &str) -> Option<&'static str> {
    lookup(extension)
        .map(|format| format.kind)
        .or_else(|| unsupported(extension).map(|format| format.format.kind))
}

#[must_use]
pub fn is_supported(extension: &str) -> bool {
    lookup(extension).is_some()
}

#[must_use]
pub fn supported_extensions() -> Vec<&'static str> {
    let mut result: Vec<_> = SUPPORTED_FORMATS
        .iter()
        .map(|format| format.extension)
        .collect();
    result.sort_unstable();
    result
}

#[must_use]
pub fn unsupported_format_error(kind: &str) -> String {
    if let Some(format) = UNSUPPORTED_FORMATS
        .iter()
        .find(|format| format.format.kind == kind)
    {
        format!(
            "unsupported document format {}: {}",
            format.format.name, format.reason
        )
    } else {
        format!("unsupported document format {kind:?}")
    }
}
