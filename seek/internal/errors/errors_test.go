package errors

import (
	"errors"
	"fmt"
	"testing"
)

func TestDatabaseError(t *testing.T) {
	t.Run("Error message format", func(t *testing.T) {
		inner := errors.New("connection refused")
		e := &DatabaseError{Op: "connect", Err: inner}

		want := "database connect: connection refused"
		if got := e.Error(); got != want {
			t.Errorf("Error() = %q, want %q", got, want)
		}
	})

	t.Run("Unwrap returns inner error", func(t *testing.T) {
		inner := errors.New("timeout")
		e := &DatabaseError{Op: "query", Err: inner}

		if !errors.Is(e, inner) {
			t.Error("errors.Is(e, inner) = false, want true")
		}
	})

	t.Run("errors.As works", func(t *testing.T) {
		inner := errors.New("disk full")
		e := &DatabaseError{Op: "write", Err: inner}

		var target *DatabaseError
		if !errors.As(e, &target) {
			t.Fatal("errors.As failed")
		}
		if target.Op != "write" {
			t.Errorf("Op = %q, want %q", target.Op, "write")
		}
	})

	t.Run("nil inner error", func(t *testing.T) {
		e := &DatabaseError{Op: "close", Err: nil}

		want := "database close: <nil>"
		if got := e.Error(); got != want {
			t.Errorf("Error() = %q, want %q", got, want)
		}
	})
}

func TestFileNotFoundError(t *testing.T) {
	t.Run("Error message format", func(t *testing.T) {
		e := &FileNotFoundError{Path: "/tmp/missing.txt"}

		want := "file not found: /tmp/missing.txt"
		if got := e.Error(); got != want {
			t.Errorf("Error() = %q, want %q", got, want)
		}
	})

	t.Run("errors.As works", func(t *testing.T) {
		e := &FileNotFoundError{Path: "/etc/passwd"}

		var target *FileNotFoundError
		if !errors.As(e, &target) {
			t.Fatal("errors.As failed")
		}
		if target.Path != "/etc/passwd" {
			t.Errorf("Path = %q, want %q", target.Path, "/etc/passwd")
		}
	})

	t.Run("empty path", func(t *testing.T) {
		e := &FileNotFoundError{Path: ""}

		want := "file not found: "
		if got := e.Error(); got != want {
			t.Errorf("Error() = %q, want %q", got, want)
		}
	})
}

func TestSearchError(t *testing.T) {
	t.Run("Error message format", func(t *testing.T) {
		inner := errors.New("index corrupted")
		e := &SearchError{Query: "test query", Err: inner}

		want := `search failed for query "test query": index corrupted`
		if got := e.Error(); got != want {
			t.Errorf("Error() = %q, want %q", got, want)
		}
	})

	t.Run("Unwrap returns inner error", func(t *testing.T) {
		inner := errors.New("timeout")
		e := &SearchError{Query: "q", Err: inner}

		if !errors.Is(e, inner) {
			t.Error("errors.Is(e, inner) = false, want true")
		}
	})

	t.Run("errors.As works", func(t *testing.T) {
		inner := errors.New("failed")
		e := &SearchError{Query: "search", Err: inner}

		var target *SearchError
		if !errors.As(e, &target) {
			t.Fatal("errors.As failed")
		}
		if target.Query != "search" {
			t.Errorf("Query = %q, want %q", target.Query, "search")
		}
	})

	t.Run("empty query", func(t *testing.T) {
		inner := errors.New("no results")
		e := &SearchError{Query: "", Err: inner}

		want := `search failed for query "": no results`
		if got := e.Error(); got != want {
			t.Errorf("Error() = %q, want %q", got, want)
		}
	})
}

func TestIndexError(t *testing.T) {
	t.Run("Error message format", func(t *testing.T) {
		inner := errors.New("permission denied")
		e := &IndexError{Path: "/data/file.txt", Err: inner}

		want := "indexing failed for /data/file.txt: permission denied"
		if got := e.Error(); got != want {
			t.Errorf("Error() = %q, want %q", got, want)
		}
	})

	t.Run("Unwrap returns inner error", func(t *testing.T) {
		inner := errors.New("io error")
		e := &IndexError{Path: "/file", Err: inner}

		if !errors.Is(e, inner) {
			t.Error("errors.Is(e, inner) = false, want true")
		}
	})

	t.Run("errors.As works", func(t *testing.T) {
		inner := errors.New("hash mismatch")
		e := &IndexError{Path: "/doc.md", Err: inner}

		var target *IndexError
		if !errors.As(e, &target) {
			t.Fatal("errors.As failed")
		}
		if target.Path != "/doc.md" {
			t.Errorf("Path = %q, want %q", target.Path, "/doc.md")
		}
	})

	t.Run("nil inner error", func(t *testing.T) {
		e := &IndexError{Path: "/file", Err: nil}

		want := "indexing failed for /file: <nil>"
		if got := e.Error(); got != want {
			t.Errorf("Error() = %q, want %q", got, want)
		}
	})
}

func TestValidationError(t *testing.T) {
	t.Run("Error message format", func(t *testing.T) {
		e := &ValidationError{Field: "email", Message: "invalid format"}

		want := "validation error for field email: invalid format"
		if got := e.Error(); got != want {
			t.Errorf("Error() = %q, want %q", got, want)
		}
	})

	t.Run("errors.As works", func(t *testing.T) {
		e := &ValidationError{Field: "name", Message: "required"}

		var target *ValidationError
		if !errors.As(e, &target) {
			t.Fatal("errors.As failed")
		}
		if target.Field != "name" {
			t.Errorf("Field = %q, want %q", target.Field, "name")
		}
		if target.Message != "required" {
			t.Errorf("Message = %q, want %q", target.Message, "required")
		}
	})

	t.Run("empty fields", func(t *testing.T) {
		e := &ValidationError{Field: "", Message: ""}

		want := "validation error for field : "
		if got := e.Error(); got != want {
			t.Errorf("Error() = %q, want %q", got, want)
		}
	})
}

func TestErrorWrappingChain(t *testing.T) {
	t.Run("chained DatabaseError", func(t *testing.T) {
		inner := errors.New("root cause")
		dbErr := &DatabaseError{Op: "migrate", Err: inner}
		wrapper := fmt.Errorf("migration failed: %w", dbErr)

		if !errors.Is(wrapper, inner) {
			t.Error("errors.Is(wrapper, inner) = false, want true")
		}

		var target *DatabaseError
		if !errors.As(wrapper, &target) {
			t.Fatal("errors.As failed")
		}
		if target.Op != "migrate" {
			t.Errorf("Op = %q, want %q", target.Op, "migrate")
		}
	})

	t.Run("chained SearchError", func(t *testing.T) {
		inner := errors.New("network error")
		searchErr := &SearchError{Query: "test", Err: inner}
		wrapper := fmt.Errorf("search operation failed: %w", searchErr)

		if !errors.Is(wrapper, inner) {
			t.Error("errors.Is(wrapper, inner) = false, want true")
		}

		var target *SearchError
		if !errors.As(wrapper, &target) {
			t.Fatal("errors.As failed")
		}
		if target.Query != "test" {
			t.Errorf("Query = %q, want %q", target.Query, "test")
		}
	})

	t.Run("chained IndexError", func(t *testing.T) {
		inner := errors.New("hash mismatch")
		idxErr := &IndexError{Path: "/file.go", Err: inner}
		wrapper := fmt.Errorf("indexing failed: %w", idxErr)

		if !errors.Is(wrapper, inner) {
			t.Error("errors.Is(wrapper, inner) = false, want true")
		}

		var target *IndexError
		if !errors.As(wrapper, &target) {
			t.Fatal("errors.As failed")
		}
		if target.Path != "/file.go" {
			t.Errorf("Path = %q, want %q", target.Path, "/file.go")
		}
	})
}
