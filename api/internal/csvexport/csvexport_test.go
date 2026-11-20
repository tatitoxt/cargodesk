package csvexport

import (
	"bytes"
	"encoding/csv"
	"slices"
	"testing"
)

func TestEscapeCell(t *testing.T) {
	cases := map[string]string{
		"=HYPERLINK(\"x\")": "'=HYPERLINK(\"x\")",
		"+1+2":              "'+1+2",
		"-5":                "'-5",
		"@SUM(A1)":          "'@SUM(A1)",
		"\tcmd":             "'\tcmd",
		"\rcmd":             "'\rcmd",
		"Plain text":        "Plain text",
		"a=b":               "a=b",
		"":                  "",
	}
	for in, want := range cases {
		if got := EscapeCell(in); got != want {
			t.Errorf("EscapeCell(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWriterQuotesAndAllowList(t *testing.T) {
	var buf bytes.Buffer
	w, err := NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	row := map[string]string{
		"reference":    "CD-1001",
		"account_name": "Acme \"Demo\", Inc.\nline two",
		"origin":       "=cmd|' /C calc'!A0",
		"notes":        "internal note must not leak",
		"version":      "7",
	}
	if err := w.Write(row); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(buf.Bytes(), []byte("internal note")) {
		t.Fatal("non-allow-listed field exported")
	}
	recs, err := csv.NewReader(&buf).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(recs[0], Columns) {
		t.Fatalf("header = %v", recs[0])
	}
	got := map[string]string{}
	for i, c := range Columns {
		got[c] = recs[1][i]
	}
	if got["account_name"] != "Acme \"Demo\", Inc.\nline two" {
		t.Fatalf("round trip of quotes/newlines failed: %q", got["account_name"])
	}
	if got["origin"] != "'=cmd|' /C calc'!A0" {
		t.Fatalf("formula not escaped: %q", got["origin"])
	}
}
