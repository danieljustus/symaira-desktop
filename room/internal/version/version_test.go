package version

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestGetInfo(t *testing.T) {
	info := GetInfo()
	if info.Tool != "symroom" {
		t.Errorf("expected tool symroom, got %s", info.Tool)
	}
	if info.SchemaVersion != 1 {
		t.Errorf("expected schema_version 1, got %d", info.SchemaVersion)
	}

	var buf bytes.Buffer
	if err := info.Write(&buf); err != nil {
		t.Fatalf("failed to write version info: %v", err)
	}

	var data map[string]any
	if err := json.Unmarshal(buf.Bytes(), &data); err != nil {
		t.Fatalf("failed to parse json output: %v", err)
	}

	if data["tool"] != "symroom" {
		t.Errorf("expected json tool symroom, got %v", data["tool"])
	}
	if float64(data["schema_version"].(float64)) != 1 {
		t.Errorf("expected json schema_version 1, got %v", data["schema_version"])
	}
}
