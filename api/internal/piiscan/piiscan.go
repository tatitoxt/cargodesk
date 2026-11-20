// Package piiscan flags fixture and seed content that looks like real
// personal data or secrets. It is a guard rail for synthetic demo data, not
// a general-purpose DLP tool: it errs on the side of false positives.
package piiscan

import (
	"bufio"
	"bytes"
	"regexp"
	"strings"
)

type Finding struct {
	File  string `json:"file"`
	Line  int    `json:"line"`
	Rule  string `json:"rule"`
	Match string `json:"match"`
}

type rule struct {
	name string
	re   *regexp.Regexp
	// allow returns true when a match is acceptable synthetic data.
	allow func(match string) bool
}

// Reserved example domains (RFC 2606 / RFC 6761) are the only ones allowed.
var allowedEmailSuffixes = []string{"@example.com", "@example.org", "@example.net", ".example", ".test", ".invalid"}

func allowedEmail(m string) bool {
	m = strings.ToLower(m)
	for _, s := range allowedEmailSuffixes {
		if strings.HasSuffix(m, s) {
			return true
		}
	}
	return false
}

func phoneLike(m string) bool {
	digits := 0
	for _, r := range m {
		if r >= '0' && r <= '9' {
			digits++
		}
	}
	// 555-01xx is the reserved fictional range in North America.
	return digits < 9 || strings.Contains(m, "555-01")
}

var rules = []rule{
	{"email", regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`), allowedEmail},
	{"phone", regexp.MustCompile(`(?:\+|\(|\b)\d[\d\s().-]{7,}\d\b`), phoneLike},
	{"street_address", regexp.MustCompile(`(?i)\b\d{1,5}\s+(?:[A-Za-z]+\s){1,3}(?:street|st\.|avenue|ave\.?|road|rd\.|boulevard|blvd|lane|calle|avenida)\b`), nil},
	{"aws_access_key", regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`), nil},
	{"github_token", regexp.MustCompile(`\b(?:ghp|gho|ghs|ghu)_[A-Za-z0-9]{30,}\b|\bgithub_pat_[A-Za-z0-9_]{30,}\b`), nil},
	{"api_key", regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}\b`), nil},
	{"private_key", regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`), nil},
	{"password_assignment", regexp.MustCompile(`(?i)"?(?:password|passwd|secret)"?\s*[:=]\s*"[^"]{6,}"`), nil},
}

// Scan returns findings for content; file is only used for reporting.
func Scan(file string, content []byte) []Finding {
	var out []Finding
	sc := bufio.NewScanner(bytes.NewReader(content))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		for _, r := range rules {
			for _, m := range r.re.FindAllString(line, -1) {
				if r.allow != nil && r.allow(m) {
					continue
				}
				out = append(out, Finding{File: file, Line: n, Rule: r.name, Match: redact(m)})
			}
		}
	}
	return out
}

// redact keeps findings useful without echoing a full secret into CI logs.
func redact(m string) string {
	if len(m) <= 8 {
		return m
	}
	return m[:4] + strings.Repeat("*", len(m)-8) + m[len(m)-4:]
}
