package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigPathFixturesDeclareSerializedContent(t *testing.T) {
	count := 0
	for _, entry := range configPathCases() {
		for _, setup := range entry.Input.Setup {
			if !strings.Contains(setup.Content, "${") {
				continue
			}
			extension := strings.TrimPrefix(filepath.Ext(setup.Path), ".")
			if extension != "json" && extension != "toml" {
				continue
			}
			count++
			if setup.ContentFormat != extension {
				t.Errorf("case %s: %s content needs explicit %s placeholder escaping", entry.ID, setup.Path, extension)
			}
		}
	}
	if count == 0 {
		t.Fatal("serialized path fixtures were not exercised")
	}
}
