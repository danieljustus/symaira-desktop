// time.Parse's date-only grammar and diagnostic, including byte quoting.
pub(super) fn parse_daily_date(raw: &str) -> Result<String, String> {
    fn quote(raw: &str) -> String {
        let mut out = String::from("\"");
        for b in raw.bytes() {
            match b {
                b'"' => out.push_str("\\\""),
                b'\\' => out.push_str("\\\\"),
                0x20..=0x7e => out.push(char::from(b)),
                _ => out.push_str(&format!("\\x{b:02x}")),
            }
        }
        out.push('"');
        out
    }
    let base = format!(
        "invalid date format, use YYYY-MM-DD: parsing time {}",
        quote(raw)
    );
    let mut rest = raw;
    let mut values = Vec::new();
    for (layout, n) in [("2006", 4), ("-", 1), ("01", 2), ("-", 1), ("02", 2)] {
        let valid = if layout == "-" {
            rest.starts_with('-')
        } else {
            rest.as_bytes()
                .get(..n)
                .is_some_and(|v| v.iter().all(u8::is_ascii_digit))
        };
        if !valid {
            return Err(format!(
                "{base} as \"2006-01-02\": cannot parse {} as {}",
                quote(rest),
                quote(layout)
            ));
        }
        if layout != "-" {
            values.push(rest[..n].parse::<i32>().unwrap());
        }
        rest = &rest[n..];
        if layout == "01" && !(1..=12).contains(&values[1]) {
            return Err(format!("{base}: month out of range"));
        }
    }
    if !rest.is_empty() {
        return Err(format!("{base}: extra text: {}", quote(rest)));
    }
    let month = time::Month::try_from(values[1] as u8)
        .map_err(|_| format!("{base}: month out of range"))?;
    let date = time::Date::from_calendar_date(values[0], month, values[2] as u8)
        .map_err(|_| format!("{base}: day out of range"))?;
    Ok(date.to_string())
}
