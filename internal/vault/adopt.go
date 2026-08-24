package vault

import (
	"bytes"
	"fmt"
	"os"
	"strings"
)

// BackfillFrontmatterBytes inserts missing frontmatter fields into data while
// preserving existing frontmatter keys, unknown fields, comments, key ordering,
// line endings, and body bytes byte-for-byte.
func BackfillFrontmatterBytes(data []byte, missing map[string]interface{}) ([]byte, error) {
	if len(missing) == 0 {
		return data, nil
	}

	lineEnding := detectLineEnding(data)
	content := string(data)
	var lines []string
	if lineEnding == "\r\n" {
		lines = strings.Split(content, "\r\n")
	} else {
		lines = strings.Split(content, "\n")
	}

	// Canonical order of standard required keys: title, created, tags
	orderedKeys := []string{"title", "created", "tags"}
	var linesToAdd []string
	for _, k := range orderedKeys {
		if val, ok := missing[k]; ok {
			valStr, err := yamlValueString(val)
			if err != nil {
				return nil, fmt.Errorf("marshal %s: %w", k, err)
			}
			linesToAdd = append(linesToAdd, k+": "+valStr)
		}
	}
	for k, val := range missing {
		if k == "title" || k == "created" || k == "tags" {
			continue
		}
		valStr, err := yamlValueString(val)
		if err != nil {
			return nil, fmt.Errorf("marshal %s: %w", k, err)
		}
		linesToAdd = append(linesToAdd, k+": "+valStr)
	}

	if len(linesToAdd) == 0 {
		return data, nil
	}

	fmStart, fmEnd := findFrontmatterBounds(lines)
	if fmStart != 0 || fmEnd <= fmStart {
		// No frontmatter block at the top of the file: create one
		newLines := make([]string, 0)
		newLines = append(newLines, "---")
		newLines = append(newLines, linesToAdd...)
		newLines = append(newLines, "---")
		newLines = append(newLines, lines...)
		return []byte(strings.Join(newLines, lineEnding)), nil
	}

	// Existing frontmatter block: insert linesToAdd right before closing ---
	insert := append(linesToAdd, lines[fmEnd:]...)
	lines = append(lines[:fmEnd], insert...)

	return []byte(strings.Join(lines, lineEnding)), nil
}

// BackfillFrontmatter updates filePath with missing frontmatter fields atomically.
// If the content is unchanged, no write is performed.
func BackfillFrontmatter(filePath string, missing map[string]interface{}) error {
	data, err := os.ReadFile(filePath) //nolint:gosec // filePath is supplied by the vault walk
	if err != nil {
		return fmt.Errorf("read file: %w", err)
	}
	newData, err := BackfillFrontmatterBytes(data, missing)
	if err != nil {
		return err
	}
	if bytes.Equal(data, newData) {
		return nil
	}
	return writeFileAtomic(filePath, newData)
}
