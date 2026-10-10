package main

import "github.com/danieljustus/symaira-desktop/scripts/rust-port/inventory"

// Keep ordinary ancestry authoritative. The only additional path is a recorded,
// checksum-bound source bundle with an ancestral anchor and exact source bytes.
func verifyOracleSourceAt(repoRoot, head, oracleCommit string) error {
	return inventory.VerifyOracleSourceAt(repoRoot, head, oracleCommit)
}

func verifyOracleSource(repoRoot, oracleCommit string) error {
	head, err := resolveGitCommit(repoRoot, "HEAD")
	if err != nil {
		return err
	}
	return verifyOracleSourceAt(repoRoot, head, oracleCommit)
}
