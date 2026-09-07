package main

import "testing"

func TestRequireSuccessRejectsErrorOutcome(t *testing.T) {
	if err := requireSuccess(helperResult{Outcome: "error", ErrorClass: "readonly"}); err == nil {
		t.Fatal("error outcome must not be accepted as success")
	}
}

func TestRequireSuccessAcceptsOK(t *testing.T) {
	if err := requireSuccess(helperResult{Outcome: "ok"}); err != nil {
		t.Fatalf("ok outcome rejected: %v", err)
	}
}
