// Package csvexport writes the shipment CSV export with an explicit column
// allow-list and spreadsheet formula escaping.
package csvexport

import (
	"encoding/csv"
	"io"
	"strings"
)

// Columns is the allow-list of exported fields. Internal audit data (actors,
// correlation IDs, versions, notes) is never exported.
var Columns = []string{
	"reference", "state", "priority", "owner", "account_name", "origin",
	"destination", "cargo_type", "required_date", "next_action_at", "due_at", "created_at",
}

// EscapeCell neutralises values that spreadsheet apps would evaluate as a
// formula (OWASP "CSV injection") by prefixing a single quote.
func EscapeCell(s string) string {
	if s == "" {
		return s
	}
	switch s[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + s
	}
	return s
}

type Writer struct{ w *csv.Writer }

func NewWriter(w io.Writer) (*Writer, error) {
	cw := csv.NewWriter(w)
	if err := cw.Write(Columns); err != nil {
		return nil, err
	}
	return &Writer{w: cw}, nil
}

// Write writes one row; values are keyed by column name. Unknown keys are
// ignored so callers cannot widen the export by accident.
func (w *Writer) Write(row map[string]string) error {
	rec := make([]string, len(Columns))
	for i, c := range Columns {
		rec[i] = EscapeCell(strings.ReplaceAll(row[c], "\x00", ""))
	}
	return w.w.Write(rec)
}

func (w *Writer) Flush() error {
	w.w.Flush()
	return w.w.Error()
}
