package main

import (
	"reflect"
	"testing"
)

func TestBuildBaseViewWriteFixture(t *testing.T) {
	first, err := build()
	if err != nil {
		t.Fatal(err)
	}
	second, err := build()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("fixture generation must be deterministic")
	}
	if len(first.Steps) != 5 || first.Steps[4].Exists {
		t.Fatalf("unexpected Go write results: %+v", first.Steps)
	}
}
