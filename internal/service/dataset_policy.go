package service

import (
	"fmt"
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

func (s *Service) checkDatasetExport(view *dbviews.View) error {
	if view == nil {
		return fmt.Errorf("dataset export requires a view")
	}
	source := strings.TrimSpace(view.Source)
	if !strings.HasPrefix(source, "dataset:") {
		return nil
	}
	slug := strings.TrimSpace(strings.TrimPrefix(source, "dataset:"))
	if slug == "" {
		return fmt.Errorf("dataset export requires a dataset slug")
	}
	handle, err := readDatasetHandle(s.VaultRoot, "datasets/"+slug+".md")
	if err != nil {
		return fmt.Errorf("dataset export policy: %w", err)
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
