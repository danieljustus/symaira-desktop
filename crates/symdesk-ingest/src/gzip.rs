//! Port of Go's `archive/zip` reader structure (Go 1.26): end-of-central-
//! directory search, zip64, base-offset repair for prefixed archives, central
//! directory walk, local-header resolution, data-descriptor and CRC handling.
//! Duplicate entry names are kept (Go's `Reader.File` is a plain list), which
//! the `zip` crate cannot represent. Inflate uses `miniz_oxide`; Go's
//! `flate: corrupt input before offset N` byte offsets are not reproduced
//! (documented limit).
use std::fmt;

pub const ERR_FORMAT: &str = "zip: not a valid zip file";
pub const ERR_ALGORITHM: &str = "zip: unsupported compression algorithm";
pub const ERR_CHECKSUM: &str = "zip: checksum error";
const EOF: &str = "EOF";
const UNEXPECTED_EOF: &str = "unexpected EOF";

const FILE_HEADER_SIG: u32 = 0x0403_4b50;
const DIR_HEADER_SIG: u32 = 0x0201_4b50;
const DIR64_LOC_SIG: u32 = 0x0706_4b50;
const DIR64_END_SIG: u32 = 0x0606_4b50;
const DATA_DESCRIPTOR_SIG: u32 = 0x0807_4b50;
const FILE_HEADER_LEN: usize = 30;
const DIR_HEADER_LEN: usize = 46;
const DIR_END_LEN: usize = 22;
const DIR64_LOC_LEN: usize = 20;
const DIR64_END_LEN: usize = 56;
const ZIP64_EXTRA_ID: u16 = 0x0001;

#[derive(Clone, Debug)]
pub struct Entry {
    pub name: String,
    pub flags: u16,
    pub method: u16,
    pub crc32: u32,
    pub csize: u64,
    pub usize_: u64,
    header_offset: i64,
}

pub struct Reader<'a> {
    data: &'a [u8],
    /// Entries kept in central-directory order (capped, see `count`).
    pub files: Vec<Entry>,
    /// Number of directory entries Go would have listed.
    pub count: usize,
}

#[derive(Debug)]
pub struct ZipError(pub String);

impl fmt::Display for ZipError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(&self.0)
    }
}

fn err<T>(m: &str) -> Result<T, ZipError> {
    Err(ZipError(m.to_string()))
}

fn u16le(b: &[u8], o: usize) -> u16 {
    u16::from_le_bytes([b[o], b[o + 1]])
}
fn u32le(b: &[u8], o: usize) -> u32 {
    u32::from_le_bytes(b[o..o + 4].try_into().unwrap())
}
fn u64le(b: &[u8], o: usize) -> u64 {
    u64::from_le_bytes(b[o..o + 8].try_into().unwrap())
}

/// `ReaderAt.ReadAt` over the in-memory archive: `Err("EOF")` when short.
fn read_at(data: &[u8], off: i64, n: usize) -> Result<&[u8], &'static str> {
    if off < 0 {
        return Err("readat $PATH: negative offset");
    }
    let off = off as usize;
    if off >= data.len() {
        return Err(EOF);
    }
    let end = off.saturating_add(n);
    if end > data.len() {
        return Err(EOF);
    }
    Ok(&data[off..end])
}

struct DirEnd {
    directory_records: u64,
    directory_size: u64,
    directory_offset: u64,
}

fn find_signature_in_block(b: &[u8]) -> Option<usize> {
    if b.len() < DIR_END_LEN {
        return None;
    }
    let mut i = b.len() - DIR_END_LEN;
    loop {
        if b[i] == b'P' && b[i + 1] == b'K' && b[i + 2] == 0x05 && b[i + 3] == 0x06 {
            let n = usize::from(b[i + DIR_END_LEN - 2]) | usize::from(b[i + DIR_END_LEN - 1]) << 8;
            if n + DIR_END_LEN + i > b.len() {
                return None;
            }
            return Some(i);
        }
        if i == 0 {
            return None;
        }
        i -= 1;
    }
}

fn read_directory_end(data: &[u8]) -> Result<(DirEnd, i64), ZipError> {
    let size = data.len() as i64;
    let mut buf: &[u8] = &[];
    let mut dir_end_offset = 0i64;
    for (i, mut blen) in [1024i64, 65 * 1024].into_iter().enumerate() {
        if blen > size {
            blen = size;
        }
        buf = &data[(size - blen) as usize..];
        if let Some(p) = find_signature_in_block(buf) {
            buf = &buf[p..];
            dir_end_offset = size - blen + p as i64;
            break;
        }
        if i == 1 || blen == size {
            return err(ERR_FORMAT);
        }
    }
    let mut d = DirEnd {
        directory_records: u64::from(u16le(buf, 10)),
        directory_size: u64::from(u32le(buf, 12)),
        directory_offset: u64::from(u32le(buf, 16)),
    };
    let comment_len = usize::from(u16le(buf, 20));
    if comment_len > buf.len() - DIR_END_LEN {
        return err("zip: invalid comment length");
    }
    if d.directory_records == 0xffff
        || d.directory_size == 0xffff
        || d.directory_offset == 0xffff_ffff
    {
        // findDirectory64End
        let loc_offset = dir_end_offset - DIR64_LOC_LEN as i64;
        let mut p: i64 = -1;
        if loc_offset >= 0 {
            let loc = read_at(data, loc_offset, DIR64_LOC_LEN).map_err(|e| ZipError(e.into()))?;
            if u32le(loc, 0) == DIR64_LOC_SIG && u32le(loc, 4) == 0 && u32le(loc, 16) == 1 {
                p = u64le(loc, 8) as i64;
            }
        }
        if p >= 0 {
            let rec = read_at(data, p, DIR64_END_LEN).map_err(|e| ZipError(e.into()))?;
            if u32le(rec, 0) != DIR64_END_SIG {
                return err(ERR_FORMAT);
            }
            d.directory_records = u64le(rec, 32);
            d.directory_size = u64le(rec, 40);
            d.directory_offset = u64le(rec, 48);
        }
    }
    let max_i64 = (1u64 << 63) - 1;
    if d.directory_size > max_i64 || d.directory_offset > max_i64 {
        return err(ERR_FORMAT);
    }
    let mut base = dir_end_offset
        .wrapping_sub(d.directory_size as i64)
        .wrapping_sub(d.directory_offset as i64);
    let o = base.wrapping_add(d.directory_offset as i64);
    if o < 0 || o >= size {
        return err(ERR_FORMAT);
    }
    if base > 0 {
        let off = d.directory_offset as i64;
        if off < size && read_directory_header(&data[off as usize..]).is_ok() {
            base = 0;
        }
    }
    Ok((d, base))
}

enum HeaderErr {
    Format,
    UnexpectedEof,
    Other(String),
}

type Header = (Entry, usize);

/// `readDirectoryHeader`: parses one header from `r` and returns its length.
fn read_directory_header_raw(r: &[u8]) -> Result<Header, HeaderErr> {
    if r.len() < DIR_HEADER_LEN {
        return Err(if r.is_empty() {
            HeaderErr::Other(EOF.into())
        } else {
            HeaderErr::UnexpectedEof
        });
    }
    if u32le(r, 0) != DIR_HEADER_SIG {
        return Err(HeaderErr::Format);
    }
    let flags = u16le(r, 8);
    let method = u16le(r, 10);
    let crc32 = u32le(r, 16);
    let csize32 = u32le(r, 20);
    let usize32 = u32le(r, 24);
    let name_len = usize::from(u16le(r, 28));
    let extra_len = usize::from(u16le(r, 30));
    let comment_len = usize::from(u16le(r, 32));
    let header_offset32 = u32le(r, 42);
    let total = name_len + extra_len + comment_len;
    if r.len() < DIR_HEADER_LEN + total {
        // io.ReadFull: io.EOF when no byte of the variable part is left.
        return Err(if r.len() == DIR_HEADER_LEN {
            HeaderErr::Other(EOF.into())
        } else {
            HeaderErr::UnexpectedEof
        });
    }
    let d = &r[DIR_HEADER_LEN..DIR_HEADER_LEN + total];
    let name = String::from_utf8_lossy(&d[..name_len]).into_owned();
    let extra = &d[name_len..name_len + extra_len];
    let mut e = Entry {
        name,
        flags,
        method,
        crc32,
        csize: u64::from(csize32),
        usize_: u64::from(usize32),
        header_offset: i64::from(header_offset32),
    };
    let mut need_u = usize32 == u32::MAX;
    let mut need_c = csize32 == u32::MAX;
    let mut need_h = header_offset32 == u32::MAX;
    let mut ex = extra;
    while ex.len() >= 4 {
        let tag = u16le(ex, 0);
        let size = usize::from(u16le(ex, 2));
        ex = &ex[4..];
        if ex.len() < size {
            break;
        }
        let mut field = &ex[..size];
        ex = &ex[size..];
        if tag == ZIP64_EXTRA_ID {
            if need_u {
                need_u = false;
                if field.len() < 8 {
                    return Err(HeaderErr::Format);
                }
                e.usize_ = u64le(field, 0);
                field = &field[8..];
            }
            if need_c {
                need_c = false;
                if field.len() < 8 {
                    return Err(HeaderErr::Format);
                }
                e.csize = u64le(field, 0);
                field = &field[8..];
            }
            if need_h {
                need_h = false;
                if field.len() < 8 {
                    return Err(HeaderErr::Format);
                }
                e.header_offset = u64le(field, 0) as i64;
            }
        }
    }
    let _ = need_u;
    if need_c || need_h {
        return Err(HeaderErr::Format);
    }
    Ok((e, DIR_HEADER_LEN + total))
}

fn read_directory_header(r: &[u8]) -> Result<Header, ()> {
    read_directory_header_raw(r).map_err(|_| ())
}

impl<'a> Reader<'a> {
    /// `zip.NewReader`; `max_keep` bounds how many entries are stored.
    pub fn new(data: &'a [u8], max_keep: usize) -> Result<Self, ZipError> {
        let (end, base) = read_directory_end(data)?;
        let start = base.wrapping_add(end.directory_offset as i64);
        let mut rest: &[u8] = if start >= 0 && (start as usize) < data.len() {
            &data[start as usize..]
        } else {
            &[]
        };
        let mut files = Vec::new();
        let mut count = 0usize;
        let last_err: Option<String>;
        loop {
            match read_directory_header_raw(rest) {
                Ok((mut e, n)) => {
                    e.header_offset = e.header_offset.wrapping_add(base);
                    rest = &rest[n..];
                    count += 1;
                    if files.len() < max_keep {
                        files.push(e);
                    }
                }
                Err(HeaderErr::Format) => {
                    last_err = Some(ERR_FORMAT.into());
                    break;
                }
                Err(HeaderErr::UnexpectedEof) => {
                    last_err = Some(UNEXPECTED_EOF.into());
                    break;
                }
                Err(HeaderErr::Other(m)) => return Err(ZipError(m)),
            }
        }
        if (count as u16) != (end.directory_records as u16) {
            return Err(ZipError(last_err.unwrap_or_default()));
        }
        Ok(Self { data, files, count })
    }

    fn find_body_offset(&self, e: &Entry) -> Result<i64, String> {
        let b = read_at(self.data, e.header_offset, FILE_HEADER_LEN).map_err(String::from)?;
        if u32le(b, 0) != FILE_HEADER_SIG {
            return Err(ERR_FORMAT.into());
        }
        let name_len = i64::from(u16le(b, 26));
        let extra_len = i64::from(u16le(b, 28));
        Ok(FILE_HEADER_LEN as i64 + name_len + extra_len)
    }

    /// `File.Open` + `io.ReadAll(io.LimitReader(rc, limit+1))`. Returns the
    /// bytes (at most `limit + 1`) or Go's error text (`open_err` tells which
    /// wrapper Go would use).
    pub fn read(&self, e: &Entry, limit: usize) -> Result<Vec<u8>, ReadError> {
        let body = self.find_body_offset(e).map_err(ReadError::Open)?;
        if e.name.ends_with('/') {
            return if e.usize_ != 0 {
                Err(ReadError::Read(ERR_FORMAT.into()))
            } else {
                Ok(Vec::new())
            };
        }
        if e.method != 0 && e.method != 8 {
            return Err(ReadError::Open(ERR_ALGORITHM.into()));
        }
        let start = e.header_offset.saturating_add(body);
        let csize = e.csize;
        // io.NewSectionReader(zipr, start, csize) bounded by the file.
        let avail = if start < 0 || start as usize >= self.data.len() {
            &[][..]
        } else {
            let s = start as usize;
            let end = (s as u64).saturating_add(csize).min(self.data.len() as u64) as usize;
            &self.data[s..end]
        };
        let mut hard_limit = limit.saturating_add(1);
        if hard_limit == 0 {
            hard_limit = usize::MAX;
        }
        let out: Vec<u8> = if e.method == 0 {
            let n = avail.len().min(hard_limit);
            avail[..n].to_vec()
        } else {
            match miniz_oxide::inflate::decompress_to_vec_with_limit(avail, hard_limit) {
                Ok(v) => v,
                Err(e2) => match e2.status {
                    miniz_oxide::inflate::TINFLStatus::HasMoreOutput => e2.output,
                    miniz_oxide::inflate::TINFLStatus::FailedCannotMakeProgress
                    | miniz_oxide::inflate::TINFLStatus::NeedsMoreInput => {
                        // truncated stream: Go reports io.ErrUnexpectedEOF
                        if e2.output.len() as u64 > e.usize_ {
                            return Err(ReadError::Read(ERR_FORMAT.into()));
                        }
                        return Err(ReadError::Read(UNEXPECTED_EOF.into()));
                    }
                    _ => {
                        if e2.output.len() as u64 > e.usize_ {
                            return Err(ReadError::Read(ERR_FORMAT.into()));
                        }
                        return Err(ReadError::Read("flate: corrupt input".into()));
                    }
                },
            }
        };
        let nread = out.len() as u64;
        if nread > e.usize_ {
            return Err(ReadError::Read(ERR_FORMAT.into()));
        }
        if out.len() >= hard_limit {
            // Reader stopped at the limit before reaching EOF: no checksum.
            return Ok(out);
        }
        if nread != e.usize_ {
            return Err(ReadError::Read(UNEXPECTED_EOF.into()));
        }
        let sum = crc32fast::hash(&out);
        if e.flags & 0x8 != 0 {
            let desc_off = start.saturating_add(csize as i64);
            let desc = match read_at(self.data, desc_off, 4) {
                Ok(d) => d,
                Err(_) => return Err(ReadError::Read(UNEXPECTED_EOF.into())),
            };
            let (crc_off, need) = if u32le(desc, 0) == DATA_DESCRIPTOR_SIG {
                (4i64, 8)
            } else {
                (0i64, 12)
            };
            // Go reads 16 bytes of descriptor space (sig + crc + sizes).
            let window = read_at(self.data, desc_off.saturating_add(crc_off), need);
            let Ok(w) = window else {
                return Err(ReadError::Read(UNEXPECTED_EOF.into()));
            };
            if u32le(w, 0) != e.crc32 {
                return Err(ReadError::Read(ERR_CHECKSUM.into()));
            }
            if sum != e.crc32 {
                return Err(ReadError::Read(ERR_CHECKSUM.into()));
            }
        } else if e.crc32 != 0 && sum != e.crc32 {
            return Err(ReadError::Read(ERR_CHECKSUM.into()));
        }
        Ok(out)
    }
}

pub enum ReadError {
    /// `open part %q: …`
    Open(String),
    /// `read part %q: …`
    Read(String),
}
