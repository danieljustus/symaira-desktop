use symdesk_core::config::{self, Config};

// Reference-validated against BurntSushi/toml v1.6.0 through the pinned Go CLI.
// These are synthetic strings, not a native Windows execution claim.
#[test]
fn flat_toml_strings_match_go_without_changing_field_order_or_values() {
    let cases = [
        ("", r#""""#),
        (
            r"C:\a\_temp\workspace/first.db",
            r#""C:\\a\\_temp\\workspace/first.db""#,
        ),
        (
            r"\\server\share\index.db",
            r#""\\\\server\\share\\index.db""#,
        ),
        ("a\"b\\c", r#""a\"b\\c""#),
        ("a'b\\c", r#""a'b\\c""#),
        ("line\nnext\ttab\rreturn", r#""line\nnext\ttab\rreturn""#),
        (
            "control\0\x01\x08\x0c\x7f",
            r#""control\u0000\u0001\b\f\u007f""#,
        ),
        ("Unicodeé\u{2028}\u{2029}", "\"Unicodeé\u{2028}\u{2029}\""),
        (
            "\ninbox = 'not a field'\n",
            r#""\ninbox = 'not a field'\n""#,
        ),
    ];
    let template = config::render_toml(&Config::default()).expect("default config");
    let mut old_encoder_rejections = 0;
    for (input, quoted) in cases {
        let expected = template
            .replacen("vault = \"\"\n", &format!("vault = {quoted}\n"), 1)
            .replacen("inbox = \"\"\n", &format!("inbox = {quoted}\n"), 1)
            .replacen(
                "storage_path_template = \"\"\n",
                &format!("storage_path_template = {quoted}\n"),
                1,
            );
        let config: Config = toml::from_str(&expected).expect("reference-validated input");
        if toml::to_string(&config).expect("old encoder") != expected {
            old_encoder_rejections += 1;
        }
        let actual = config::render_toml(&config).expect("encode flat configuration");
        assert_eq!(actual, expected, "input: {input:?}");
        let decoded: Config = toml::from_str(&actual).expect("valid TOML");
        assert_eq!(decoded.vault, input);
        assert_eq!(decoded.inbox, input);
        assert_eq!(decoded.storage_path_template, input);
    }
    assert!(
        old_encoder_rejections > 0,
        "must reject the original encoder"
    );
}
