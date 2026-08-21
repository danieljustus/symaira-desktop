package page

import "testing"

func TestNewRequest_ClampsBounds(t *testing.T) {
	tests := []struct {
		name       string
		limit      int
		offset     int
		wantLimit  int
		wantOffset int
	}{
		{name: "defaults", limit: 0, offset: -1, wantLimit: DefaultLimit, wantOffset: 0},
		{name: "max", limit: MaxLimit + 1, offset: 3, wantLimit: MaxLimit, wantOffset: 3},
		{name: "valid", limit: 7, offset: 2, wantLimit: 7, wantOffset: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NewRequest(tt.limit, tt.offset)
			if got.Limit != tt.wantLimit || got.Offset != tt.wantOffset {
				t.Errorf("NewRequest(%d, %d) = %+v, want limit=%d offset=%d", tt.limit, tt.offset, got, tt.wantLimit, tt.wantOffset)
			}
		})
	}
}

func TestTrim_SplitsOverFetchedRows(t *testing.T) {
	req := Request{Limit: 2}
	more := Trim([]string{"a", "b", "c"}, req)
	if len(more.Items) != 2 || !more.HasMore || more.Items[1] != "b" {
		t.Errorf("Trim(overfetch) = %+v, want [a b] with HasMore", more)
	}

	last := Trim([]string{"a"}, req)
	if len(last.Items) != 1 || last.HasMore {
		t.Errorf("Trim(last page) = %+v, want [a] without HasMore", last)
	}
}
