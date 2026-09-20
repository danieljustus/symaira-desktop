#![deny(unsafe_code)]

//! Conflict-copy convention parity.
//!
//! Every expected value for [`derive_original_path`] was produced by running the
//! Go oracle's own function (`cmd/symdesk/conflict.go:99` `deriveOriginalPath`)
//! inside `cmd/symdesk` and printing its result — the table is a captured
//! oracle transcript, not a hand-written expectation.

use symdesk_vault::conflict::{
    CONFLICT_COPY_SUFFIX, derive_original_path, is_sync_conflict_base_name,
};

#[test]
fn derive_original_path_matches_the_go_oracle() {
    let cases = [
        ("note.md", "note.md"),
        ("note 2.md", "note.md"),
        ("note conflicted copy.md", "note.md"),
        (" 2.md", ".md"),
        ("/abs/path/note 2.md", "/abs/path/note.md"),
        ("nested/note conflicted copy.md", "nested/note.md"),
        ("vault/note.md", "vault/note.md"),
        ("vault/note 2.md", "vault/note.md"),
        ("vault/note conflicted copy.md", "vault/note.md"),
        // Only ONE suffix is stripped, so the copy of a copy keeps its marker.
        (
            "vault/note conflicted copy 2.md",
            "vault/note conflicted copy.md",
        ),
        ("vault/a.b.md", "vault/a.b.md"),
        ("vault/nested/deep/note 2.md", "vault/nested/deep/note.md"),
        ("vault/noext 2", "vault/noext"),
        ("vault/.env", "vault/.env"),
        ("vault/dot.name conflicted copy.pdf", "vault/dot.name.pdf"),
        ("vault/trailing. 2.md", "vault/trailing..md"),
        // The marker must be a suffix of the name part, not just present.
        ("vault/already 2 copy.md", "vault/already 2 copy.md"),
    ];
    for (input, expected) in cases {
        assert_eq!(
            derive_original_path(input),
            expected,
            "Go oracle says {input:?} -> {expected:?}"
        );
    }
}

#[test]
fn derive_original_path_keeps_degenerate_inputs_unchanged() {
    // Go routes these through filepath.Dir/Base/Join cleaning: "" becomes "."
    // and "vault/" becomes "vault/vault". Neither can occur for a real conflict
    // file, so the port returns the input unchanged; see the module docs.
    assert_eq!(derive_original_path(""), "");
    assert_eq!(derive_original_path("vault/"), "vault/");
}

#[test]
fn doctor_recognition_matches_the_go_expression() {
    // cmd/symdesk/doctor.go:404
    //   strings.Contains(base, " 2.md") || strings.Contains(base, "conflicted copy")
    for flagged in [
        "note 2.md",
        "note conflicted copy.md",
        " 2.md",
        "noteconflicted copy.md",
    ] {
        assert!(
            is_sync_conflict_base_name(flagged),
            "{flagged} must be flagged"
        );
    }
    for clean in ["note.md", "note 2.txt", "note.md.bak", "note-copy.md"] {
        assert!(
            !is_sync_conflict_base_name(clean),
            "{clean} must not be flagged"
        );
    }
}

#[test]
fn conflict_copy_suffix_is_the_go_constant() {
    assert_eq!(CONFLICT_COPY_SUFFIX, " conflicted copy");
}
