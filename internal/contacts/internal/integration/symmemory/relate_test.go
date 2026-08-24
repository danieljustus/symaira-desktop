package symmemory

import (
	"context"
	"errors"
	"testing"
)

func TestRelate_MissingBinary_ReturnsErrUnavailable(t *testing.T) {
	withEmptyPath(t)
	_, err := Relate(context.Background(), RelateInput{
		FromEntityID: "a",
		ToEntityID:   "b",
		RelationType: "works-with",
	})
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("Relate() error = %v, want ErrUnavailable", err)
	}
}

func TestRelate_EmptyInput_ReturnsErrUnavailable(t *testing.T) {
	withFakeBinary(t, `echo '{"id":"r1"}'`)
	_, err := Relate(context.Background(), RelateInput{
		FromEntityID: "",
		ToEntityID:   "b",
		RelationType: "works-with",
	})
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("Relate() with empty from id error = %v, want ErrUnavailable", err)
	}
}

func TestRelate_ValidResponse_Succeeds(t *testing.T) {
	withFakeBinary(t, `echo '{"id":"r1","from_entity_id":"a","to_entity_id":"b","relation_type":"works-with","source":"symrelate","source_ref":"contact-1","verification":"verified"}'`)
	rel, err := Relate(context.Background(), RelateInput{
		FromEntityID: "a",
		ToEntityID:   "b",
		RelationType: "works-with",
		Source:       "symrelate",
		SourceRef:    "contact-1",
		Verification: "verified",
	})
	if err != nil {
		t.Fatalf("Relate() error = %v", err)
	}
	if rel.ID != "r1" || rel.FromEntityID != "a" || rel.ToEntityID != "b" || rel.RelationType != "works-with" {
		t.Errorf("Relate() = %+v, want relation r1 a--works-with-->b", rel)
	}
	if rel.Source != "symrelate" || rel.SourceRef != "contact-1" || rel.Verification != "verified" {
		t.Errorf("Relate() provenance = %+v, want symrelate/contact-1/verified", rel)
	}
}

func TestRelate_MalformedJSON_ReturnsErrUnavailable(t *testing.T) {
	withFakeBinary(t, `echo 'not json'`)
	_, err := Relate(context.Background(), RelateInput{
		FromEntityID: "a",
		ToEntityID:   "b",
		RelationType: "works-with",
	})
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("Relate() error = %v, want ErrUnavailable", err)
	}
}

func TestRelate_NonZeroExit_ReturnsErrUnavailable(t *testing.T) {
	withFakeBinary(t, `exit 1`)
	_, err := Relate(context.Background(), RelateInput{
		FromEntityID: "a",
		ToEntityID:   "b",
		RelationType: "works-with",
	})
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("Relate() error = %v, want ErrUnavailable", err)
	}
}

func TestUnrelate_MissingBinary_ReturnsErrUnavailable(t *testing.T) {
	withEmptyPath(t)
	_, err := Unrelate(context.Background(), "r1")
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("Unrelate() error = %v, want ErrUnavailable", err)
	}
}

func TestUnrelate_EmptyID_ReturnsErrUnavailable(t *testing.T) {
	withFakeBinary(t, `echo '{"id":"r1"}'`)
	_, err := Unrelate(context.Background(), "")
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("Unrelate(\"\") error = %v, want ErrUnavailable", err)
	}
}

func TestUnrelate_ValidResponse_Succeeds(t *testing.T) {
	withFakeBinary(t, `echo '{"id":"r1","from_entity_id":"a","to_entity_id":"b","relation_type":"works-with"}'`)
	rel, err := Unrelate(context.Background(), "r1")
	if err != nil {
		t.Fatalf("Unrelate() error = %v", err)
	}
	if rel.ID != "r1" {
		t.Errorf("Unrelate() = %+v, want relation r1", rel)
	}
}

func TestUnrelate_MalformedJSON_ReturnsErrUnavailable(t *testing.T) {
	withFakeBinary(t, `echo 'not json'`)
	_, err := Unrelate(context.Background(), "r1")
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("Unrelate() error = %v, want ErrUnavailable", err)
	}
}

func TestUnrelate_NonZeroExit_ReturnsErrUnavailable(t *testing.T) {
	withFakeBinary(t, `exit 1`)
	_, err := Unrelate(context.Background(), "r1")
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("Unrelate() error = %v, want ErrUnavailable", err)
	}
}
