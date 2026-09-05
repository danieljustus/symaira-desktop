package service

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/danieljustus/symaira-desktop/internal/dataset"
	"github.com/danieljustus/symaira-desktop/internal/dbviews"
)

func datasetPolicy(sensitivity, retentionRule string) (string, string, error) {
	normalized, err := dataset.NormalizeSensitivity(sensitivity)
	if err != nil {
		return "", "", err
	}
	retentionRule = strings.TrimSpace(retentionRule)
	if retentionRule == "" {
		retentionRule = dataset.DefaultRetentionRule
	}
	if err := dataset.ValidateRetentionRuleReference(retentionRule); err != nil {
		return "", "", err
	}
	return normalized, retentionRule, nil
}

func datasetSensitivityRank(value string) int {
	switch value {
	case dataset.SensitivityPublic:
		return 0
	case dataset.SensitivityInternal:
		return 1
	case dataset.SensitivityConfidential:
		return 2
	case dataset.SensitivityRestricted:
		return 3
	default:
		return -1
	}
}

// checkDatasetExportSlug validates a dataset handle before any rendered bytes
// are produced. A missing or malformed handle is always a policy failure: the
// sidecar rows are derived data and must not be exported without authoritative
// handle metadata.
func (s *Service) checkDatasetExportSlug(slug string) error {
	slug = strings.TrimSpace(slug)
	if slug == "" {
		return fmt.Errorf("dataset export requires a dataset slug")
	}
	if slug != dbviews.Slugify(slug) {
		return fmt.Errorf("dataset export policy: dataset slug %q is not filesystem-safe", slug)
	}
	handle, err := readDatasetHandle(s.VaultRoot, "datasets/"+slug+".md")
	if err != nil {
		return fmt.Errorf("dataset export policy: %w", err)
	}
	if handle.Slug != slug {
		return fmt.Errorf("dataset export policy: dataset handle slug %q does not match path slug %q", handle.Slug, slug)
	}
	max := dataset.SensitivityInternal
	if s.Config != nil && strings.TrimSpace(s.Config.DatasetExportMaxSensitivity) != "" {
		max = strings.ToLower(strings.TrimSpace(s.Config.DatasetExportMaxSensitivity))
	}
	if err := dataset.ValidateSensitivity(max); err != nil {
		return fmt.Errorf("dataset export policy: %w", err)
	}
	if datasetSensitivityRank(handle.Sensitivity) > datasetSensitivityRank(max) {
		return fmt.Errorf("dataset export denied: dataset %q is %s; maximum configured export sensitivity is %s (set dataset_export_max_sensitivity explicitly to opt in)", slug, handle.Sensitivity, max)
	}
	return nil
}

// checkDatasetExportPath covers both dataset handles and raw dataset files when
// a note-style export accepts a vault-relative path. Dataset paths are rooted
// at datasets/<slug>; the handle beside the raw directory is authoritative.
func (s *Service) checkDatasetExportPath(relPath string) error {
	normalized := strings.TrimPrefix(filepath.ToSlash(strings.TrimSpace(relPath)), "./")
	if !strings.HasPrefix(normalized, "datasets/") {
		return nil
	}
	tail := strings.TrimPrefix(normalized, "datasets/")
	parts := strings.Split(tail, "/")
	if len(parts) == 0 || strings.TrimSpace(parts[0]) == "" {
		return fmt.Errorf("dataset export policy: dataset path requires a dataset handle")
	}
	slug := parts[0]
	if len(parts) == 1 {
		slug = strings.TrimSuffix(slug, ".md")
	}
	return s.checkDatasetExportSlug(slug)
}

func (s *Service) checkDatasetExport(view *dbviews.View) error {
	if view == nil {
		return fmt.Errorf("dataset export requires a view")
	}
	source := strings.TrimSpace(view.Source)
	if !strings.HasPrefix(source, "dataset:") {
		return nil
	}
	slug := strings.TrimSpace(strings.TrimPrefix(source, "dataset:"))
	return s.checkDatasetExportSlug(slug)
}
