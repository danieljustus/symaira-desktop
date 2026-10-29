package fixtureoracle

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestNormalizeStringsPreservesJSONSyntax(t *testing.T) {
	input := map[string]any{"path": `C:\fixture\note.md`, "quoted": "a\"b\nline\tend", "nested": []any{nil, true, 7.0, `C:\fixture\two.md`}}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	normalized := NormalizeStrings(decoded, func(s string) string { return strings.ReplaceAll(s, `\`, "/") })
	out, err := json.Marshal(normalized)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if got["path"] != "C:/fixture/note.md" || got["quoted"] != input["quoted"] || !reflect.DeepEqual(got["nested"], []any{nil, true, 7.0, "C:/fixture/two.md"}) {
		t.Fatalf("decoded normalization: %#v", got)
	}
}
