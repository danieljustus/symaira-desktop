// Package searchquery parses the small, portable search language exposed by
// symdesk. It deliberately does not generate SQL: callers can apply the plan
// to the sidecar while keeping parsing testable and shared across CLI, MCP and
// app clients.
package searchquery

import (
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
)

// Field is a metadata field supported by the query language.
type Field string

const (
	FieldPath     Field = "path"
	FieldTag      Field = "tag"
	FieldType     Field = "type"
	FieldStatus   Field = "status"
	FieldFilename Field = "filename"
	FieldFileType Field = "filetype"
	FieldCreated  Field = "created"
	FieldModified Field = "modified"
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

// Parse parses path:, tag:, type:, status:, filename:, filetype:, created:,
// modified:, quoted phrases, -negation and /regular expressions/. A syntax error
// is intentionally returned to callers;
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
				// Relative date windows intentionally remain readable (`last week`)
				// while the rest of the grammar is whitespace-delimited. Consume
				// the unit as part of a date filter only.
				if isDateField(field) && strings.EqualFold(filterValue, "last") {
					unitStart := skipSpace(input, next)
					unit, unitEnd := bare(input, unitStart)
					if isDateUnit(unit) {
						filterValue += " " + strings.ToLower(unit)
						next = unitEnd
					}
				}
				if isDateField(field) {
					if err := ValidateDateValue(filterValue); err != nil {
						return Plan{}, err
					}
				}
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
	case string(FieldFilename):
		return FieldFilename, value, true, nil
	case string(FieldFileType):
		return FieldFileType, value, true, nil
	case string(FieldCreated):
		return FieldCreated, value, true, nil
	case string(FieldModified):
		return FieldModified, value, true, nil
	default:
		if operatorName.MatchString(fieldName) {
			return "", "", false, fmt.Errorf("unknown search operator %q", fieldName+":")
		}
		return "", "", false, nil
	}
}

var operatorName = regexp.MustCompile(`^[[:alpha:]][[:alnum:]_-]*$`)

func isDateField(field Field) bool {
	return field == FieldCreated || field == FieldModified
}

func isDateUnit(value string) bool {
	switch strings.ToLower(value) {
	case "day", "week", "month", "year":
		return true
	default:
		return false
	}
}

// DateRange is an inclusive time interval used by created:/modified: filters.
type DateRange struct {
	From time.Time
	To   time.Time
}

// ParseDateValue resolves a date expression against reference. It accepts an
// ISO date, an RFC3339 timestamp, an inclusive `start..end` range, and the
// relative windows `last day`, `last week`, `last month`, and `last year`.
func ParseDateValue(value string, reference time.Time) (DateRange, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return DateRange{}, fmt.Errorf("date filter requires a value")
	}
	reference = reference.UTC()
	lower := strings.ToLower(value)
	if strings.HasPrefix(lower, "last ") {
		duration := map[string]time.Duration{
			"last day":   24 * time.Hour,
			"last week":  7 * 24 * time.Hour,
			"last month": 30 * 24 * time.Hour,
			"last year":  365 * 24 * time.Hour,
		}[lower]
		if duration == 0 {
			return DateRange{}, fmt.Errorf("invalid relative date %q", value)
		}
		return DateRange{From: reference.Add(-duration), To: reference}, nil
	}

	parts := strings.Split(value, "..")
	if len(parts) > 2 || (len(parts) == 2 && (strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "")) {
		return DateRange{}, fmt.Errorf("invalid date range %q", value)
	}
	from, fromDate, err := parseDateEndpoint(strings.TrimSpace(parts[0]))
	if err != nil {
		return DateRange{}, err
	}
	to := from
	toDate := fromDate
	if len(parts) == 2 {
		to, toDate, err = parseDateEndpoint(strings.TrimSpace(parts[1]))
		if err != nil {
			return DateRange{}, err
		}
	}
	if fromDate {
		from = startOfUTCDate(from)
	}
	if toDate {
		to = startOfUTCDate(to).Add(24*time.Hour - time.Nanosecond)
	}
	if from.After(to) {
		return DateRange{}, fmt.Errorf("date range starts after it ends: %q", value)
	}
	return DateRange{From: from, To: to}, nil
}

// ValidateDateValue validates a date expression without making it depend on
// the wall clock. Relative expressions are checked against a fixed epoch.
func ValidateDateValue(value string) error {
	for _, expression := range strings.Split(value, ",") {
		if strings.TrimSpace(expression) == "" {
			return fmt.Errorf("invalid date %q", value)
		}
		if _, err := ParseDateValue(expression, time.Unix(0, 0).UTC()); err != nil {
			return err
		}
	}
	return nil
}

func parseDateEndpoint(value string) (time.Time, bool, error) {
	if t, err := time.Parse("2006-01-02", value); err == nil {
		return t.UTC(), true, nil
	}
	if t, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return t.UTC(), false, nil
	}
	return time.Time{}, false, fmt.Errorf("invalid date %q (use YYYY-MM-DD or YYYY-MM-DD..YYYY-MM-DD)", value)
}

func startOfUTCDate(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
}
