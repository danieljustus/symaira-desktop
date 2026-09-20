package inventory

import "fmt"

// FirstDifference reports where two fixture encodings start to differ, so a
// platform-specific drift names the offending field instead of only the fact.
// An empty report means the contents are equal up to the shorter one.
func FirstDifference(recorded, checked []byte) string {
	limit := len(recorded)
	if len(checked) < limit {
		limit = len(checked)
	}
	at := limit
	for i := 0; i < limit; i++ {
		if recorded[i] != checked[i] {
			at = i
			break
		}
	}
	window := func(content []byte) string {
		end := at + 60
		if end > len(content) {
			end = len(content)
		}
		return string(content[at:end])
	}
	return fmt.Sprintf("first difference at byte %d: recorded=%q checked=%q", at, window(recorded), window(checked))
}
