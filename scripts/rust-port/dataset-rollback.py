#!/usr/bin/env python3
"""Prove an older Go SymDesk can read and extend a Rust-written dataset."""

import argparse
import json
import subprocess
import tempfile
from pathlib import Path


def run(binary, vault, args, label):
    command = [str(binary), "--json", "--vault", str(vault), *args]
    result = subprocess.run(command, capture_output=True, text=True, check=False)
    if result.returncode:
        raise RuntimeError(
            f"{label} failed ({result.returncode})\n"
            f"stdout: {result.stdout.strip()}\nstderr: {result.stderr.strip()}"
        )
    return result.stdout


def dataset_identities(output):
    try:
        document = json.loads(output)
        rows = document["rows"]
        identities = {row["identity"] for row in rows}
    except (json.JSONDecodeError, KeyError, TypeError) as error:
        raise RuntimeError(f"dataset query returned an invalid result: {error}") from error
    if len(identities) != len(rows):
        raise RuntimeError("dataset query returned duplicate identities")
    return identities


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--rust-binary", required=True, type=Path)
    parser.add_argument("--go-binary", required=True, type=Path, help="historical Go fallback binary")
    parser.add_argument("--go-ref", default="v0.12.2", help="historical Go source revision represented by the binary")
    options = parser.parse_args()
    rust_binary = options.rust_binary.resolve(strict=True)
    go_binary = options.go_binary.resolve(strict=True)

    with tempfile.TemporaryDirectory(prefix="symdesk-dataset-rollback-") as temp:
        vault = Path(temp) / "vault"
        vault.mkdir()
        query = ["dataset", "query", "rollback", "--columns", "identity,event_id,amount"]

        run(rust_binary, vault, [
            "dataset", "sync", "rollback", "--identity-field", "event_id",
            "--rows", '[{"identity":"rust-seed","values":{"event_id":"rust-seed","amount":1}}]',
            "--provenance", '{"source_name":"rust-seed","source_sha256":"rust-seed-v1","imported_at":"2026-01-01T00:00:00Z"}',
        ], "Rust seed write")
        go_seed = dataset_identities(run(go_binary, vault, query, "historical Go reads Rust dataset"))
        if go_seed != {"rust-seed"}:
            raise RuntimeError(f"historical Go saw identities {sorted(go_seed)}, want ['rust-seed']")

        run(go_binary, vault, [
            "dataset", "sync", "rollback", "--identity-field", "event_id",
            "--rows", '[{"identity":"go-write","values":{"event_id":"go-write","amount":2}}]',
            "--source-name", "go-write", "--source-sha256", "go-write-v1",
            "--imported-at", "2026-01-02T00:00:00Z",
        ], "historical Go mutation")

        run(rust_binary, vault, [
            "dataset", "sync", "rollback", "--identity-field", "event_id",
            "--rows", '[{"identity":"rust-return","values":{"event_id":"rust-return","amount":3}}]',
            "--provenance", '{"source_name":"rust-return","source_sha256":"rust-return-v1","imported_at":"2026-01-03T00:00:00Z"}',
        ], "Rust reopen and mutation")
        got = dataset_identities(run(rust_binary, vault, query, "Rust reads mixed-version dataset"))
        want = {"rust-seed", "go-write", "rust-return"}
        if got != want:
            raise RuntimeError(f"Rust saw identities {sorted(got)}, want {sorted(want)}")

    print(f"PASS Rust → Go {options.go_ref} → Rust dataset handoff ({len(want)} rows)")


if __name__ == "__main__":
    main()
