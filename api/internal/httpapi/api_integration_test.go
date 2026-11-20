package httpapi_test

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tatitoxt/cargodesk/api/internal/httpapi"
	"github.com/tatitoxt/cargodesk/api/internal/seed"
	"github.com/tatitoxt/cargodesk/api/internal/store"
	"github.com/tatitoxt/cargodesk/api/internal/testdb"
)

type env struct {
	t   *testing.T
	srv *httptest.Server
	now atomic.Pointer[time.Time]
}

func newEnv(t *testing.T, demo bool) *env {
	pool := testdb.Fresh(t)
	e := &env{t: t}
	now := testdb.Now
	e.now.Store(&now)
	h := httpapi.New(httpapi.Config{Store: store.New(pool), DemoAuth: demo,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Clock: func() time.Time { return *e.now.Load() }})
	e.srv = httptest.NewServer(h)
	t.Cleanup(e.srv.Close)
	return e
}

func (e *env) advance(d time.Duration) {
	n := e.now.Load().Add(d)
	e.now.Store(&n)
}

type client struct {
	e    *env
	http *http.Client
}

func (e *env) anon() *client {
	jar, _ := cookiejar.New(nil)
	return &client{e: e, http: &http.Client{Jar: jar}}
}

func (e *env) login(userID string) *client {
	c := e.anon()
	res := c.do("POST", "/api/v1/auth/demo-login", map[string]string{"user_id": userID}, nil)
	if res.status != 200 {
		e.t.Fatalf("login %s: %d %s", userID, res.status, res.raw)
	}
	return c
}

type resp struct {
	status int
	header http.Header
	raw    []byte
	body   map[string]any
}

func (c *client) do(method, path string, body any, hdr map[string]string) resp {
	c.e.t.Helper()
	var rd io.Reader
	if body != nil {
		if s, ok := body.(string); ok {
			rd = strings.NewReader(s)
		} else {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
	}
	req, _ := http.NewRequest(method, c.e.srv.URL+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := c.http.Do(req)
	if err != nil {
		c.e.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	r := resp{status: res.StatusCode, header: res.Header, raw: raw}
	_ = json.Unmarshal(raw, &r.body)
	return r
}

func (c *client) mustOK(want int, method, path string, body any) map[string]any {
	c.e.t.Helper()
	r := c.do(method, path, body, nil)
	if r.status != want {
		c.e.t.Fatalf("%s %s: status %d, want %d: %s", method, path, r.status, want, r.raw)
	}
	return r.body
}

func ver(m map[string]any) int { return int(m["version"].(float64)) }

func createBody(account string) map[string]any {
	return map[string]any{"account_name": account, "contact_label": "Demo contact", "origin": "Origin (demo)",
		"destination": "Destination (demo)", "cargo_type": "palletized", "required_date": "2026-03-20", "priority": "high"}
}

func TestHealthAndCorrelationID(t *testing.T) {
	e := newEnv(t, true)
	r := e.anon().do("GET", "/healthz", nil, map[string]string{"X-Correlation-ID": "abc-123"})
	if r.status != 200 || r.header.Get("X-Correlation-ID") != "abc-123" {
		t.Fatalf("%d %v", r.status, r.header)
	}
	r = e.anon().do("GET", "/healthz", nil, map[string]string{"X-Correlation-ID": "bad id with spaces"})
	if got := r.header.Get("X-Correlation-ID"); got == "" || got == "bad id with spaces" {
		t.Fatalf("invalid correlation id was echoed: %q", got)
	}
}

func TestAuthentication(t *testing.T) {
	e := newEnv(t, true)
	anon := e.anon()
	if r := anon.do("GET", "/api/v1/shipments", nil, nil); r.status != 401 || r.body["error"] != "unauthenticated" {
		t.Fatalf("anon list: %d %s", r.status, r.raw)
	}
	users := anon.mustOK(200, "GET", "/api/v1/auth/demo-users", nil)["users"].([]any)
	if len(users) != 5 {
		t.Fatalf("demo users = %d", len(users))
	}
	if r := anon.do("POST", "/api/v1/auth/demo-login", map[string]string{"user_id": "00000000-0000-4000-b000-0000000000ff"}, nil); r.status != 404 {
		t.Fatalf("unknown user login: %d", r.status)
	}
	c := e.login(seed.CoordinatorA)
	me := c.mustOK(200, "GET", "/api/v1/me", nil)
	if me["role"] != "coordinator" {
		t.Fatalf("me = %v", me)
	}
	if r := c.do("POST", "/api/v1/auth/logout", nil, nil); r.status != 204 {
		t.Fatalf("logout: %d", r.status)
	}
	if r := c.do("GET", "/api/v1/me", nil, nil); r.status != 401 {
		t.Fatalf("after logout: %d", r.status)
	}
	e.advance(13 * time.Hour)
	c = e.login(seed.CoordinatorA)
	e.advance(13 * time.Hour)
	if r := c.do("GET", "/api/v1/me", nil, nil); r.status != 401 {
		t.Fatalf("expired session: %d", r.status)
	}
}

func TestDemoAuthDisabled(t *testing.T) {
	e := newEnv(t, false)
	if r := e.anon().do("GET", "/api/v1/auth/demo-users", nil, nil); r.status != 404 {
		t.Fatalf("demo users with demo auth off: %d", r.status)
	}
	if r := e.anon().do("POST", "/api/v1/auth/demo-login", map[string]string{"user_id": seed.AdminA}, nil); r.status != 404 {
		t.Fatalf("demo login with demo auth off: %d", r.status)
	}
}

// The main operator flow, through HTTP only.
func TestOperatorFlowEndToEnd(t *testing.T) {
	e := newEnv(t, true)
	c := e.login(seed.CoordinatorA)
	sh := c.mustOK(201, "POST", "/api/v1/shipments", createBody("Flow Test Account"))
	id := sh["id"].(string)
	if sh["state"] != "new" || sh["sla_status"] != "on_track" || !strings.HasPrefix(sh["reference"].(string), "CD-") {
		t.Fatalf("created %v", sh)
	}
	p := "/api/v1/shipments/" + id
	sh = c.mustOK(200, "PATCH", p+"/assignment", map[string]any{"version": ver(sh), "owner_id": seed.Coordinator2,
		"next_action_at": testdb.Now.Add(2 * time.Hour), "next_action_note": "send quote"})
	if sh["owner"].(map[string]any)["display_name"] != "Bruno Sample" {
		t.Fatalf("owner %v", sh["owner"])
	}
	steps := []map[string]any{
		{"to": "quoted", "quote": map[string]any{"amount_cents": 125000, "currency": "USD"}},
		{"to": "accepted"},
		{"to": "scheduled", "pickup_at": testdb.Now.Add(24 * time.Hour), "carrier": "Carrier Test"},
		{"to": "in_transit"},
		{"to": "delivered", "evidence": map[string]any{"received_by": "Dock clerk", "delivered_at": testdb.Now, "note": "POD #1"}},
	}
	for _, s := range steps {
		s["version"] = ver(sh)
		sh = c.mustOK(200, "POST", p+"/transitions", s)
		if sh["state"] != s["to"] {
			t.Fatalf("state %v, want %v", sh["state"], s["to"])
		}
	}
	if sh["sla_status"] != "met" || sh["next_action_at"] != nil || len(sh["allowed_transitions"].([]any)) != 0 {
		t.Fatalf("delivered %v", sh)
	}
	evs := c.mustOK(200, "GET", p+"/events", nil)["events"].([]any)
	var actions []string
	for _, ev := range evs {
		actions = append(actions, ev.(map[string]any)["action"].(string))
	}
	if got := strings.Join(actions, ","); got != "created,assigned,transitioned,transitioned,transitioned,transitioned,transitioned" {
		t.Fatalf("timeline %s", got)
	}
	d := c.mustOK(200, "GET", p, nil)
	if d["received_by"] != "Dock clerk" || len(d["quotes"].([]any)) != 1 {
		t.Fatalf("detail %v", d)
	}
}

func TestTransitionErrors(t *testing.T) {
	e := newEnv(t, true)
	c := e.login(seed.CoordinatorA)
	sh := c.mustOK(201, "POST", "/api/v1/shipments", createBody("Errors"))
	p := "/api/v1/shipments/" + sh["id"].(string) + "/transitions"

	r := c.do("POST", p, map[string]any{"version": 1, "to": "delivered"}, nil)
	if r.status != 409 || r.body["error"] != "invalid_transition" || r.body["current_state"] != "new" {
		t.Fatalf("invalid transition: %d %s", r.status, r.raw)
	}
	r = c.do("POST", p, map[string]any{"version": 1, "to": "quoted", "quote": map[string]any{"amount_cents": 0, "currency": "usd"}}, nil)
	if r.status != 422 || r.body["error"] != "transition_requirements" || len(r.body["fields"].([]any)) != 2 {
		t.Fatalf("requirements: %d %s", r.status, r.raw)
	}
	r = c.do("POST", p, map[string]any{"version": 1, "to": "needs_info"}, nil)
	if r.status != 422 {
		t.Fatalf("needs_info without reason: %d", r.status)
	}
	c.mustOK(200, "POST", p, map[string]any{"version": 1, "to": "needs_info", "reason": "missing weight"})
	r = c.do("POST", p, map[string]any{"version": 1, "to": "cancelled", "reason": "stale"}, nil)
	if r.status != 409 || r.body["error"] != "version_conflict" || r.body["version_current"].(float64) != 2 {
		t.Fatalf("stale version: %d %s", r.status, r.raw)
	}
	if r := c.do("POST", p, map[string]any{"to": "cancelled", "reason": "x"}, nil); r.status != 422 {
		t.Fatalf("missing version: %d", r.status)
	}
}

func TestOverdueAndFilters(t *testing.T) {
	e := newEnv(t, true)
	c := e.login(seed.CoordinatorA)
	list := func(q string) []string {
		t.Helper()
		items := c.mustOK(200, "GET", "/api/v1/shipments"+q, nil)["items"].([]any)
		var refs []string
		for _, it := range items {
			refs = append(refs, it.(map[string]any)["reference"].(string))
		}
		return refs
	}
	if n := len(list("")); n != 8 {
		t.Fatalf("all = %d, want 8 seeded for tenant A", n)
	}
	if got := strings.Join(list("?overdue=true"), ","); got != "CD-1004,CD-1002" {
		t.Fatalf("overdue = %s", got)
	}
	if got := strings.Join(list("?state=needs_info"), ","); got != "CD-1003" {
		t.Fatalf("needs_info = %s", got)
	}
	if got := len(list("?state=new,needs_info")); got != 3 {
		t.Fatalf("new+needs_info = %d", got)
	}
	if got := len(list("?owner=none")); got != 3 {
		t.Fatalf("unassigned = %d", got)
	}
	if got := len(list("?owner=me")); got != 3 {
		t.Fatalf("mine = %d", got)
	}
	if got := strings.Join(list("?q=umbrella"), ","); got != "CD-1004" {
		t.Fatalf("search = %s", got)
	}
	if got := strings.Join(list("?q=cd-1001"), ","); got != "CD-1001" {
		t.Fatalf("search by reference = %s", got)
	}
	if got := strings.Join(list("?due=today"), ","); got != "CD-1004,CD-1003" {
		t.Fatalf("due today = %s", got)
	}
	if got := len(list("?q=%25")); got != 0 {
		t.Fatalf("LIKE wildcard was not escaped: %d", got)
	}
	if r := c.do("GET", "/api/v1/shipments?state=bogus", nil, nil); r.status != 422 {
		t.Fatalf("bad state filter: %d", r.status)
	}
	// A new request becomes overdue once its quote SLA (8h for high) passes.
	sh := c.mustOK(201, "POST", "/api/v1/shipments", createBody("Late"))
	e.advance(9 * time.Hour)
	got := c.mustOK(200, "GET", "/api/v1/shipments/"+sh["id"].(string), nil)
	if got["sla_status"] != "breached" || got["overdue"] != true {
		t.Fatalf("after 9h: %v %v", got["sla_status"], got["overdue"])
	}
}

func TestRoles(t *testing.T) {
	e := newEnv(t, true)
	viewer := e.login(seed.ViewerA)
	coord := e.login(seed.CoordinatorA)
	admin := e.login(seed.AdminA)
	sh := coord.mustOK(201, "POST", "/api/v1/shipments", createBody("Roles"))
	p := "/api/v1/shipments/" + sh["id"].(string)

	viewer.mustOK(200, "GET", "/api/v1/shipments", nil)
	viewer.mustOK(200, "GET", p, nil)
	for _, req := range []struct {
		method, path string
		body         any
	}{
		{"POST", "/api/v1/shipments", createBody("x")},
		{"POST", p + "/transitions", map[string]any{"version": 1, "to": "cancelled", "reason": "x"}},
		{"PATCH", p + "/assignment", map[string]any{"version": 1, "owner_id": seed.ViewerA}},
		{"POST", p + "/events", map[string]any{"kind": "note", "body": "x"}},
		{"GET", "/api/v1/exports/shipments.csv", nil},
		{"PUT", "/api/v1/settings/sla", map[string]any{"hours": map[string]int{"low": 1, "normal": 1, "high": 1, "urgent": 1}}},
	} {
		if r := viewer.do(req.method, req.path, req.body, nil); r.status != 403 {
			t.Fatalf("viewer %s %s: %d", req.method, req.path, r.status)
		}
	}
	hours := map[string]any{"hours": map[string]int{"low": 48, "normal": 12, "high": 4, "urgent": 1}}
	if r := coord.do("PUT", "/api/v1/settings/sla", hours, nil); r.status != 403 {
		t.Fatalf("coordinator SLA update: %d", r.status)
	}
	admin.mustOK(200, "PUT", "/api/v1/settings/sla", hours)
	if r := admin.do("PUT", "/api/v1/settings/sla", map[string]any{"hours": map[string]int{"low": 0, "normal": 12, "high": 4, "urgent": 1}}, nil); r.status != 422 {
		t.Fatalf("invalid SLA: %d %s", r.status, r.raw)
	}
	// The new policy applies to new requests.
	created := coord.mustOK(201, "POST", "/api/v1/shipments", createBody("After policy change"))
	due, _ := time.Parse(time.RFC3339, created["due_at"].(string))
	if !due.Equal(testdb.Now.Add(4 * time.Hour)) {
		t.Fatalf("due_at = %v", due)
	}
}

func TestTenantIsolationOverHTTP(t *testing.T) {
	e := newEnv(t, true)
	a := e.login(seed.CoordinatorA)
	b := e.login(seed.CoordinatorB)
	sh := a.mustOK(201, "POST", "/api/v1/shipments", createBody("Tenant A only"))
	p := "/api/v1/shipments/" + sh["id"].(string)
	for _, r := range []resp{
		b.do("GET", p, nil, nil),
		b.do("GET", p+"/events", nil, nil),
		b.do("POST", p+"/transitions", map[string]any{"version": 1, "to": "cancelled", "reason": "x"}, nil),
		b.do("PATCH", p+"/assignment", map[string]any{"version": 1, "priority": "low"}, nil),
		b.do("POST", p+"/events", map[string]any{"kind": "note", "body": "x"}, nil),
	} {
		if r.status != 404 {
			t.Fatalf("cross-tenant access: %d %s", r.status, r.raw)
		}
	}
	csvBody := b.do("GET", "/api/v1/exports/shipments.csv", nil, nil)
	if strings.Contains(string(csvBody.raw), "Tenant A only") || strings.Contains(string(csvBody.raw), "CD-") {
		t.Fatal("tenant B export contains tenant A rows")
	}
	if r := a.do("GET", "/api/v1/shipments/not-a-uuid", nil, nil); r.status != 404 {
		t.Fatalf("malformed id: %d", r.status)
	}
}

func TestIdempotencyKey(t *testing.T) {
	e := newEnv(t, true)
	c := e.login(seed.CoordinatorA)
	h := map[string]string{"Idempotency-Key": "create-123"}
	first := c.do("POST", "/api/v1/shipments", createBody("Idem"), h)
	second := c.do("POST", "/api/v1/shipments", createBody("Idem"), h)
	if first.status != 201 || second.status != 201 || first.body["id"] != second.body["id"] || second.header.Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay: %d %d %v %v", first.status, second.status, first.body["id"], second.body["id"])
	}
	if r := c.do("POST", "/api/v1/shipments", createBody("Different"), h); r.status != 422 || r.body["error"] != "idempotency_key_reused" {
		t.Fatalf("reused key: %d %s", r.status, r.raw)
	}
	items := c.mustOK(200, "GET", "/api/v1/shipments?q=Idem", nil)["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("shipments created = %d, want 1", len(items))
	}
	// Keys are per user: another user reusing the key gets a new shipment.
	other := e.login(seed.Coordinator2)
	if r := other.do("POST", "/api/v1/shipments", createBody("Idem"), h); r.status != 201 || r.body["id"] == first.body["id"] {
		t.Fatalf("other user: %d", r.status)
	}
	p := "/api/v1/shipments/" + first.body["id"].(string) + "/transitions"
	th := map[string]string{"Idempotency-Key": "tr-1"}
	body := map[string]any{"version": 1, "to": "needs_info", "reason": "double click"}
	r1, r2 := c.do("POST", p, body, th), c.do("POST", p, body, th)
	if r1.status != 200 || r2.status != 200 || ver(r2.body) != 2 {
		t.Fatalf("transition replay: %d %d %s", r1.status, r2.status, r2.raw)
	}
}

func TestNotesExceptionsAndXSSPayloadsAreData(t *testing.T) {
	e := newEnv(t, true)
	c := e.login(seed.CoordinatorA)
	sh := c.mustOK(201, "POST", "/api/v1/shipments", createBody("Notes"))
	p := "/api/v1/shipments/" + sh["id"].(string)
	xss := `<img src=x onerror="alert(1)"><script>alert(2)</script>`
	r := c.do("POST", p+"/events", map[string]any{"kind": "note", "body": xss}, nil)
	if r.status != 201 || bytes.Contains(r.raw, []byte("<script>")) {
		t.Fatalf("note: %d %s", r.status, r.raw)
	}
	evs := c.mustOK(200, "GET", p+"/events", nil)["events"].([]any)
	last := evs[len(evs)-1].(map[string]any)
	if last["action"] != "note_added" || last["data"].(map[string]any)["body"] != xss {
		t.Fatalf("note event %v", last)
	}
	ex := c.mustOK(201, "POST", p+"/events", map[string]any{"kind": "exception", "version": ver(sh), "exception_kind": "delay",
		"reason": "Road closure", "follow_up_at": testdb.Now.Add(3 * time.Hour), "owner_id": seed.Coordinator2})["shipment"].(map[string]any)
	if ex["open_exceptions"].(float64) != 1 || ex["owner"].(map[string]any)["id"] != seed.Coordinator2 {
		t.Fatalf("exception %v", ex)
	}
	if r := c.do("POST", p+"/events", map[string]any{"kind": "exception", "version": ver(ex), "exception_kind": "delay", "reason": "x"}, nil); r.status != 422 {
		t.Fatalf("exception without follow-up: %d", r.status)
	}
}

func TestPayloadLimitsAndContentType(t *testing.T) {
	e := newEnv(t, true)
	c := e.login(seed.CoordinatorA)
	big := createBody("Big")
	big["notes"] = strings.Repeat("a", httpapi.MaxBodyBytes)
	if r := c.do("POST", "/api/v1/shipments", big, nil); r.status != 413 {
		t.Fatalf("large payload: %d", r.status)
	}
	long := createBody("Long")
	long["notes"] = strings.Repeat("a", 2001)
	if r := c.do("POST", "/api/v1/shipments", long, nil); r.status != 422 {
		t.Fatalf("long notes: %d", r.status)
	}
	if r := c.do("POST", "/api/v1/shipments", `{"account_name":"x","unknown":1}`, nil); r.status != 400 {
		t.Fatalf("unknown field: %d", r.status)
	}
	if r := c.do("POST", "/api/v1/shipments", `{`, nil); r.status != 400 {
		t.Fatalf("bad json: %d", r.status)
	}
	if r := c.do("POST", "/api/v1/shipments", nil, map[string]string{"Content-Type": "text/plain"}); r.status != 415 {
		t.Fatalf("form-style post: %d", r.status)
	}
	bad := createBody("Bad")
	bad["required_date"] = "2026-02-30"
	bad["cargo_type"] = "plutonium"
	r := c.do("POST", "/api/v1/shipments", bad, nil)
	if r.status != 422 || len(r.body["fields"].([]any)) != 2 {
		t.Fatalf("validation: %d %s", r.status, r.raw)
	}
}

func TestCSVExport(t *testing.T) {
	e := newEnv(t, true)
	c := e.login(seed.CoordinatorA)
	body := createBody(`=HYPERLINK("http://evil.invalid","x")`)
	body["notes"] = "internal note must not be exported"
	c.mustOK(201, "POST", "/api/v1/shipments", body)
	r := c.do("GET", "/api/v1/exports/shipments.csv?q=HYPERLINK", nil, nil)
	if r.status != 200 || !strings.HasPrefix(r.header.Get("Content-Type"), "text/csv") ||
		!strings.Contains(r.header.Get("Content-Disposition"), "attachment") {
		t.Fatalf("export: %d %v", r.status, r.header)
	}
	rows, err := csv.NewReader(bytes.NewReader(r.raw)).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d", len(rows))
	}
	if strings.Join(rows[0], ",") != "reference,state,priority,owner,account_name,origin,destination,cargo_type,required_date,next_action_at,due_at,created_at" {
		t.Fatalf("header %v", rows[0])
	}
	if rows[1][4] != `'=HYPERLINK("http://evil.invalid","x")` {
		t.Fatalf("account cell %q", rows[1][4])
	}
	if strings.Contains(string(r.raw), "internal note") {
		t.Fatal("notes leaked into export")
	}
	all := c.do("GET", "/api/v1/exports/shipments.csv", nil, nil)
	if n := bytes.Count(all.raw, []byte("\n")); n != 10 {
		t.Fatalf("full export lines = %d, want header + 9", n)
	}
}
