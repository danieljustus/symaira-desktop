# Document format support

The Go format registry in `internal/documentformat` is the source of truth for
formats that both ingestion and folder retrieval can extract as text without an
optional runtime. Keep this table aligned with `SupportedFormats` when changing
the registry.

## Shared text-extraction contract

| Extension | Format |
| --- | --- |
| `.csv` | CSV |
| `.docx` | Office Open XML document |
| `.epub` | EPUB (XHTML chapters) |
| `.htm`, `.html` | HTML |
| `.markdown`, `.md` | Markdown |
| `.odp` | OpenDocument presentation |
| `.ods` | OpenDocument spreadsheet |
| `.odt` | OpenDocument text |
| `.pdf` | PDF |
| `.rtf` | Rich Text Format |
| `.text`, `.txt` | Plain text |
| `.pptx` | Office Open XML presentation |
| `.xlsx` | Office Open XML spreadsheet |

EPUB entries are ordered deterministically and `META-INF/rights.xml` or
`META-INF/encryption.xml` is reported as DRM-protected. No decryption is
attempted.

## Recognized but blocked

These extensions are recognized so the UI/job status can report an actionable
reason; they are not indexed or extracted:

- `.azw3`, `.mobi`: no bundled parser; DRM status cannot be determined without
  parsing the container.
- `.doc`, `.ppt`, `.xls`: no bundled legacy binary Office parser.
- `.djvu`: no bundled DjVu parser.
- `.key`, `.numbers`, `.pages`: no bundled iWork bundle parser.
- `.odg`: no bundled OpenDocument drawing parser.

The application must surface these failures rather than treating the files as
unknown text or silently dropping them. Adding support requires a real parser,
focused extraction tests, and an update to both this document and the shared
registry. DRM-protected documents remain unsupported by design.

Image OCR (`.png`, `.jpg`, `.jpeg`, `.tif`, `.tiff`, `.webp`, `.heic`) and EML
are ingestion-specific paths and are intentionally outside the shared text
retrieval registry.
