// Package searchquery parses the small, portable search language exposed by
// symdesk. It deliberately does not generate SQL: callers can apply the plan
// to the sidecar while keeping parsing testable and shared across CLI, MCP and
// app clients.
package searchquery

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

// Field is a metadata field supported by the query language.
type Field string

const (
	FieldPath   Field = "path"
	FieldTag    Field = "tag"
	FieldType   Field = "type"
	FieldStatus Field = "status"
)

// Filter limits results using an indexed metadata field.
type Filter struct {
	Field   Field
	Value   string
	Negated bool
}

// Term is a full-text term. Phrases retain their exact multi-word boundary;
// unquoted terms are prefix matches, preserving the existing FTS behaviour.
type Term struct {
	Value   string
	Phrase  bool
	Negated bool
}

// Regex is applied to a complete document after the sidecar's indexed filters
// and FTS terms have narrowed the candidate set. Go's RE2 implementation keeps
// user expressions bounded and avoids catastrophic backtracking.
type Regex struct {
	Pattern string
	Negated bool
	re      *regexp.Regexp
}

// Matches reports whether the compiled regular expression matches text.
func (r Regex) Matches(text string) bool {
	return r.re.MatchString(text)
}

// Plan is the parsed query. All entries are combined with AND semantics.
type Plan struct {
	Filters []Filter
	Terms   []Term
	Regexes []Regex
}

// RequiresSidecar reports whether the query uses syntax that a sibling search
// tool cannot faithfully interpret. Plain terms may still use symseek.
func (p Plan) RequiresSidecar() bool {
	if len(p.Filters) > 0 || len(p.Regexes) > 0 {
		return true
	}
	for _, term := range p.Terms {
		if term.Phrase || term.Negated {
			return true
		}
	}
	return false
}

// Parse parses path:, tag:, type:, status:, quoted phrases, -negation and
// /regular expressions/. A syntax error is intentionally returned to callers;
// they can then degrade the whole original query to safe plain full-text search.
func Parse(input string) (Plan, error) {
	var plan Plan
	for i := 0; ; {
		i = skipSpace(input, i)
		if i >= len(input) {
			return plan, nil
		}

		negated := false
		if input[i] == '-' {
			negated = true
			i++
			if i >= len(input) || unicode.IsSpace(rune(input[i])) {
				return Plan{}, fmt.Errorf("negation must be followed by a term")
			}
		}

		switch input[i] {
		case '"':
			value, next, err := quoted(input, i)
			if err != nil {
				return Plan{}, err
			}
			plan.Terms = append(plan.Terms, Term{Value: value, Phrase: true, Negated: negated})
			i = next
		case '/':
			pattern, next, err := regex(input, i)
			if err != nil {
				return Plan{}, err
			}
			compiled, err := regexp.Compile(pattern)
			if err != nil {
				return Plan{}, fmt.Errorf("invalid regular expression: %w", err)
			}
			plan.Regexes = append(plan.Regexes, Regex{Pattern: pattern, Negated: negated, re: compiled})
			i = next
		default:
			value, next := bare(input, i)
			if value == "" {
				return Plan{}, fmt.Errorf("expected search term")
			}
			if field, filterValue, ok, err := filter(value); err != nil {
				return Plan{}, err
			} else if ok {
				plan.Filters = append(plan.Filters, Filter{Field: field, Value: filterValue, Negated: negated})
			} else {
				plan.Terms = append(plan.Terms, Term{Value: value, Negated: negated})
			}
			i = next
		}

		if i < len(input) && !unicode.IsSpace(rune(input[i])) {
			return Plan{}, fmt.Errorf("unexpected character %q after search term", input[i])
		}
	}
}

func skipSpace(input string, i int) int {
	for i < len(input) && unicode.IsSpace(rune(input[i])) {
		i++
	}
	return i
}

func bare(input string, i int) (string, int) {
	start := i
	for i < len(input) && !unicode.IsSpace(rune(input[i])) {
		i++
	}
	return input[start:i], i
}

func quoted(input string, i int) (string, int, error) {
	start := i
	i++ // opening quote
	var value strings.Builder
	escaped := false
	for i < len(input) {
		ch := input[i]
		i++
		if escaped {
			value.WriteByte(ch)
			escaped = false
			continue
		}
		if ch == '\\' {
			escaped = true
			continue
		}
		if ch == '"' {
			if value.Len() == 0 {
				return "", 0, fmt.Errorf("empty quoted phrase at offset %d", start)
			}
			return value.String(), i, nil
		}
		value.WriteByte(ch)
	}
	return "", 0, fmt.Errorf("unterminated quoted phrase at offset %d", start)
}

func regex(input string, i int) (string, int, error) {
	start := i
	i++ // opening slash
	var pattern strings.Builder
	escaped := false
	for i < len(input) {
		ch := input[i]
		i++
		if escaped {
			pattern.WriteByte('\\')
			pattern.WriteByte(ch)
			escaped = false
			continue
		}
		if ch == '\\' {
			escaped = true
			continue
		}
		if ch == '/' {
			if pattern.Len() == 0 {
				return "", 0, fmt.Errorf("empty regular expression at offset %d", start)
			}
			return pattern.String(), i, nil
		}
		pattern.WriteByte(ch)
	}
	return "", 0, fmt.Errorf("unterminated regular expression at offset %d", start)
}

func filter(token string) (Field, string, bool, error) {
	fieldName, value, hasSeparator := strings.Cut(token, ":")
	if !hasSeparator {
		return "", "", false, nil
	}
	if value == "" {
		return "", "", false, fmt.Errorf("operator %q requires a value", fieldName+":")
	}

	switch strings.ToLower(fieldName) {
	case string(FieldPath):
		return FieldPath, value, true, nil
	case string(FieldTag):
		return FieldTag, value, true, nil
	case string(FieldType):
		return FieldType, value, true, nil
	case string(FieldStatus):
		return FieldStatus, value, true, nil
	default:
		if operatorName.MatchString(fieldName) {
			return "", "", false, fmt.Errorf("unknown search operator %q", fieldName+":")
		}
		return "", "", false, nil
	}
}

var operatorName = regexp.MustCompile(`^[[:alpha:]][[:alnum:]_-]*$`)
