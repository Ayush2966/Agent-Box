package errs

import (
	"errors"
	"fmt"
	"testing"
)

func TestErrorFormatting(t *testing.T) {
	e := New(Precondition, "role %q lacks CREATEDB", "app").WithStep("db").WithHint("ALTER ROLE app CREATEDB;")
	want := "db failed: role \"app\" lacks CREATEDB\nhint: ALTER ROLE app CREATEDB;"
	if e.Error() != want {
		t.Fatalf("got %q", e.Error())
	}
}

func TestWithHintCopies(t *testing.T) {
	base := New(Invalid, "x")
	_ = base.WithHint("h")
	if base.Hint != "" {
		t.Fatal("WithHint mutated the receiver")
	}
}

func TestFrom(t *testing.T) {
	if e := From(errors.New("boom")); e.Code != Internal || e.Message != "boom" {
		t.Fatalf("plain error: %+v", e)
	}
	wrapped := fmt.Errorf("context: %w", New(NotFound, "no box"))
	if e := From(wrapped); e.Code != NotFound || e.Message != "no box" {
		t.Fatalf("wrapped: %+v", e)
	}
}

func TestFromNil(t *testing.T) {
	if e := From(nil); e != nil {
		t.Fatalf("From(nil) = %+v, want nil", e)
	}
}
