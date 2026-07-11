package vault

import (
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"reflect"
	"sort"
)

// ASNValidationError reports an invalid archive serial number in a note.
// ASN is deliberately strict: YAML strings and floating-point values are not
// accepted even when they look numeric, so every writer agrees on one format.
type ASNValidationError struct {
	Path   string
	Reason string
}

func (e *ASNValidationError) Error() string {
	return fmt.Sprintf("invalid asn in %s: %s", e.Path, e.Reason)
}

// ValidateASN verifies the contract requirement for archive serial numbers.
func ValidateASN(asn int) error {
	if asn < 1 {
		return fmt.Errorf("must be a positive integer")
	}
	return nil
}

func asnFromFrontmatter(path string, frontmatter map[string]interface{}) (*int, error) {
	value, ok := frontmatter["asn"]
	if !ok {
		return nil, nil
	}

	if value == nil {
		return nil, &ASNValidationError{Path: path, Reason: "must be a positive integer"}
	}

	rv := reflect.ValueOf(value)
	var asn int
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n := rv.Int()
		if n > int64(math.MaxInt) || n < int64(math.MinInt) {
			return nil, &ASNValidationError{Path: path, Reason: "is outside the supported integer range"}
		}
		asn = int(n)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n := rv.Uint()
		if n > uint64(math.MaxInt) {
			return nil, &ASNValidationError{Path: path, Reason: "is outside the supported integer range"}
		}
		asn = int(n)
	default:
		return nil, &ASNValidationError{Path: path, Reason: "must be a positive integer"}
	}

	if err := ValidateASN(asn); err != nil {
		return nil, &ASNValidationError{Path: path, Reason: err.Error()}
	}
	return &asn, nil
}

// ASNDiagnostic identifies one note with an invalid ASN value.
type ASNDiagnostic struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

// ASNDuplicate identifies every note that claims the same ASN.
type ASNDuplicate struct {
	ASN   int      `json:"asn"`
	Paths []string `json:"paths"`
}

// ASNReport is a vault-wide snapshot of ASN assignments. Paths are relative to
// the vault root, so the report can be safely shown by the CLI and doctor.
type ASNReport struct {
	FilesScanned int             `json:"files_scanned"`
	Assigned     int             `json:"assigned"`
	Malformed    []ASNDiagnostic `json:"malformed"`
	Duplicates   []ASNDuplicate  `json:"duplicates"`
	ParseErrors  []ASNDiagnostic `json:"parse_errors"`

	assignments map[int][]string
}

// Healthy reports whether the vault complies with the ASN contract.
func (r ASNReport) Healthy() bool {
	return len(r.Malformed) == 0 && len(r.Duplicates) == 0 && len(r.ParseErrors) == 0
}

// AssignedPaths returns the relative paths currently claiming asn.
func (r ASNReport) AssignedPaths(asn int) []string {
	return append([]string(nil), r.assignments[asn]...)
}

// LowestFree returns the first positive ASN which is not assigned in this
// snapshot. The caller is responsible for holding WithASNLock while using it.
func (r ASNReport) LowestFree() int {
	for asn := 1; ; asn++ {
		if _, used := r.assignments[asn]; !used {
			return asn
		}
	}
}

// ScanASNs validates ASN frontmatter in every vault note and identifies
// duplicate assignments. It continues after malformed documents so doctor can
// present a complete diagnostic instead of only the first failure.
func ScanASNs(vaultRoot string) (ASNReport, error) {
	report := ASNReport{assignments: make(map[int][]string)}

	err := Walk(vaultRoot, func(path string) error {
		report.FilesScanned++
		relPath, err := filepath.Rel(vaultRoot, path)
		if err != nil {
			return fmt.Errorf("make ASN path relative: %w", err)
		}

		doc, err := ParseFile(path)
		if err != nil {
			var asnErr *ASNValidationError
			if errors.As(err, &asnErr) {
				report.Malformed = append(report.Malformed, ASNDiagnostic{Path: relPath, Message: asnErr.Reason})
			} else {
				report.ParseErrors = append(report.ParseErrors, ASNDiagnostic{Path: relPath, Message: err.Error()})
			}
			return nil
		}

		if doc.ASN != nil {
			report.Assigned++
			report.assignments[*doc.ASN] = append(report.assignments[*doc.ASN], relPath)
		}
		return nil
	})
	if err != nil {
		return report, err
	}

	for asn, paths := range report.assignments {
		if len(paths) > 1 {
			sort.Strings(paths)
			report.Duplicates = append(report.Duplicates, ASNDuplicate{ASN: asn, Paths: append([]string(nil), paths...)})
		}
	}
	sort.Slice(report.Duplicates, func(i, j int) bool { return report.Duplicates[i].ASN < report.Duplicates[j].ASN })
	sort.Slice(report.Malformed, func(i, j int) bool { return report.Malformed[i].Path < report.Malformed[j].Path })
	sort.Slice(report.ParseErrors, func(i, j int) bool { return report.ParseErrors[i].Path < report.ParseErrors[j].Path })

	return report, nil
}
