package errs

import (
	"errors"
	"fmt"
	"testing"
)

func TestError_FormatsAndUnwrapsCause(t *testing.T) {
	cause := errors.New("database failed")
	err := E("contact.Get", KindInternal, "unable to load contact", cause)
	if got := err.Error(); got != "contact.Get: unable to load contact: database failed" {
		t.Fatalf("Error() = %q", got)
	}
	if !errors.Is(err, cause) || err.Unwrap() != cause {
		t.Fatal("structured error does not unwrap its cause")
	}

	withoutCause := E("contact.List", KindInvalid, "bad request", nil)
	if got := withoutCause.Error(); got != "contact.List: bad request" {
		t.Fatalf("Error() without cause = %q", got)
	}
}

func TestKindOf_ClassifiesStructuredAndPlainErrors(t *testing.T) {
	if got := KindOf(nil); got != "" {
		t.Errorf("KindOf(nil) = %q, want empty", got)
	}
	if got := KindOf(errors.New("plain")); got != KindInternal {
		t.Errorf("KindOf(plain) = %q, want internal", got)
	}
	wrapped := fmt.Errorf("outer: %w", Conflict("contact.Create", "already exists", nil))
	if got := KindOf(wrapped); got != KindConflict {
		t.Errorf("KindOf(wrapped) = %q, want conflict", got)
	}
}

func TestConstructorsSetExpectedKinds(t *testing.T) {
	tests := []struct {
		name string
		make func() *Error
		want Kind
	}{
		{name: "not found", make: func() *Error { return NotFound("get", "missing", nil) }, want: KindNotFound},
		{name: "conflict", make: func() *Error { return Conflict("create", "duplicate", nil) }, want: KindConflict},
		{name: "invalid", make: func() *Error { return Invalid("create", "bad", nil) }, want: KindInvalid},
		{name: "internal", make: func() *Error { return Internal("load", "failed", nil) }, want: KindInternal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := KindOf(tt.make()); got != tt.want {
				t.Errorf("KindOf() = %q, want %q", got, tt.want)
			}
		})
	}
}
