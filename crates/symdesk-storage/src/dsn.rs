//! DSN handling: the pgx connection string (URL or keyword/value) reduced to
//! a `tokio_postgres::Config` plus the pgxpool `pool_max_conns` setting.

use std::str::FromStr;

use tokio_postgres::Config;

pub(crate) struct Parsed {
    pub config: Config,
    pub pool_max_conns: Option<usize>,
}

pub(crate) fn parse(dsn: &str) -> Result<Parsed, String> {
    let mut pool_max_conns = None;
    let rewritten = if dsn.starts_with("postgres://") || dsn.starts_with("postgresql://") {
        url_form(dsn, &mut pool_max_conns)?
    } else {
        keyword_form(dsn, &mut pool_max_conns)?
    };
    let config = Config::from_str(&rewritten).map_err(|error| error.to_string())?;
    Ok(Parsed {
        config,
        pool_max_conns,
    })
}

/// Applies the parameters this port understands and normalizes `sslmode` to
/// the three modes tokio-postgres knows. Without TLS support `verify-*` is
/// `require` (the connection then fails the same way Go's does against a
/// server that has no TLS), and `allow` is `prefer`.
fn param(
    key: &str,
    value: &str,
    pool_max_conns: &mut Option<usize>,
) -> Result<Option<String>, String> {
    if key == "pool_max_conns" {
        let parsed: i64 = value
            .parse()
            .map_err(|_| format!("cannot parse `pool_max_conns` as an integer: `{value}`"))?;
        if parsed < 1 {
            return Err("pool_max_conns must be >= 1".to_string());
        }
        *pool_max_conns = Some(parsed as usize);
        return Ok(None);
    }
    if key.starts_with("pool_") {
        return Err(format!("unsupported pool parameter `{key}`"));
    }
    if key == "sslmode" {
        let mapped = match value {
            "disable" => "disable",
            "allow" | "prefer" => "prefer",
            "require" | "verify-ca" | "verify-full" => "require",
            other => return Err(format!("sslmode is invalid: `{other}`")),
        };
        return Ok(Some(mapped.to_string()));
    }
    Ok(Some(value.to_string()))
}

fn url_form(dsn: &str, pool_max_conns: &mut Option<usize>) -> Result<String, String> {
    check_percent_encoding(dsn)?;
    let Some((base, query)) = dsn.split_once('?') else {
        return Ok(dsn.to_string());
    };
    let mut kept = Vec::new();
    for pair in query.split('&').filter(|pair| !pair.is_empty()) {
        let (key, value) = pair.split_once('=').unwrap_or((pair, ""));
        if let Some(value) = param(key, value, pool_max_conns)? {
            kept.push(format!("{key}={value}"));
        }
    }
    if kept.is_empty() {
        Ok(base.to_string())
    } else {
        Ok(format!("{base}?{}", kept.join("&")))
    }
}

fn check_percent_encoding(dsn: &str) -> Result<(), String> {
    let bytes = dsn.as_bytes();
    let mut i = 0;
    while i < bytes.len() {
        if bytes[i] == b'%' {
            let ok = bytes.len() >= i + 3
                && bytes[i + 1].is_ascii_hexdigit()
                && bytes[i + 2].is_ascii_hexdigit();
            if !ok {
                let end = (i + 3).min(bytes.len());
                return Err(format!(
                    "invalid percent-encoded token: {:?}",
                    String::from_utf8_lossy(&bytes[i..end])
                ));
            }
            i += 3;
        } else {
            i += 1;
        }
    }
    Ok(())
}

/// libpq keyword/value form: `key=value` pairs separated by whitespace,
/// values optionally single-quoted with backslash escapes.
fn keyword_form(dsn: &str, pool_max_conns: &mut Option<usize>) -> Result<String, String> {
    let mut out = Vec::new();
    let mut chars = dsn.chars().peekable();
    loop {
        while chars.peek().is_some_and(|c| c.is_whitespace()) {
            chars.next();
        }
        if chars.peek().is_none() {
            break;
        }
        let mut key = String::new();
        while let Some(&c) = chars.peek() {
            if c == '=' || c.is_whitespace() {
                break;
            }
            key.push(c);
            chars.next();
        }
        while chars.peek().is_some_and(|c| c.is_whitespace()) {
            chars.next();
        }
        if chars.next() != Some('=') || key.is_empty() {
            return Err("failed to parse as keyword/value (invalid keyword/value)".to_string());
        }
        while chars.peek().is_some_and(|c| c.is_whitespace()) {
            chars.next();
        }
        let mut value = String::new();
        if chars.peek() == Some(&'\'') {
            chars.next();
            loop {
                match chars.next() {
                    Some('\\') => match chars.next() {
                        Some(c) => value.push(c),
                        None => {
                            return Err(
                                "unterminated quoted string in connection info string".to_string()
                            );
                        }
                    },
                    Some('\'') => break,
                    Some(c) => value.push(c),
                    None => {
                        return Err(
                            "unterminated quoted string in connection info string".to_string()
                        );
                    }
                }
            }
        } else {
            while let Some(&c) = chars.peek() {
                if c.is_whitespace() {
                    break;
                }
                if c == '\\' {
                    chars.next();
                    if let Some(escaped) = chars.next() {
                        value.push(escaped);
                    }
                    continue;
                }
                value.push(c);
                chars.next();
            }
        }
        if let Some(value) = param(&key, &value, pool_max_conns)? {
            out.push(format!(
                "{key}='{}'",
                value.replace('\\', "\\\\").replace('\'', "\\'")
            ));
        }
    }
    Ok(out.join(" "))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn extracts_pool_max_conns_from_both_forms() {
        let url = parse("postgres://u:p@h:5432/db?sslmode=disable&pool_max_conns=3").unwrap();
        assert_eq!(url.pool_max_conns, Some(3));
        let kw = parse("host=h user=u dbname=db pool_max_conns=7 sslmode=disable").unwrap();
        assert_eq!(kw.pool_max_conns, Some(7));
        assert_eq!(parse("host=h").unwrap().pool_max_conns, None);
    }

    #[test]
    fn rejects_what_go_rejects_at_parse_time() {
        assert!(parse("this is not a dsn").is_err());
        assert!(parse("postgres://postgres@127.0.0.1:notaport/db").is_err());
        assert!(parse("postgres://%zz@127.0.0.1/db").is_err());
        assert!(parse("postgres://h/db?pool_max_conns=0").is_err());
        assert!(parse("postgres://h/db?pool_min_conns=1").is_err());
    }

    #[test]
    fn maps_sslmode_to_the_supported_modes() {
        for (mode, expected) in [
            ("disable", tokio_postgres::config::SslMode::Disable),
            ("allow", tokio_postgres::config::SslMode::Prefer),
            ("prefer", tokio_postgres::config::SslMode::Prefer),
            ("require", tokio_postgres::config::SslMode::Require),
            ("verify-ca", tokio_postgres::config::SslMode::Require),
            ("verify-full", tokio_postgres::config::SslMode::Require),
        ] {
            let parsed = parse(&format!("postgres://h/db?sslmode={mode}")).unwrap();
            assert_eq!(parsed.config.get_ssl_mode(), expected, "{mode}");
        }
        assert!(parse("postgres://h/db?sslmode=bogus").is_err());
    }

    #[test]
    fn keyword_values_may_be_quoted() {
        let parsed = parse("host=h password='a b\\'c' dbname=db").unwrap();
        assert_eq!(parsed.config.get_password(), Some(&b"a b'c"[..]));
    }
}
