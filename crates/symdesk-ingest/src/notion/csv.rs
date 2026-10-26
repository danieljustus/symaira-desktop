//! Strict encoding/csv reader: byte-positioned diagnostics and fixed record width.
pub(super) fn read_all(input: &str) -> Result<Vec<Vec<String>>, String> {
    let mut reader = Reader::new(input);
    let mut records: Vec<Vec<String>> = Vec::new();
    while let Some(record) = reader.read() {
        let (line, row) = record?;
        if let Some(header) = records.first()
            && row.len() != header.len()
        {
            return Err(format!("record on line {line}: wrong number of fields"));
        }
        records.push(row);
    }
    Ok(records)
}
struct Reader {
    lines: Vec<Vec<u8>>,
    next: usize,
}
impl Reader {
    fn new(input: &str) -> Self {
        let lines = input
            .as_bytes()
            .split_inclusive(|b| *b == b'\n')
            .map(|line| {
                let mut line = line.to_vec();
                if line.ends_with(b"\r\n") {
                    line.remove(line.len() - 2);
                } else if line.ends_with(b"\r") {
                    line.pop();
                }
                line
            })
            .collect();
        Self { lines, next: 0 }
    }
    fn read(&mut self) -> Option<Result<(usize, Vec<String>), String>> {
        while self
            .lines
            .get(self.next)
            .is_some_and(|l| l == b"\n" || l.is_empty())
        {
            self.next += 1;
        }
        let mut line = self.lines.get(self.next)?.clone();
        self.next += 1;
        let start = self.next;
        let mut line_num = start;
        let mut pos = 0;
        let mut record = Vec::new();
        let error = |line_num: usize, col: usize, msg: &str| {
            if line_num == start {
                format!("parse error on line {line_num}, column {col}: {msg}")
            } else {
                format!(
                    "record on line {start}; parse error on line {line_num}, column {col}: {msg}"
                )
            }
        };
        loop {
            let mut field = Vec::new();
            if line.get(pos) == Some(&b'"') {
                pos += 1;
                loop {
                    if pos == line.len() {
                        if let Some(next) = self.lines.get(self.next) {
                            line = next.clone();
                            self.next += 1;
                            line_num += 1;
                            pos = 0;
                            continue;
                        }
                        return Some(Err(error(
                            line_num,
                            pos + 1,
                            "extraneous or missing \" in quoted-field",
                        )));
                    }
                    match line[pos] {
                        b'"' => {
                            pos += 1;
                            match line.get(pos) {
                                Some(b'"') => {
                                    field.push(b'"');
                                    pos += 1;
                                }
                                Some(b',') => {
                                    pos += 1;
                                    record.push(String::from_utf8_lossy(&field).into_owned());
                                    break;
                                }
                                Some(b'\n') | None => {
                                    record.push(String::from_utf8_lossy(&field).into_owned());
                                    return Some(Ok((start, record)));
                                }
                                _ => {
                                    return Some(Err(error(
                                        line_num,
                                        pos,
                                        "extraneous or missing \" in quoted-field",
                                    )));
                                }
                            }
                        }
                        b => {
                            field.push(b);
                            pos += 1;
                        }
                    }
                }
            } else {
                while let Some(&b) = line.get(pos) {
                    if b == b',' || b == b'\n' {
                        break;
                    }
                    if b == b'"' {
                        return Some(Err(error(line_num, pos + 1, "bare \" in non-quoted-field")));
                    }
                    field.push(b);
                    pos += 1;
                }
                record.push(String::from_utf8_lossy(&field).into_owned());
                if line.get(pos) == Some(&b',') {
                    pos += 1;
                } else {
                    return Some(Ok((start, record)));
                }
            }
        }
    }
}
