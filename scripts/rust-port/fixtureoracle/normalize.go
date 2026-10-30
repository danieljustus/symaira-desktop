package fixtureoracle

// NormalizeStrings transforms decoded observation values, never serialized JSON
// syntax. In particular, Windows path separators must not rewrite JSON's quote,
// newline, or Unicode escapes. Object keys remain unchanged.
func NormalizeStrings(value any, normalize func(string) string) any {
	switch value := value.(type) {
	case string:
		return normalize(value)
	case []any:
		for i := range value {
			value[i] = NormalizeStrings(value[i], normalize)
		}
	case map[string]any:
		for key, item := range value {
			value[key] = NormalizeStrings(item, normalize)
		}
	}
	return value
}
