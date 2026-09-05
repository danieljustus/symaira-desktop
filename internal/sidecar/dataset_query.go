package sidecar

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// DatasetQueryMaxRows is the largest page a sidecar dataset query will return.
// The limit is enforced in SQL before rows cross the sidecar boundary.
const DatasetQueryMaxRows = 1000

type DatasetFilter struct {
	Key      string
	Operator string
	Value    string
}

type DatasetFilterGroup struct {
	Operator string
	Filters  []DatasetFilter
	Groups   []DatasetFilterGroup
}

type DatasetSort struct {
	Key       string
	Ascending bool
}

type DatasetAggregate struct {
	Column   string
	Function string
	As       string
}

type DatasetQueryOptions struct {
	Schema      map[string]string
	Columns     []string
	Filters     []DatasetFilter
	FilterGroup *DatasetFilterGroup
	Sorts       []DatasetSort
	GroupBy     string
	Aggregates  []DatasetAggregate
	Limit       int
	Offset      int
}

type DatasetQueryRow struct {
	RowKey     string
	Identity   string
	Values     map[string]interface{}
	SourcePath string
	RowNumber  int
}

type DatasetQueryResult struct {
	Rows      []DatasetQueryRow
	TotalRows int
	Limit     int
	Offset    int
}

// QueryDataset evaluates only structured, validated query fields. Values stay
// in the derived sidecar and the LIMIT/OFFSET is applied by SQLite.
func (db *DB) QueryDataset(datasetSlug string, opts DatasetQueryOptions) (*DatasetQueryResult, error) {
	if strings.TrimSpace(datasetSlug) == "" {
		return nil, fmt.Errorf("dataset slug is required")
	}
	if len(opts.Schema) == 0 {
		return nil, fmt.Errorf("dataset schema is required")
	}
	columns := opts.Columns
	if len(columns) == 0 {
		for column := range opts.Schema {
			columns = append(columns, column)
		}
		sort.Strings(columns)
	}
	if err := validateDatasetColumns(columns, opts.Schema); err != nil {
		return nil, err
	}
	if opts.GroupBy != "" {
		if err := validateDatasetColumn(opts.GroupBy, opts.Schema); err != nil {
			return nil, fmt.Errorf("dataset group column %q not found", opts.GroupBy)
		}
	}
	for _, filter := range opts.Filters {
		if err := validateDatasetColumn(filter.Key, opts.Schema); err != nil {
			return nil, err
		}
	}
	if opts.FilterGroup != nil {
		if err := validateDatasetFilterGroup(*opts.FilterGroup, opts.Schema); err != nil {
			return nil, err
		}
	}
	for _, sortSpec := range opts.Sorts {
		if err := validateDatasetColumn(sortSpec.Key, opts.Schema); err != nil {
			return nil, fmt.Errorf("dataset sort column %q not found", sortSpec.Key)
		}
	}
	for _, aggregate := range opts.Aggregates {
		function := strings.ToLower(strings.TrimSpace(aggregate.Function))
		if function != "count" && function != "sum" && function != "min" && function != "max" && function != "average" {
			return nil, fmt.Errorf("unsupported dataset aggregate %q", aggregate.Function)
		}
		if function != "count" {
			if err := validateDatasetColumn(aggregate.Column, opts.Schema); err != nil {
				return nil, fmt.Errorf("dataset aggregate column %q not found", aggregate.Column)
			}
		}
	}

	where, args, err := datasetWhere(opts, opts.Schema)
	if err != nil {
		return nil, err
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 10
	}
	if limit > DatasetQueryMaxRows {
		limit = DatasetQueryMaxRows
	}
	offset := opts.Offset
	if offset < 0 {
		offset = 0
	}
	if opts.GroupBy != "" || len(opts.Aggregates) > 0 {
		return db.queryDatasetAggregate(datasetSlug, opts, where, args, limit, offset)
	}

	countQuery := "SELECT COUNT(*) FROM dataset_rows WHERE dataset_slug = ?" + where
	var total int
	countArgs := append([]interface{}{datasetSlug}, args...)
	if err := db.conn.QueryRow(countQuery, countArgs...).Scan(&total); err != nil {
		return nil, err
	}

	selectArgs := append([]interface{}{datasetSlug}, args...)
	selectSQL := "SELECT row_key, COALESCE(identity, ''), values_json, source_path, row_number FROM dataset_rows WHERE dataset_slug = ?" + where // #nosec G202 -- where is assembled only from fixed SQL fragments and bound values.
	orderSQL, orderArgs, err := datasetOrder(opts.Sorts, opts.Schema)
	if err != nil {
		return nil, err
	}
	selectSQL += orderSQL + " LIMIT ? OFFSET ?"
	selectArgs = append(selectArgs, orderArgs...)
	selectArgs = append(selectArgs, limit, offset)
	rows, err := db.conn.Query(selectSQL, selectArgs...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := &DatasetQueryResult{TotalRows: total, Limit: limit, Offset: offset, Rows: make([]DatasetQueryRow, 0, limit)}
	for rows.Next() {
		var row DatasetQueryRow
		var valuesJSON string
		if err := rows.Scan(&row.RowKey, &row.Identity, &valuesJSON, &row.SourcePath, &row.RowNumber); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(valuesJSON), &row.Values); err != nil {
			return nil, fmt.Errorf("decode dataset row %q: %w", row.RowKey, err)
		}
		if row.Values == nil {
			row.Values = map[string]interface{}{}
		}
		result.Rows = append(result.Rows, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func validateDatasetColumns(columns []string, schema map[string]string) error {
	for _, column := range columns {
		if err := validateDatasetColumn(column, schema); err != nil {
			return err
		}
	}
	return nil
}

func validateDatasetColumn(column string, schema map[string]string) error {
	switch column {
	case "identity", "_identity", "_key":
		return nil
	}
	if strings.TrimSpace(column) == "" || schema[column] == "" {
		if _, ok := schema[column]; !ok {
			return fmt.Errorf("dataset column %q not found", column)
		}
	}
	return nil
}

func validateDatasetFilterGroup(group DatasetFilterGroup, schema map[string]string) error {
	for _, filter := range group.Filters {
		if err := validateDatasetColumn(filter.Key, schema); err != nil {
			return err
		}
	}
	for _, child := range group.Groups {
		if err := validateDatasetFilterGroup(child, schema); err != nil {
			return err
		}
	}
	return nil
}

func datasetPath(column string) string { return `$."` + strings.ReplaceAll(column, `"`, `\"`) + `"` }

func datasetRaw(column string) (string, []interface{}) {
	switch column {
	case "identity", "_identity":
		return "identity", nil
	case "_key":
		return "row_key", nil
	}
	return "json_extract(values_json, ?)", []interface{}{datasetPath(column)}
}

func datasetPresent(column string) (string, []interface{}) {
	switch column {
	case "identity", "_identity":
		return "identity IS NOT NULL", nil
	case "_key":
		return "row_key IS NOT NULL", nil
	}
	return "json_type(values_json, ?) IS NOT NULL", []interface{}{datasetPath(column)}
}

func datasetTyped(column, typ string) (string, []interface{}) {
	raw, args := datasetRaw(column)
	switch strings.ToLower(typ) {
	case "number", "integer", "float":
		return "CAST(" + raw + " AS REAL)", args
	case "date", "datetime":
		return "julianday(" + raw + ")", args
	default:
		return "LOWER(CAST(" + raw + " AS TEXT))", args
	}
}

func datasetWhere(opts DatasetQueryOptions, schema map[string]string) (string, []interface{}, error) {
	parts := make([]string, 0, len(opts.Filters)+1)
	args := make([]interface{}, 0)
	for _, filter := range opts.Filters {
		expr, exprArgs, err := datasetFilterExpression(filter, schema)
		if err != nil {
			return "", nil, err
		}
		parts = append(parts, expr)
		args = append(args, exprArgs...)
	}
	if opts.FilterGroup != nil {
		expr, exprArgs, err := datasetFilterGroupExpression(*opts.FilterGroup, schema)
		if err != nil {
			return "", nil, err
		}
		parts = append(parts, expr)
		args = append(args, exprArgs...)
	}
	if len(parts) == 0 {
		return "", args, nil
	}
	return " AND (" + strings.Join(parts, ") AND (") + ")", args, nil
}

func datasetFilterGroupExpression(group DatasetFilterGroup, schema map[string]string) (string, []interface{}, error) {
	parts := make([]string, 0, len(group.Filters)+len(group.Groups))
	args := make([]interface{}, 0)
	for _, filter := range group.Filters {
		expr, exprArgs, err := datasetFilterExpression(filter, schema)
		if err != nil {
			return "", nil, err
		}
		parts = append(parts, expr)
		args = append(args, exprArgs...)
	}
	for _, child := range group.Groups {
		expr, exprArgs, err := datasetFilterGroupExpression(child, schema)
		if err != nil {
			return "", nil, err
		}
		parts = append(parts, expr)
		args = append(args, exprArgs...)
	}
	if len(parts) == 0 {
		return "1", nil, nil
	}
	joiner := " OR "
	if strings.ToLower(strings.TrimSpace(group.Operator)) != "any" {
		joiner = " AND "
	}
	return "(" + strings.Join(parts, joiner) + ")", args, nil
}

func datasetFilterExpression(filter DatasetFilter, schema map[string]string) (string, []interface{}, error) {
	if err := validateDatasetColumn(filter.Key, schema); err != nil {
		return "", nil, err
	}
	op := strings.ToLower(strings.TrimSpace(filter.Operator))
	if op == "" || op == "is" || op == "=" || op == "==" {
		op = "equals"
	}
	typ := schema[filter.Key]
	present, presentArgs := datasetPresent(filter.Key)
	typed, typedArgs := datasetTyped(filter.Key, typ)
	value := strings.TrimSpace(filter.Value)
	args := func(extra ...interface{}) []interface{} {
		return append(append([]interface{}{}, typedArgs...), extra...)
	}
	compare := func(operator string) (string, []interface{}) {
		if strings.EqualFold(typ, "date") || strings.EqualFold(typ, "datetime") {
			return present + " AND julianday(json_extract(values_json, ?)) " + operator + " julianday(?)", append(append([]interface{}{}, presentArgs...), datasetPath(filter.Key), value)
		}
		return present + " AND " + typed + " " + operator + " CAST(? AS " + datasetCastType(typ) + ")", append(append([]interface{}{}, presentArgs...), args(value)...)
	}
	switch op {
	case "equals":
		if value == "" {
			raw, rawArgs := datasetRaw(filter.Key)
			return "NOT (" + present + ") OR " + raw + " IS NULL OR CAST(" + raw + " AS TEXT) = ''", append(append(append([]interface{}{}, presentArgs...), rawArgs...), rawArgs...), nil
		}
		if strings.EqualFold(typ, "date") || strings.EqualFold(typ, "datetime") {
			return present + " AND julianday(json_extract(values_json, ?)) = julianday(?)", append(append([]interface{}{}, presentArgs...), []interface{}{datasetPath(filter.Key), value}...), nil
		}
		if strings.EqualFold(typ, "number") || strings.EqualFold(typ, "integer") || strings.EqualFold(typ, "float") {
			return present + " AND " + typed + " = CAST(? AS REAL)", append(append([]interface{}{}, presentArgs...), append(typedArgs, value)...), nil
		}
		return present + " AND " + typed + " = LOWER(?)", append(append([]interface{}{}, presentArgs...), append(typedArgs, strings.ToLower(value))...), nil
	case "not_equals", "is_not", "!=":
		if strings.EqualFold(typ, "date") || strings.EqualFold(typ, "datetime") {
			return "(NOT (" + present + ") OR NOT (julianday(json_extract(values_json, ?)) = julianday(?)))", append(append([]interface{}{}, presentArgs...), []interface{}{datasetPath(filter.Key), value}...), nil
		}
		if strings.EqualFold(typ, "number") || strings.EqualFold(typ, "integer") || strings.EqualFold(typ, "float") {
			return "(NOT (" + present + ") OR NOT (" + typed + " = CAST(? AS REAL)))", append(append([]interface{}{}, presentArgs...), append(typedArgs, value)...), nil
		}
		return "(NOT (" + present + ") OR NOT (" + typed + " = LOWER(?)))", append(append([]interface{}{}, presentArgs...), append(typedArgs, strings.ToLower(value))...), nil
	case "is_empty", "empty":
		raw, rawArgs := datasetRaw(filter.Key)
		return "NOT (" + present + ") OR " + raw + " IS NULL OR CAST(" + raw + " AS TEXT) = ''", append(append(append([]interface{}{}, presentArgs...), rawArgs...), rawArgs...), nil
	case "is_not_empty", "not_empty":
		raw, rawArgs := datasetRaw(filter.Key)
		return present + " AND " + raw + " IS NOT NULL AND CAST(" + raw + " AS TEXT) <> ''", append(presentArgs, append(rawArgs, rawArgs...)...), nil
	case "contains", "not_contains", "starts_with", "prefix", "ends_with", "suffix":
		raw, rawArgs := datasetRaw(filter.Key)
		pattern := "%" + value + "%"
		operator := "LIKE"
		switch op {
		case "starts_with", "prefix":
			pattern = value + "%"
		case "ends_with", "suffix":
			pattern = "%" + value
		}
		expr := "LOWER(CAST(" + raw + " AS TEXT)) " + operator + " LOWER(?)"
		if op == "not_contains" {
			return "NOT (" + present + " AND " + expr + ")", append(append([]interface{}{}, presentArgs...), append(rawArgs, pattern)...), nil
		}
		return present + " AND " + expr, append(append([]interface{}{}, presentArgs...), append(rawArgs, pattern)...), nil
	case "greater_than", "gt", ">":
		expr, exprArgs := compare(">")
		return expr, exprArgs, nil
	case "greater_than_or_equal", "gte", ">=":
		expr, exprArgs := compare(">=")
		return expr, exprArgs, nil
	case "less_than", "lt", "<":
		expr, exprArgs := compare("<")
		return expr, exprArgs, nil
	case "less_than_or_equal", "lte", "<=":
		expr, exprArgs := compare("<=")
		return expr, exprArgs, nil
	case "after", "before", "on_or_after", "on_or_before":
		opr := ">"
		switch op {
		case "before":
			opr = "<"
		case "on_or_after":
			opr = ">="
		case "on_or_before":
			opr = "<="
		}
		return present + " AND julianday(json_extract(values_json, ?)) " + opr + " julianday(?)", append(append([]interface{}{}, presentArgs...), []interface{}{datasetPath(filter.Key), value}...), nil
	case "in", "not_in", "contains_all", "contains_any", "contains_none":
		return datasetSetFilter(filter.Key, typ, op, value)
	default:
		return datasetFilterExpression(DatasetFilter{Key: filter.Key, Operator: "equals", Value: filter.Value}, schema)
	}
}

func datasetCastType(typ string) string {
	if strings.EqualFold(typ, "number") || strings.EqualFold(typ, "integer") || strings.EqualFold(typ, "float") {
		return "REAL"
	}
	return "TEXT"
}

func parseDatasetSet(value string) []string {
	value = strings.TrimSpace(strings.Trim(value, "[]"))
	if value == "" {
		return nil
	}
	if strings.Contains(value, ",") {
		return splitTrimmed(value, ",")
	}
	return strings.Fields(value)
}

func splitTrimmed(value, sep string) []string {
	parts := strings.Split(value, sep)
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			result = append(result, part)
		}
	}
	return result
}

func datasetSetRaw(column string) (string, []interface{}) {
	switch column {
	case "identity", "_identity":
		return "identity", nil
	case "_key":
		return "row_key", nil
	}
	return "CASE WHEN json_valid(values_json) = 1 THEN json_extract(values_json, ?) ELSE NULL END", []interface{}{datasetPath(column)}
}

func datasetSetPresent(column string) (string, []interface{}) {
	switch column {
	case "identity", "_identity":
		return "identity IS NOT NULL", nil
	case "_key":
		return "row_key IS NOT NULL", nil
	}
	return "CASE WHEN json_valid(values_json) = 1 THEN json_type(values_json, ?) ELSE NULL END IS NOT NULL", []interface{}{datasetPath(column)}
}

func datasetSetValid(column string) string {
	switch column {
	case "identity", "_identity", "_key":
		return "1"
	default:
		return "json_valid(values_json) = 1"
	}
}

func datasetSetContainer(column string) (string, []interface{}) {
	switch column {
	case "identity", "_identity", "_key":
		return "'[]'", nil
	}
	path := datasetPath(column)
	return "CASE WHEN json_valid(values_json) = 1 THEN CASE WHEN json_type(values_json, ?) IN ('array', 'object') THEN json_extract(values_json, ?) ELSE '[]' END ELSE '[]' END", []interface{}{path, path}
}

func datasetSetItems(typ string, items []string) []interface{} {
	values := make([]interface{}, 0, len(items))
	for _, item := range items {
		if strings.EqualFold(typ, "bool") || strings.EqualFold(typ, "boolean") {
			switch strings.ToLower(strings.TrimSpace(item)) {
			case "true", "1":
				item = "1"
			case "false", "0":
				item = "0"
			}
		} else if !strings.EqualFold(typ, "number") && !strings.EqualFold(typ, "integer") && !strings.EqualFold(typ, "float") {
			item = strings.ToLower(item)
		}
		values = append(values, item)
	}
	return values
}

func datasetSetInExpression(raw, typ string, count int) string {
	placeholders := make([]string, count)
	for i := range placeholders {
		placeholders[i] = "?"
		if strings.EqualFold(typ, "number") || strings.EqualFold(typ, "integer") || strings.EqualFold(typ, "float") {
			placeholders[i] = "CAST(? AS REAL)"
		}
	}
	valueList := strings.Join(placeholders, ",")
	if strings.EqualFold(typ, "number") || strings.EqualFold(typ, "integer") || strings.EqualFold(typ, "float") {
		return "CAST(" + raw + " AS REAL) IN (" + valueList + ")"
	}
	return "LOWER(CAST(" + raw + " AS TEXT)) IN (" + valueList + ")"
}

func datasetSetEqualsExpression(valueExpr, typ string) string {
	if strings.EqualFold(typ, "number") || strings.EqualFold(typ, "integer") || strings.EqualFold(typ, "float") {
		return "CAST(" + valueExpr + " AS REAL) = CAST(? AS REAL)"
	}
	return "LOWER(CAST(" + valueExpr + " AS TEXT)) = LOWER(?)"
}

func datasetSetFilter(column, typ, op, value string) (string, []interface{}, error) {
	items := parseDatasetSet(value)
	if len(items) == 0 {
		if op == "not_in" || op == "contains_none" {
			return "1", nil, nil
		}
		return "0", nil, nil
	}
	present, presentArgs := datasetSetPresent(column)
	raw, rawArgs := datasetSetRaw(column)
	container, containerArgs := datasetSetContainer(column)
	setValues := datasetSetItems(typ, items)
	scalarMatch := datasetSetInExpression(raw, typ, len(items))
	arrayMatch := "EXISTS (SELECT 1 FROM json_each(" + container + ") WHERE " + datasetSetInExpression("value", typ, len(items)) + ")"
	one := present + " AND (" + scalarMatch + " OR " + arrayMatch + ")"
	oneArgs := append([]interface{}{}, presentArgs...)
	oneArgs = append(oneArgs, rawArgs...)
	oneArgs = append(oneArgs, setValues...)
	oneArgs = append(oneArgs, containerArgs...)
	oneArgs = append(oneArgs, setValues...)
	if op == "in" {
		return one, oneArgs, nil
	}
	if op == "not_in" {
		return datasetSetValid(column) + " AND NOT (" + one + ")", oneArgs, nil
	}
	arrayOne := present + " AND " + arrayMatch
	arrayOneArgs := append([]interface{}{}, presentArgs...)
	arrayOneArgs = append(arrayOneArgs, containerArgs...)
	arrayOneArgs = append(arrayOneArgs, setValues...)
	if op == "contains_any" {
		return arrayOne, arrayOneArgs, nil
	}
	if op == "contains_none" {
		return datasetSetValid(column) + " AND NOT (" + arrayOne + ")", arrayOneArgs, nil
	}
	parts := make([]string, 0, len(items))
	args := make([]interface{}, 0, len(items)*(len(presentArgs)+len(containerArgs)+1))
	for i := range items {
		part := present + " AND EXISTS (SELECT 1 FROM json_each(" + container + ") WHERE " + datasetSetEqualsExpression("value", typ) + ")"
		parts = append(parts, part)
		args = append(args, presentArgs...)
		args = append(args, containerArgs...)
		args = append(args, setValues[i])
	}
	return "(" + strings.Join(parts, " AND ") + ")", args, nil
}

func datasetOrder(sorts []DatasetSort, schema map[string]string) (string, []interface{}, error) {
	if len(sorts) == 0 {
		return " ORDER BY row_key ASC", nil, nil
	}
	parts := make([]string, 0, len(sorts)+1)
	args := make([]interface{}, 0)
	for _, sortSpec := range sorts {
		if err := validateDatasetColumn(sortSpec.Key, schema); err != nil {
			return "", nil, err
		}
		raw, rawArgs := datasetRaw(sortSpec.Key)
		typed, typedArgs := datasetTyped(sortSpec.Key, schema[sortSpec.Key])
		order := "ASC"
		if !sortSpec.Ascending {
			order = "DESC"
		}
		parts = append(parts, "("+raw+" IS NULL) ASC, "+typed+" "+order)
		args = append(args, rawArgs...)
		args = append(args, typedArgs...)
	}
	parts = append(parts, "row_key ASC")
	return " ORDER BY " + strings.Join(parts, ", "), args, nil
}

func (db *DB) queryDatasetAggregate(slug string, opts DatasetQueryOptions, where string, whereArgs []interface{}, limit, offset int) (*DatasetQueryResult, error) {
	groupExpr, groupArgs := "", []interface{}{}
	if opts.GroupBy != "" {
		groupExpr, groupArgs = datasetRaw(opts.GroupBy)
	}
	aggExprs := make([]string, 0, len(opts.Aggregates))
	aggArgs := make([]interface{}, 0)
	for _, aggregate := range opts.Aggregates {
		function := strings.ToLower(strings.TrimSpace(aggregate.Function))
		if function == "count" && aggregate.Column == "" {
			aggExprs = append(aggExprs, "COUNT(*)")
			continue
		}
		raw, rawArgs := datasetRaw(aggregate.Column)
		switch function {
		case "count":
			aggExprs = append(aggExprs, "COUNT("+raw+")")
		case "sum":
			aggExprs = append(aggExprs, "SUM(CAST("+raw+" AS REAL))")
		case "min":
			aggExprs = append(aggExprs, "MIN("+raw+")")
		case "max":
			aggExprs = append(aggExprs, "MAX("+raw+")")
		case "average":
			aggExprs = append(aggExprs, "AVG(CAST("+raw+" AS REAL))")
		}
		aggArgs = append(aggArgs, rawArgs...)
	}
	if len(aggExprs) == 0 && opts.GroupBy != "" {
		aggExprs = append(aggExprs, "COUNT(*)")
	}
	selectParts := make([]string, 0, 1+len(aggExprs))
	if opts.GroupBy != "" {
		selectParts = append(selectParts, groupExpr)
	}
	selectParts = append(selectParts, aggExprs...)
	base := " FROM dataset_rows WHERE dataset_slug = ?" + where
	var total int
	if opts.GroupBy == "" {
		total = 1
	} else {
		countSQL := "SELECT COUNT(*) FROM (SELECT " + groupExpr + base + " GROUP BY " + groupExpr + ")"
		countArgs := append([]interface{}{}, groupArgs...)
		countArgs = append(countArgs, slug)
		countArgs = append(countArgs, whereArgs...)
		countArgs = append(countArgs, groupArgs...)
		if err := db.conn.QueryRow(countSQL, countArgs...).Scan(&total); err != nil {
			return nil, err
		}
	}
	query := "SELECT " + strings.Join(selectParts, ",") + base // #nosec G202 -- selectParts/base contain fixed SQL fragments; all data is passed as bound arguments.
	args := append([]interface{}{}, groupArgs...)
	args = append(args, aggArgs...)
	args = append(args, slug)
	args = append(args, whereArgs...)
	if opts.GroupBy != "" {
		query += " GROUP BY " + groupExpr + " ORDER BY " + groupExpr + " ASC"
		args = append(args, groupArgs...)
		args = append(args, groupArgs...)
	}
	query += " LIMIT ? OFFSET ?"
	args = append(args, limit, offset)
	rows, err := db.conn.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := &DatasetQueryResult{TotalRows: total, Limit: limit, Offset: offset, Rows: make([]DatasetQueryRow, 0, limit)}
	for rows.Next() {
		scanValues := make([]interface{}, len(selectParts))
		for i := range scanValues {
			scanValues[i] = new(interface{})
		}
		if err := rows.Scan(scanValues...); err != nil {
			return nil, err
		}
		values := make(map[string]interface{})
		index := 0
		if opts.GroupBy != "" {
			values[opts.GroupBy] = normalizeDatasetSQLValue(*scanValues[index].(*interface{}))
			index++
		}
		for i, aggregate := range opts.Aggregates {
			name := aggregate.As
			if name == "" {
				name = strings.ToLower(aggregate.Function) + "_" + aggregate.Column
				if strings.EqualFold(aggregate.Function, "count") && aggregate.Column == "" {
					name = "count"
				}
			}
			values[name] = normalizeDatasetSQLValue(*scanValues[index+i].(*interface{}))
		}
		if len(opts.Aggregates) == 0 {
			values["count"] = normalizeDatasetSQLValue(*scanValues[index].(*interface{}))
		}
		result.Rows = append(result.Rows, DatasetQueryRow{Values: values})
	}
	return result, rows.Err()
}

func normalizeDatasetSQLValue(value interface{}) interface{} {
	switch v := value.(type) {
	case []byte:
		return string(v)
	case int64:
		return float64(v)
	case int:
		return float64(v)
	case float32:
		return float64(v)
	default:
		return v
	}
}
