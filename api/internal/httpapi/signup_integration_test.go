package httpapi_test

import (
	"strings"
	"testing"
)

func signupBody(over map[string]string) map[string]string {
	b := map[string]string{
		"slug":             "acme-freight",
		"name":             "Acme Freight",
		"timezone":         "America/Argentina/Buenos_Aires",
		"reference_prefix": "AF",
		"email":            "owner@acme-freight.example",
		"display_name":     "Ana Owner",
		"password":         "sup3r-secure-pass",
	}
	for k, v := range over {
		b[k] = v
	}
	return b
}

func TestSignupProvisionsOrgAndAdmin(t *testing.T) {
	e := newEnv(t, false)
	c := e.anon() // same jar carries the session cookie to the board
	res := c.do("POST", "/api/v1/signup", signupBody(nil), nil)
	if res.status != 200 {
		t.Fatalf("signup: %d %s", res.status, res.raw)
	}
	if res.body["role"] != "admin" || res.body["organization"] == nil {
		t.Fatalf("expected admin actor in response: %s", res.raw)
	}

	// The session issued by signup is already authenticated as the new admin:
	// the board answers with the org's (empty) shipment list, not another org's.
	board := c.do("GET", "/api/v1/shipments", nil, nil)
	if board.status != 200 {
		t.Fatalf("board: %d %s", board.status, board.raw)
	}

	// The same credentials sign in again through the normal password path.
	login := e.anon().do("POST", "/api/v1/auth/password-login",
		map[string]string{"email": "owner@acme-freight.example", "password": "sup3r-secure-pass"}, nil)
	if login.status != 200 || login.body["must_change_password"] != false {
		t.Fatalf("re-login: %d %s", login.status, login.raw)
	}
}

func TestSignupValidation(t *testing.T) {
	e := newEnv(t, false)
	cases := []map[string]string{
		{"slug": "BAD SLUG"},
		{"slug": "x"},
		{"name": ""},
		{"timezone": "Not/AZone"},
		{"reference_prefix": "a"},
		{"reference_prefix": "abcde"},
		{"email": "not-an-email"},
		{"password": "short"},
	}
	for _, over := range cases {
		res := e.anon().do("POST", "/api/v1/signup", signupBody(over), nil)
		if res.status != 422 {
			t.Fatalf("%v: want 422, got %d %s", over, res.status, res.raw)
		}
	}
}

func TestSignupConflicts(t *testing.T) {
	e := newEnv(t, false)
	first := e.anon().do("POST", "/api/v1/signup", signupBody(nil), nil)
	if first.status != 200 {
		t.Fatalf("first: %d %s", first.status, first.raw)
	}
	// Same slug, different email → 409 slug_taken.
	dupe := e.anon().do("POST", "/api/v1/signup", signupBody(map[string]string{"email": "other@x.example"}), nil)
	if dupe.status != 409 {
		t.Fatalf("slug dupe: %d %s", dupe.status, dupe.raw)
	}
	// Same email, different slug → 409 email_taken.
	dupeMail := e.anon().do("POST", "/api/v1/signup", signupBody(map[string]string{"slug": "other-co"}), nil)
	if dupeMail.status != 409 {
		t.Fatalf("email dupe: %d %s", dupeMail.status, dupeMail.raw)
	}
}

func TestSignupRateLimit(t *testing.T) {
	e := newEnv(t, false)
	// The limiter is keyed by RemoteAddr (127.0.0.1 in httptest).
	statuses := map[int]int{}
	for i := 0; i < 12; i++ {
		res := e.anon().do("POST", "/api/v1/signup", signupBody(map[string]string{
			"slug":  "spam-" + strings.Repeat("x", 2) + string(rune('a'+i)),
			"email": "spam" + string(rune('a'+i)) + "@x.example",
		}), nil)
		statuses[res.status]++
	}
	if statuses[429] == 0 {
		t.Fatalf("expected some 429s after limit, got %v", statuses)
	}
}

func TestSignupRejectsEmptyTimezoneAndLongPassword(t *testing.T) {
	e := newEnv(t, true)
	for _, over := range []map[string]string{
		{"timezone": ""},
		{"password": strings.Repeat("p", 73)},
		{"email": "a@@b"},
	} {
		if r := e.anon().do("POST", "/api/v1/signup", signupBody(over), nil); r.status != 422 {
			t.Fatalf("signup %v: %d %s", over, r.status, r.raw)
		}
	}
}
