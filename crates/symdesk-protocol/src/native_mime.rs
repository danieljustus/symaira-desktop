//! Windows registry adapter for Go-compatible MIME extension loading.

#[cfg(target_os = "windows")]
pub(super) fn registry_entries() -> impl Iterator<Item = (String, String)> {
    winreg::HKCR
        .enum_keys()
        .filter_map(Result::ok)
        .filter(|name| super::mime::valid_registry_extension(name))
        .filter_map(|name| {
            let key = winreg::HKCR.open_subkey(&name).ok()?;
            let value: String = key.get_value("Content Type").ok()?;
            Some((name, value))
        })
}
