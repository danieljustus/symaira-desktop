package paperlessimport

import (
	"errors"
	"testing"
)

func TestStagedError_Error(t *testing.T) {
	err := &stagedError{stage: "download", err: errors.New("network error")}
	got := err.Error()
	if got != "network error" {
		t.Errorf("Error() = %q, want %q", got, "network error")
	}
}

func TestStagedError_Unwrap(t *testing.T) {
	inner := errors.New("inner error")
	err := &stagedError{stage: "download", err: inner}

	unwrapped := err.Unwrap()
	if unwrapped != inner {
		t.Errorf("Unwrap() = %v, want %v", unwrapped, inner)
	}
}

func TestStageError(t *testing.T) {
	inner := errors.New("test error")
	err := stageError("download", inner)
	if err == nil {
		t.Fatal("stageError returned nil")
	}

	se, ok := err.(*stagedError)
	if !ok {
		t.Fatalf("err is not *stagedError, got %T", err)
	}
	if se.stage != "download" {
		t.Errorf("stage = %q, want download", se.stage)
	}
	if se.err != inner {
		t.Errorf("err = %v, want %v", se.err, inner)
	}
}

func TestStageError_Nil(t *testing.T) {
	err := stageError("download", nil)
	if err != nil {
		t.Errorf("stageError with nil = %v, want nil", err)
	}
}

func TestStageFromError_StagedError(t *testing.T) {
	inner := errors.New("test error")
	err := &stagedError{stage: "download", err: inner}

	stage := stageFromError(err)
	if stage != "download" {
		t.Errorf("stageFromError = %q, want download", stage)
	}
}

func TestStageFromError_DownloadMessage(t *testing.T) {
	err := errors.New("failed to download file")
	stage := stageFromError(err)
	if stage != "download" {
		t.Errorf("stageFromError = %q, want download", stage)
	}
}

func TestStageFromError_DetectMessage(t *testing.T) {
	err := errors.New("failed to detect source type")
	stage := stageFromError(err)
	if stage != "detect" {
		t.Errorf("stageFromError = %q, want detect", stage)
	}
}

func TestStageFromError_ExtractMessage(t *testing.T) {
	err := errors.New("failed to extract text")
	stage := stageFromError(err)
	if stage != "extract" {
		t.Errorf("stageFromError = %q, want extract", stage)
	}
}

func TestStageFromError_ArchiveMessage(t *testing.T) {
	err := errors.New("failed to archive file")
	stage := stageFromError(err)
	if stage != "archive" {
		t.Errorf("stageFromError = %q, want archive", stage)
	}
}

func TestStageFromError_WriteMessage(t *testing.T) {
	err := errors.New("failed to write note")
	stage := stageFromError(err)
	if stage != "write" {
		t.Errorf("stageFromError = %q, want write", stage)
	}
}

func TestStageFromError_MetadataMessage(t *testing.T) {
	err := errors.New("failed to record document")
	stage := stageFromError(err)
	if stage != "metadata" {
		t.Errorf("stageFromError = %q, want metadata", stage)
	}

	err2 := errors.New("failed to set vault path")
	stage2 := stageFromError(err2)
	if stage2 != "metadata" {
		t.Errorf("stageFromError = %q, want metadata", stage2)
	}

	err3 := errors.New("failed to complete job")
	stage3 := stageFromError(err3)
	if stage3 != "metadata" {
		t.Errorf("stageFromError = %q, want metadata", stage3)
	}
}

func TestStageFromError_Default(t *testing.T) {
	err := errors.New("some unknown error")
	stage := stageFromError(err)
	if stage != "import" {
		t.Errorf("stageFromError = %q, want import (default)", stage)
	}
}

func TestStageFromError_EmptyStage(t *testing.T) {
	inner := errors.New("test error")
	err := &stagedError{stage: "", err: inner}

	stage := stageFromError(err)
	// Empty stage should fall through to message-based detection.
	if stage != "import" {
		t.Errorf("stageFromError = %q, want import (default for empty stage)", stage)
	}
}
