// Export views: what the user composed in the UI, as JSON, turned into SQL.
//
// The browser never sends SQL. A view names fields and operators; only those
// in the whitelists below are accepted, and values always go in as
// parameters. Adding a filter means adding an entry to filterFields.
package main

import (
	"database/sql"
	"fmt"
	"strings"
)

// View is one export view. The same definition drives preview, export and
// (later) saved views.
type View struct {
	Filters []Filter `json:"filters"`
}

// Filter is one condition; all filters of a view must hold.
type Filter struct {
	Field string `json:"field"`
	Op    string `json:"op"`
	Value string `json:"value"`
}

// column is one output column.
type column struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	expr  string
	width float64 // in Excel character widths
}

// columns are fixed for now; choosing them is a later step.
var columns = []column{
	{"identifier", "Identifier", "d.identifier", 22},
	{"doctype", "Type", "d.doctype", 22},
	{"vergaderjaar", "Vergaderjaar", "d.vergaderjaar", 13},
	{"datum", "Datum", "d.datum", 12},
	{"title", "Titel", "d.title", 80},
	{"dossiernummer", "Dossiernummer", "d.dossiernummer", 15},
	{"preferred_url", "URL", "d.preferred_url", 50},
}

// filterFields maps a field name from the UI to its SQL expression and the
// operators it allows.
var filterFields = map[string]struct {
	expr string
	ops  []string
}{
	"doctype": {"d.doctype", []string{"eq"}},
}

var operators = map[string]string{
	"eq": "%s = ?",
}

// where returns the WHERE clause (possibly empty) and its arguments.
func (v View) where() (string, []any, error) {
	var conds []string
	var args []any
	for _, f := range v.Filters {
		field, ok := filterFields[f.Field]
		if !ok {
			return "", nil, fmt.Errorf("onbekend filterveld %q", f.Field)
		}
		allowed := false
		for _, op := range field.ops {
			allowed = allowed || op == f.Op
		}
		if !allowed {
			return "", nil, fmt.Errorf("filter %q ondersteunt %q niet", f.Field, f.Op)
		}
		conds = append(conds, fmt.Sprintf(operators[f.Op], field.expr))
		args = append(args, f.Value)
	}
	if len(conds) == 0 {
		return "", nil, nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args, nil
}

// selectSQL returns the query for the view's rows; limit <= 0 means all.
func (v View) selectSQL(limit int) (string, []any, error) {
	where, args, err := v.where()
	if err != nil {
		return "", nil, err
	}
	exprs := make([]string, len(columns))
	for i, c := range columns {
		exprs[i] = c.expr
	}
	q := "SELECT " + strings.Join(exprs, ", ") + " FROM document d" + where +
		" ORDER BY d.vergaderjaar, d.datum, d.identifier"
	if limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", limit)
	}
	return q, args, nil
}

func (v View) countSQL() (string, []any, error) {
	where, args, err := v.where()
	if err != nil {
		return "", nil, err
	}
	return "SELECT COUNT(*) FROM document d" + where, args, nil
}

func (v View) count(db *sql.DB) (int64, error) {
	q, args, err := v.countSQL()
	if err != nil {
		return 0, err
	}
	var n int64
	err = db.QueryRow(q, args...).Scan(&n)
	return n, err
}

// eachRow runs the view and calls fn for every row, as strings ("" for NULL).
func (v View) eachRow(db *sql.DB, limit int, fn func([]string) error) error {
	q, args, err := v.selectSQL(limit)
	if err != nil {
		return err
	}
	rows, err := db.Query(q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	vals := make([]sql.NullString, len(columns))
	ptrs := make([]any, len(columns))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return err
		}
		rec := make([]string, len(vals))
		for i, v := range vals {
			rec[i] = v.String
		}
		if err := fn(rec); err != nil {
			return err
		}
	}
	return rows.Err()
}
