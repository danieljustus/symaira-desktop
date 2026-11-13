"""Negative controls for scripts/rust-port/unsafe-inventory.py (SEC-004).

Every test builds a throwaway git repository, so the real tree is never
touched. A green policy run on the real tree proves nothing unless these
controls show the gate rejects new unsafe, stale entries and missing safety
documentation.
"""
import importlib.util
import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

sys.dont_write_bytecode = True
SCRIPT = Path(__file__).resolve().parent / "unsafe-inventory.py"
spec = importlib.util.spec_from_file_location("unsafe_inventory", SCRIPT)
assert spec is not None and spec.loader is not None
inv = importlib.util.module_from_spec(spec)
spec.loader.exec_module(inv)

DENY = "#![deny(unsafe_code)]\n"
SAFE_SRC = DENY + "pub fn f() {}\n"
UNSAFE_SRC = (
    "#![deny(unsafe_code)]\n"
    "#[allow(unsafe_code)]\n"
    "pub fn g() {\n"
    "    // SAFETY: no preconditions.\n"
    "    unsafe { core::hint::unreachable_unchecked() }\n"
    "}\n"
)


def entry(path, count, scope="src", **kw):
    base = {"path": path, "count": count, "scope": scope,
            "invariant": "documented", "test": "crates/a/tests/t.rs"}
    base.update(kw)
    return base


class Repo:
    def __init__(self, files, entries, roots=()):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)
        files = {"crates/a/tests/t.rs": DENY, **files}
        for rel, text in files.items():
            p = self.root / rel
            p.parent.mkdir(parents=True, exist_ok=True)
            p.write_text(text, encoding="utf-8")
        subprocess.run(["git", "init", "-q", str(self.root)], check=True)
        subprocess.run(["git", "-C", str(self.root), "add", "-A"], check=True)
        self.allow = self.root / "allow.json"
        self.allow.write_text(json.dumps({"crate_roots_allowing_unsafe": list(roots), "unsafe": entries}))

    def errors(self):
        try:
            return inv.check(self.root, self.allow, report=False)
        finally:
            self.tmp.cleanup()


class UnsafeInventory(unittest.TestCase):
    def test_clean_tree_passes(self):
        self.assertEqual(Repo({"crates/a/src/lib.rs": SAFE_SRC}, []).errors(), [])

    def test_documented_exception_passes(self):
        r = Repo({"crates/a/src/lib.rs": UNSAFE_SRC}, [entry("crates/a/src/lib.rs", 1)])
        self.assertEqual(r.errors(), [])

    def test_new_unsafe_outside_allowlist_fails(self):
        errors = Repo({"crates/a/src/lib.rs": UNSAFE_SRC}, []).errors()
        self.assertTrue(any("outside the allowlist" in e for e in errors), errors)

    def test_count_growth_and_stale_entry_fail(self):
        grown = UNSAFE_SRC.replace("}\n", "    // SAFETY: x\n    unsafe { core::hint::unreachable_unchecked() }\n}\n", 1)
        errors = Repo({"crates/a/src/lib.rs": grown}, [entry("crates/a/src/lib.rs", 1)]).errors()
        self.assertTrue(any("allowlist says 1" in e for e in errors), errors)
        stale = Repo({"crates/a/src/lib.rs": SAFE_SRC}, [entry("crates/a/src/lib.rs", 1)]).errors()
        self.assertTrue(any("stale entry" in e for e in stale), stale)

    def test_missing_safety_comment_fails(self):
        src = UNSAFE_SRC.replace("    // SAFETY: no preconditions.\n", "")
        errors = Repo({"crates/a/src/lib.rs": src}, [entry("crates/a/src/lib.rs", 1)]).errors()
        self.assertTrue(any("SAFETY:" in e for e in errors), errors)

    def test_missing_invariant_or_test_file_fails(self):
        files = {"crates/a/src/lib.rs": UNSAFE_SRC}
        no_inv = Repo(files, [entry("crates/a/src/lib.rs", 1, invariant=" ")]).errors()
        self.assertTrue(any("no invariant" in e for e in no_inv), no_inv)
        no_test = Repo(files, [entry("crates/a/src/lib.rs", 1, test="crates/a/tests/missing.rs::x")]).errors()
        self.assertTrue(any("does not exist" in e for e in no_test), no_test)

    def test_crate_root_without_deny_fails(self):
        errors = Repo({"crates/a/src/lib.rs": "pub fn f() {}\n"}, []).errors()
        self.assertTrue(any("lacks #![deny" in e for e in errors), errors)
        listed = Repo({"crates/a/src/lib.rs": "pub fn f() {}\n"}, [], roots=["crates/a/src/lib.rs"]).errors()
        self.assertEqual(listed, [])

    def test_allow_attribute_without_entry_fails(self):
        src = DENY + "#[allow(unsafe_code)]\nmod m {}\n"
        errors = Repo({"crates/a/src/lib.rs": src}, []).errors()
        self.assertTrue(any("allow(unsafe_code)" in e for e in errors), errors)

    def test_prose_and_literals_are_not_counted(self):
        src = (
            DENY
            + '/// An unsafe path is rejected.\n'
            + '// unsafe { nope }\n'
            + '/* unsafe /* nested unsafe */ still comment */\n'
            + 'pub const A: &str = "unsafe block";\n'
            + 'pub const B: &str = r#"unsafe " inside"#;\n'
            + "pub const C: char = '\"';\n"
            + "pub fn id<'a>(x: &'a str) -> &'a str { x }\n"
            + "pub fn unsafe_run_id() {}\n"
        )
        tokens, _, _, _ = inv.scan(self._write(src))
        self.assertEqual(tokens, [])

    def test_real_unsafe_after_literals_is_counted_on_its_line(self):
        src = DENY + 'pub const A: &str = "x\ny";\npub unsafe fn f() {}\n'
        tokens, _, _, _ = inv.scan(self._write(src))
        self.assertEqual(tokens, [4])

    def _write(self, text):
        tmp = tempfile.NamedTemporaryFile("w", suffix=".rs", delete=False, encoding="utf-8")
        tmp.write(text)
        tmp.close()
        self.addCleanup(Path(tmp.name).unlink)
        return Path(tmp.name)


if __name__ == "__main__":
    unittest.main()
