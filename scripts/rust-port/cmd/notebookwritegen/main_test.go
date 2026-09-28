package main

import (
	"reflect"
	"testing"
)

func TestBuildNotebookWriteFixture(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Fatal(err)
	}
	first, err := build(root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := build(root)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("fixture generation must be deterministic")
	}
	if len(first.Steps) != 5 || first.Steps[2].Output.Sources[0] != "a.md" {
		t.Fatalf("unexpected Go write results: %+v", first.Steps)
	}
}
