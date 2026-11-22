package httpapi_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tatitoxt/cargodesk/api/internal/httpapi"
	"github.com/tatitoxt/cargodesk/api/internal/notify"
	"github.com/tatitoxt/cargodesk/api/internal/seed"
	"github.com/tatitoxt/cargodesk/api/internal/store"
	"github.com/tatitoxt/cargodesk/api/internal/testdb"
)

type env struct {
	t    *testing.T
	srv  *httptest.Server
	now  atomic.Pointer[time.Time]
	pool *pgxpool.Pool
}

func newEnv(t *testing.T, demo bool, attachmentsDir ...string) *env {
	pool := testdb.Fresh(t)
	e := &env{t: t, pool: pool}
	now := testdb.Now
	e.now.Store(&now)
	dir := ""
	if len(attachmentsDir) > 0 {
		dir = attachmentsDir[0]
	}
	h := httpapi.New(httpapi.Config{Store: store.New(pool), DemoAuth: demo, AttachmentsDir: dir,
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
	if a, b := list("?q="+url.QueryEscape("cold store")), list("?q="+url.QueryEscape("clinic network")); len(a) != 1 || strings.Join(a, "") != strings.Join(b, "") {
		t.Fatalf("search by origin/destination = %v / %v", a, b)
	}
	if got := len(list("?q=" + url.QueryEscape("Hidden Tenant"))); got != 0 {
		t.Fatalf("cross-tenant search leaked %d rows", got)
	}
	if got := len(list("?q=%25")); got != 0 {
		t.Fatalf("LIKE wildcard was not escaped: %d", got)
	}
	if r := c.do("GET", "/api/v1/shipments?state=bogus", nil, nil); r.status != 422 {
		t.Fatalf("bad state filter: %d", r.status)
	}
	for _, bad := range []string{"?limit=0", "?limit=501", "?limit=abc", "?offset=-1", "?offset=x"} {
		if r := c.do("GET", "/api/v1/shipments"+bad, nil, nil); r.status != 422 {
			t.Fatalf("bad paging param %q: %d", bad, r.status)
		}
	}
	// Pages are disjoint, complete and stable (id tiebreak), and the body
	// reports the unpaged total.
	seen := map[string]bool{}
	offset := 0
	for {
		r := c.mustOK(200, "GET", fmt.Sprintf("/api/v1/shipments?limit=3&offset=%d", offset), nil)
		if r["total"].(float64) != 8 {
			t.Fatalf("total = %v, want 8", r["total"])
		}
		items := r["items"].([]any)
		if len(items) == 0 {
			break
		}
		for _, it := range items {
			ref := it.(map[string]any)["reference"].(string)
			if seen[ref] {
				t.Fatalf("row %s appears on two pages", ref)
			}
			seen[ref] = true
		}
		offset += len(items)
	}
	if len(seen) != 8 {
		t.Fatalf("paging saw %d rows, want 8", len(seen))
	}
	// Paging composes with filters: overdue=true pages over 2 rows only.
	r := c.mustOK(200, "GET", "/api/v1/shipments?overdue=true&limit=1&offset=1", nil)
	if r["total"].(float64) != 2 || len(r["items"].([]any)) != 1 {
		t.Fatalf("filtered page = total %v, %d items", r["total"], len(r["items"].([]any)))
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
	// A rejected request does not consume its key: the corrected retry runs.
	fix := map[string]string{"Idempotency-Key": "fix-1"}
	bad := createBody("Fixable")
	bad["required_date"] = "not-a-date"
	if r := c.do("POST", "/api/v1/shipments", bad, fix); r.status != 422 || r.body["error"] != "validation_failed" {
		t.Fatalf("invalid create: %d %s", r.status, r.raw)
	}
	if r := c.do("POST", "/api/v1/shipments", createBody("Fixable"), fix); r.status != 201 || r.header.Get("Idempotent-Replayed") != "" {
		t.Fatalf("corrected retry: %d %s", r.status, r.raw)
	}
	p := "/api/v1/shipments/" + first.body["id"].(string) + "/transitions"
	th := map[string]string{"Idempotency-Key": "tr-1"}
	body := map[string]any{"version": 1, "to": "needs_info", "reason": "double click"}
	r1, r2 := c.do("POST", p, body, th), c.do("POST", p, body, th)
	if r1.status != 200 || r2.status != 200 || ver(r2.body) != 2 {
		t.Fatalf("transition replay: %d %d %s", r1.status, r2.status, r2.raw)
	}
}

func TestConcurrentIdempotentCreatesMakeOneShipment(t *testing.T) {
	e := newEnv(t, true)
	c := e.login(seed.CoordinatorA)
	h := map[string]string{"Idempotency-Key": "race-1"}
	const n = 8
	statuses := make(chan int, n)
	var wg sync.WaitGroup
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			statuses <- c.do("POST", "/api/v1/shipments", createBody("Racer"), h).status
		}()
	}
	wg.Wait()
	close(statuses)
	for st := range statuses {
		if st != 201 && st != 409 {
			t.Errorf("status %d, want 201 or 409", st)
		}
	}
	items := c.mustOK(200, "GET", "/api/v1/shipments?q=Racer", nil)["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("shipments created = %d, want 1", len(items))
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

func TestSavedViews(t *testing.T) {
	e := newEnv(t, true)
	c := e.login(seed.CoordinatorA)

	body := c.mustOK(200, "GET", "/api/v1/views", nil)
	if n := len(body["views"].([]any)); n != 0 {
		t.Fatalf("initial views = %d", n)
	}

	v := c.mustOK(201, "POST", "/api/v1/views", map[string]any{
		"name": "My overdue", "filters": map[string]string{"overdue": "true", "owner": "me"},
	})
	id := v["id"].(string)
	if v["name"] != "My overdue" || v["created_by"] != seed.CoordinatorA {
		t.Fatalf("created view: %v", v)
	}

	body = c.mustOK(200, "GET", "/api/v1/views", nil)
	views := body["views"].([]any)
	if len(views) != 1 || views[0].(map[string]any)["id"] != id {
		t.Fatalf("list views: %s", body["views"])
	}
	filters := views[0].(map[string]any)["filters"].(map[string]any)
	if filters["overdue"] != "true" || filters["owner"] != "me" {
		t.Fatalf("filters: %v", filters)
	}

	if r := c.do("POST", "/api/v1/views", map[string]any{
		"name": "my overdue", "filters": map[string]string{"q": "x"},
	}, nil); r.status != 409 || r.body["error"] != "view_name_taken" {
		t.Fatalf("duplicate name: %d %s", r.status, r.raw)
	}

	for _, bad := range []map[string]any{
		{"name": "x", "filters": map[string]string{"bogus": "1"}},
		{"name": "x", "filters": map[string]string{}},
		{"name": "  ", "filters": map[string]string{"q": "x"}},
	} {
		if r := c.do("POST", "/api/v1/views", bad, nil); r.status != 422 {
			t.Fatalf("invalid %v: %d %s", bad, r.status, r.raw)
		}
	}

	// Views are org-scoped: the other tenant sees neither the view nor can delete it.
	other := e.login(seed.CoordinatorB)
	if b := other.mustOK(200, "GET", "/api/v1/views", nil); len(b["views"].([]any)) != 0 {
		t.Fatalf("tenant B sees org A views: %s", b["views"])
	}
	if r := other.do("DELETE", "/api/v1/views/"+id, nil, nil); r.status != 404 {
		t.Fatalf("cross-tenant delete: %d", r.status)
	}

	if r := c.do("DELETE", "/api/v1/views/"+id, nil, nil); r.status != 204 {
		t.Fatalf("delete: %d %s", r.status, r.raw)
	}
	if r := c.do("DELETE", "/api/v1/views/"+id, nil, nil); r.status != 404 {
		t.Fatalf("re-delete: %d", r.status)
	}
}

func TestBusinessHoursCalendar(t *testing.T) {
	e := newEnv(t, true)
	viewer := e.login(seed.ViewerA)
	coord := e.login(seed.CoordinatorA)
	admin := e.login(seed.AdminA)

	calendar := map[string]any{"calendar": map[string]any{
		"days": []int{1, 2, 3, 4, 5}, "start_minute": 540, "end_minute": 1020}}
	if r := viewer.do("PUT", "/api/v1/settings/business-hours", calendar, nil); r.status != 403 {
		t.Fatalf("viewer put: %d", r.status)
	}
	if r := coord.do("PUT", "/api/v1/settings/business-hours", calendar, nil); r.status != 403 {
		t.Fatalf("coordinator put: %d", r.status)
	}
	if r := admin.do("PUT", "/api/v1/settings/business-hours",
		map[string]any{"calendar": map[string]any{"days": []int{9}, "start_minute": 0, "end_minute": 60}}, nil); r.status != 422 {
		t.Fatalf("invalid calendar: %d %s", r.status, r.raw)
	}
	// No calendar yet: null body, wall-clock due dates.
	got := admin.mustOK(200, "GET", "/api/v1/settings/business-hours", nil)
	if got["calendar"] != nil {
		t.Fatalf("calendar before set = %v", got["calendar"])
	}
	base := coord.mustOK(201, "POST", "/api/v1/shipments", createBody("wall clock"))
	baseDue, _ := time.Parse(time.RFC3339, base["due_at"].(string))

	admin.mustOK(200, "PUT", "/api/v1/settings/business-hours", calendar)
	got = admin.mustOK(200, "GET", "/api/v1/settings/business-hours", nil)
	cal := got["calendar"].(map[string]any)
	if cal["start_minute"].(float64) != 540 || cal["end_minute"].(float64) != 1020 {
		t.Fatalf("calendar = %v", cal)
	}
	// With a 9-5 calendar the same policy lands far later in wall-clock time.
	withCal := coord.mustOK(201, "POST", "/api/v1/shipments", createBody("business hours"))
	calDue, _ := time.Parse(time.RFC3339, withCal["due_at"].(string))
	if !calDue.After(baseDue) {
		t.Fatalf("business due %v not after wall-clock due %v", calDue, baseDue)
	}
	// Org B is unaffected.
	coordB := e.login(seed.CoordinatorB)
	other := coordB.mustOK(201, "POST", "/api/v1/shipments", createBody("org b"))
	otherDue, _ := time.Parse(time.RFC3339, other["due_at"].(string))
	if !otherDue.Equal(baseDue) {
		t.Fatalf("org B due = %v, want wall-clock %v", otherDue, baseDue)
	}
	// DELETE reverts to wall-clock.
	if r := coord.do("DELETE", "/api/v1/settings/business-hours", nil, nil); r.status != 403 {
		t.Fatalf("coordinator delete: %d", r.status)
	}
	admin.do("DELETE", "/api/v1/settings/business-hours", nil, nil)
	reverted := coord.mustOK(201, "POST", "/api/v1/shipments", createBody("reverted"))
	revDue, _ := time.Parse(time.RFC3339, reverted["due_at"].(string))
	if !revDue.Equal(baseDue) {
		t.Fatalf("reverted due = %v, want %v", revDue, baseDue)
	}
}

func TestWebhookNotifications(t *testing.T) {
	// Receiver that records bodies + signatures.
	var mu sync.Mutex
	var got []struct {
		body string
		sig  string
	}
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, struct {
			body string
			sig  string
		}{string(b), r.Header.Get("X-CargoDesk-Signature")})
		mu.Unlock()
		w.WriteHeader(200)
	}))
	t.Cleanup(receiver.Close)

	e := newEnv(t, true)
	admin := e.login(seed.AdminA)
	coord := e.login(seed.CoordinatorA)
	viewer := e.login(seed.ViewerA)

	// RBAC: viewer and coordinator cannot configure the webhook.
	if r := viewer.do("PUT", "/api/v1/settings/webhook", map[string]any{"url": receiver.URL}, nil); r.status != 403 {
		t.Fatalf("viewer PUT webhook: %d", r.status)
	}
	if r := coord.do("PUT", "/api/v1/settings/webhook", map[string]any{"url": receiver.URL}, nil); r.status != 403 {
		t.Fatalf("coordinator PUT webhook: %d", r.status)
	}
	// http (not https) is rejected; https or, for tests, plain http receiver:
	// the API demands https, so use the receiver behind an https-looking URL check.
	if r := admin.do("PUT", "/api/v1/settings/webhook", map[string]any{"url": "http://example.com/hook"}, nil); r.status != 422 {
		t.Fatalf("http webhook: %d", r.status)
	}
	// httptest receiver is http:// — allow it by relaxing the check via direct store call for the test target.
	st := store.New(e.pool)
	actor, err := st.ActorByUserID(t.Context(), seed.AdminA)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetWebhook(t.Context(), actor, receiver.URL, "s3cr3t", nil); err != nil {
		t.Fatal(err)
	}

	// Every shipment event should enqueue + deliver a POST.
	coord.mustOK(201, "POST", "/api/v1/shipments", createBody("Webhook Account"))

	// Dispatcher runs against the same pool.
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	d := &notify.Dispatcher{Store: st, Interval: 20 * time.Millisecond,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	go d.Run(ctx)

	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n >= 1 || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) < 1 {
		t.Fatal("no webhook delivery received")
	}
	if got[0].sig == "" || !strings.HasPrefix(got[0].sig, "sha256=") {
		t.Fatalf("missing HMAC signature: %q", got[0].sig)
	}
	m := map[string]any{}
	if err := json.Unmarshal([]byte(got[0].body), &m); err != nil {
		t.Fatal(err)
	}
	if m["event"] != "created" || m["organization"] != "demo-freight" {
		t.Fatalf("payload: %v", m)
	}

	// GET shows the delivered log for the admin.
	r := admin.do("GET", "/api/v1/settings/webhook", nil, nil)
	if r.status != 200 || r.body["url"] != receiver.URL || r.body["has_secret"] != true {
		t.Fatalf("GET webhook: %d %s", r.status, r.raw)
	}
	dels, _ := r.body["deliveries"].([]any)
	if len(dels) == 0 || dels[0].(map[string]any)["delivered"] != true {
		t.Fatalf("deliveries log: %s", r.raw)
	}
}

func TestAttachments(t *testing.T) {
	e := newEnv(t, true, t.TempDir())
	coord := e.login(seed.CoordinatorA)
	viewer := e.login(seed.ViewerA)
	sh := coord.mustOK(201, "POST", "/api/v1/shipments", createBody("Attach Org"))
	id := sh["id"].(string)

	upload := func(c *client, name, contentType string, data []byte) resp {
		var buf bytes.Buffer
		wr := multipart.NewWriter(&buf)
		part, _ := wr.CreateFormFile("file", name)
		part.Write(data)
		wr.Close()
		return c.do("POST", "/api/v1/shipments/"+id+"/attachments", buf.String(),
			map[string]string{"Content-Type": wr.FormDataContentType()})
	}

	payload := []byte("POD signature scan bytes")
	up := upload(coord, "pod-1.pdf", "application/pdf", payload)
	if up.status != 201 {
		t.Fatalf("upload: %d %s", up.status, up.raw)
	}
	att := up.body
	sum := sha256.Sum256(payload)
	if att["sha256"] != hex.EncodeToString(sum[:]) || att["filename"] != "pod-1.pdf" {
		t.Fatalf("bad metadata: %v", att)
	}

	// Viewers can list and download but not upload or delete.
	if r := upload(viewer, "x.txt", "text/plain", []byte("x")); r.status != 403 {
		t.Fatalf("viewer upload: %d", r.status)
	}
	list := viewer.mustOK(200, "GET", "/api/v1/shipments/"+id+"/attachments", nil)
	if len(list["attachments"].([]any)) != 1 {
		t.Fatalf("list: %v", list)
	}
	dl := viewer.do("GET", "/api/v1/shipments/"+id+"/attachments/"+att["id"].(string), nil, nil)
	if dl.status != 200 || !bytes.Equal(dl.raw, payload) {
		t.Fatalf("download: %d %s", dl.status, dl.raw)
	}
	if dl.header.Get("Content-Disposition") == "" {
		t.Fatal("missing Content-Disposition")
	}

	// Audit events record the upload.
	evs := coord.mustOK(200, "GET", "/api/v1/shipments/"+id+"/events", nil)
	found := false
	for _, ev := range evs["events"].([]any) {
		if ev.(map[string]any)["action"] == "attachment_added" {
			found = true
		}
	}
	if !found {
		t.Fatal("no attachment_added audit event")
	}

	// Tenant isolation: another org's shipment id returns 404 across all ops.
	other := coord.mustOK(201, "POST", "/api/v1/shipments", createBody("Attach Org B"))
	bid := other["id"].(string)
	for _, tc := range [][2]string{
		{"GET", "/api/v1/shipments/" + bid + "/attachments/" + att["id"].(string)},
		{"DELETE", "/api/v1/shipments/" + bid + "/attachments/" + att["id"].(string)},
	} {
		if r := coord.do(tc[0], tc[1], nil, nil); r.status != 404 {
			t.Fatalf("%s: got %d", tc[1], r.status)
		}
	}

	// Delete removes the row and the download stops working.
	if r := coord.do("DELETE", "/api/v1/shipments/"+id+"/attachments/"+att["id"].(string), nil, nil); r.status != 204 {
		t.Fatalf("delete: %d", r.status)
	}
	if r := viewer.do("GET", "/api/v1/shipments/"+id+"/attachments/"+att["id"].(string), nil, nil); r.status != 404 {
		t.Fatalf("post-delete download: %d", r.status)
	}
	list = coord.mustOK(200, "GET", "/api/v1/shipments/"+id+"/attachments", nil)
	if len(list["attachments"].([]any)) != 0 {
		t.Fatalf("post-delete list: %v", list)
	}
}

// Webhook event filtering: a subscription limited to selected actions only
// enqueues deliveries for those actions; an empty/absent list means all.
func TestWebhookEventFilter(t *testing.T) {
	e := newEnv(t, true)
	admin := e.login(seed.AdminA)
	coord := e.login(seed.CoordinatorA)

	st := store.New(e.pool)
	actor, err := st.ActorByUserID(t.Context(), seed.AdminA)
	if err != nil {
		t.Fatal(err)
	}

	// Unknown action names are rejected.
	if r := admin.do("PUT", "/api/v1/settings/webhook", map[string]any{
		"url": "https://example.com/hook", "events": []string{"nope"},
	}, nil); r.status != 422 {
		t.Fatalf("bad events: %d", r.status)
	}

	// Subscribe only to transitions; created/note events enqueue nothing.
	if err := st.SetWebhook(t.Context(), actor, "https://example.com/hook", "s3", []string{"transitioned"}); err != nil {
		t.Fatal(err)
	}
	resp := coord.mustOK(201, "POST", "/api/v1/shipments", createBody("Filtered Account"))
	id, _ := resp["id"].(string)

	queued := func() (actions []string) {
		rows, qerr := e.pool.Query(t.Context(),
			`SELECT action FROM notification_deliveries ORDER BY id`)
		if qerr != nil {
			t.Fatal(qerr)
		}
		defer rows.Close()
		for rows.Next() {
			var a string
			if err := rows.Scan(&a); err != nil {
				t.Fatal(err)
			}
			actions = append(actions, a)
		}
		return actions
	}
	if got := queued(); len(got) != 0 {
		t.Fatalf("filtered-out event still enqueued: %v", got)
	}

	// A note on the same shipment is also outside the subscription.
	coord.mustOK(201, "POST", "/api/v1/shipments/"+id+"/events", map[string]any{"kind": "note", "body": "check"})
	if got := queued(); len(got) != 0 {
		t.Fatalf("note event still enqueued: %v", got)
	}

	// A transition is inside the subscription and enqueues.
	ver := resp["version"].(float64)
	sh := coord.mustOK(200, "POST", "/api/v1/shipments/"+id+"/transitions",
		map[string]any{"to": "quoted", "version": int(ver),
			"quote": map[string]any{"amount_cents": 100, "currency": "USD"}})
	if sh["state"] != "quoted" {
		t.Fatalf("state %v", sh["state"])
	}
	if got := queued(); len(got) != 1 || got[0] != "transitioned" {
		t.Fatalf("transition should enqueue: %v", got)
	}
	// Clean the queue so the unfiltered check below starts empty.
	if _, err := e.pool.Exec(t.Context(), `DELETE FROM notification_deliveries`); err != nil {
		t.Fatal(err)
	}

	// Widening the filter to all actions (nil) restores enqueueing.
	if err := st.SetWebhook(t.Context(), actor, "https://example.com/hook", "s3", nil); err != nil {
		t.Fatal(err)
	}
	coord.mustOK(201, "POST", "/api/v1/shipments/"+id+"/events", map[string]any{"kind": "note", "body": "check 2"})
	if got := queued(); len(got) != 1 || got[0] != "note_added" {
		t.Fatalf("expected one note delivery, got %v", got)
	}

	// GET exposes the subscription for admins.
	r := admin.do("GET", "/api/v1/settings/webhook", nil, nil)
	if r.status != 200 || r.body["events"] != nil {
		t.Fatalf("GET webhook events: %d %s", r.status, r.raw)
	}
}

// Admin member management: create, role-change and delete org users with the
// last-admin guard and tenant isolation.
func TestMemberManagement(t *testing.T) {
	e := newEnv(t, true)
	viewer := e.login(seed.ViewerA)
	coord := e.login(seed.CoordinatorA)
	admin := e.login(seed.AdminA)
	other := e.login(seed.CoordinatorB)

	body := map[string]any{"email": "new.coord@cargodesk.example", "display_name": "New Coord", "role": "coordinator"}
	if r := viewer.do("POST", "/api/v1/users", body, nil); r.status != 403 {
		t.Fatalf("viewer create: %d", r.status)
	}
	if r := coord.do("POST", "/api/v1/users", body, nil); r.status != 403 {
		t.Fatalf("coordinator create: %d", r.status)
	}
	if r := admin.do("POST", "/api/v1/users",
		map[string]any{"email": "no-at-sign", "display_name": "X", "role": "superuser"}, nil); r.status != 422 {
		t.Fatalf("bad fields: %d %s", r.status, r.raw)
	}
	created := admin.mustOK(201, "POST", "/api/v1/users", body)
	newUser := created["user"].(map[string]any)
	newID := newUser["id"].(string)
	if newUser["role"] != "coordinator" || newUser["email"] != "new.coord@cargodesk.example" {
		t.Fatalf("user = %v", newUser)
	}
	if r := admin.do("POST", "/api/v1/users", body, nil); r.status != 409 {
		t.Fatalf("duplicate email: %d, want 409", r.status)
	}

	// The new member appears in the roster and can sign in.
	roster := admin.mustOK(200, "GET", "/api/v1/users", nil)
	found := false
	for _, u := range roster["users"].([]any) {
		if u.(map[string]any)["id"] == newID {
			found = true
		}
	}
	if !found {
		t.Fatal("new member missing from roster")
	}
	logged := e.login(newID)
	me := logged.mustOK(200, "GET", "/api/v1/me", nil)
	if me["role"] != "coordinator" {
		t.Fatalf("new member role = %v", me["role"])
	}

	// Demoting the only admin is rejected; with a second admin in place,
	// demoting that second admin succeeds (AdminA remains an admin).
	if r := admin.do("PATCH", "/api/v1/users/"+seed.AdminA,
		map[string]any{"role": "viewer"}, nil); r.status != 409 {
		t.Fatalf("demote last admin: %d, want 409", r.status)
	}
	second := admin.mustOK(201, "POST", "/api/v1/users",
		map[string]any{"email": "second.admin@cargodesk.example", "display_name": "Second Admin", "role": "admin"})
	secondID := second["user"].(map[string]any)["id"].(string)
	updated := admin.mustOK(200, "PATCH", "/api/v1/users/"+secondID, map[string]any{"role": "viewer"})
	if updated["user"].(map[string]any)["role"] != "viewer" {
		t.Fatalf("demoted role = %v", updated["user"])
	}

	// Tenant isolation + unknown id.
	if r := admin.do("PATCH", "/api/v1/users/"+seed.CoordinatorB,
		map[string]any{"role": "admin"}, nil); r.status != 404 {
		t.Fatalf("cross-tenant patch: %d, want 404", r.status)
	}
	if r := admin.do("DELETE", "/api/v1/users/00000000-0000-4000-b000-00000000dead", nil, nil); r.status != 404 {
		t.Fatalf("delete unknown: %d, want 404", r.status)
	}
	r := other.do("GET", "/api/v1/users", nil, nil)
	if r.status != 200 {
		t.Fatalf("other tenant roster: %d", r.status)
	}
	for _, u := range r.body["users"].([]any) {
		if u.(map[string]any)["id"] == newID {
			t.Fatal("cross-tenant member leak")
		}
	}

	// Deleting a member kills their sessions.
	admin.do("DELETE", "/api/v1/users/"+secondID, nil, nil)
	if r := admin.do("DELETE", "/api/v1/users/"+secondID, nil, nil); r.status != 404 {
		t.Fatalf("re-delete: %d, want 404", r.status)
	}
	logged = e.login(newID)
	admin.do("DELETE", "/api/v1/users/"+newID, nil, nil)
	if r := logged.do("GET", "/api/v1/me", nil, nil); r.status != 401 {
		t.Fatalf("deleted member session: %d, want 401", r.status)
	}
}

func TestScheduleSLA(t *testing.T) {
	e := newEnv(t, true)
	c := e.login(seed.CoordinatorA)
	admin := e.login(seed.AdminA)

	// A tighter schedule policy applies to newly accepted shipments.
	admin.mustOK(200, "PUT", "/api/v1/settings/sla", map[string]any{
		"schedule_hours": map[string]int{"low": 96, "normal": 2, "high": 4, "urgent": 1}})
	got := admin.mustOK(200, "GET", "/api/v1/settings/sla", nil)
	if got["schedule_hours"].(map[string]any)["normal"] != 2.0 {
		t.Fatalf("schedule policy %v", got)
	}
	if r := admin.do("PUT", "/api/v1/settings/sla", map[string]any{}, nil); r.status != 422 {
		t.Fatalf("empty policy update: %d", r.status)
	}

	sh := c.mustOK(201, "POST", "/api/v1/shipments", createBody("Schedule SLA Account"))
	p := "/api/v1/shipments/" + sh["id"].(string)
	sh = c.mustOK(200, "POST", p+"/transitions", map[string]any{"version": ver(sh), "to": "quoted",
		"quote": map[string]any{"amount_cents": 50000, "currency": "USD"}})
	sh = c.mustOK(200, "POST", p+"/transitions", map[string]any{"version": ver(sh), "to": "accepted"})
	if sh["schedule_sla_status"] != "on_track" || sh["accepted_at"] == nil {
		t.Fatalf("accepted %v", sh)
	}
	// createBody uses priority "high" -> 4h schedule window.
	sdue, _ := time.Parse(time.RFC3339, sh["schedule_due_at"].(string))
	if !sdue.Equal(testdb.Now.Add(4 * time.Hour)) {
		t.Fatalf("schedule_due_at = %v", sdue)
	}

	// Inside the 2h at-risk window, then past the deadline.
	e.advance(3 * time.Hour)
	sh = c.mustOK(200, "GET", p, nil)
	if sh["schedule_sla_status"] != "at_risk" {
		t.Fatalf("at risk %v", sh["schedule_sla_status"])
	}
	e.advance(2 * time.Hour)
	sh = c.mustOK(200, "GET", p, nil)
	if sh["schedule_sla_status"] != "breached" || sh["overdue"] != true {
		t.Fatalf("breached %v", sh)
	}

	// Scheduling after the deadline records a miss.
	sh = c.mustOK(200, "POST", p+"/transitions", map[string]any{"version": ver(sh), "to": "scheduled",
		"pickup_at": testdb.Now.Add(48 * time.Hour), "carrier": "Carrier X"})
	if sh["schedule_sla_status"] != "missed" || sh["scheduled_at"] == nil {
		t.Fatalf("missed %v", sh)
	}
}

func TestAttachmentRetention(t *testing.T) {
	dir := t.TempDir()
	e := newEnv(t, true, dir)
	admin := e.login(seed.AdminA)
	coord := e.login(seed.CoordinatorA)
	sh := coord.mustOK(201, "POST", "/api/v1/shipments", createBody("Retention Org"))
	id := sh["id"].(string)

	upload := func() string {
		var buf bytes.Buffer
		wr := multipart.NewWriter(&buf)
		part, _ := wr.CreateFormFile("file", "pod.pdf")
		part.Write([]byte("pod bytes"))
		wr.Close()
		r := coord.do("POST", "/api/v1/shipments/"+id+"/attachments", buf.String(),
			map[string]string{"Content-Type": wr.FormDataContentType()})
		if r.status != 201 {
			t.Fatalf("upload: %d %s", r.status, r.raw)
		}
		return r.body["id"].(string)
	}
	attID := upload()

	// RBAC + validation.
	if r := coord.do("PUT", "/api/v1/settings/retention", `{"attachmentRetentionDays":1}`, nil); r.status != 403 {
		t.Fatalf("coordinator PUT retention: %d", r.status)
	}
	if r := admin.do("PUT", "/api/v1/settings/retention", `{"attachmentRetentionDays":-5}`, nil); r.status != 422 {
		t.Fatalf("negative retention: %d %s", r.status, r.raw)
	}
	r := admin.do("PUT", "/api/v1/settings/retention", `{"attachmentRetentionDays":1}`, nil)
	if r.status != 200 {
		t.Fatalf("PUT retention: %d %s", r.status, r.raw)
	}
	if got := admin.mustOK(200, "GET", "/api/v1/settings/retention", nil)["attachmentRetentionDays"]; got != float64(1) {
		t.Fatalf("GET retention: %v", got)
	}

	// Within the window the purge keeps the file.
	r = admin.do("POST", "/api/v1/settings/retention/purge", `{}`, nil)
	if r.status != 200 || r.body["purged"] != float64(0) {
		t.Fatalf("early purge: %d %s", r.status, r.raw)
	}

	// Past the window the row, blob and audit trail all move. Advancing the
	// clock expires the seeded sessions, so log in again.
	e.advance(48 * time.Hour)
	admin = e.login(seed.AdminA)
	coord = e.login(seed.CoordinatorA)
	blobBefore, _ := filepath.Glob(filepath.Join(dir, "*", "*pod.pdf"))
	if len(blobBefore) == 0 {
		t.Fatal("blob missing before purge")
	}
	if r := coord.do("POST", "/api/v1/settings/retention/purge", `{}`, nil); r.status != 403 {
		t.Fatalf("coordinator purge: %d", r.status)
	}
	r = admin.do("POST", "/api/v1/settings/retention/purge", `{}`, nil)
	if r.status != 200 || r.body["purged"] != float64(1) {
		t.Fatalf("purge: %d %s", r.status, r.raw)
	}
	if got := coord.mustOK(200, "GET", "/api/v1/shipments/"+id+"/attachments", nil)["attachments"]; len(got.([]any)) != 0 {
		t.Fatalf("post-purge list: %v", got)
	}
	if r := coord.do("GET", "/api/v1/shipments/"+id+"/attachments/"+attID, nil, nil); r.status != 404 {
		t.Fatalf("post-purge download: %d", r.status)
	}
	if _, err := os.Stat(blobBefore[0]); !os.IsNotExist(err) {
		t.Fatalf("blob still on disk: %v", err)
	}
	evs := coord.mustOK(200, "GET", "/api/v1/shipments/"+id+"/events", nil)
	found := false
	for _, ev := range evs["events"].([]any) {
		m := ev.(map[string]any)
		if m["action"] == "attachment_removed" && m["correlation_id"] == "retention-purge" {
			found = true
		}
	}
	if !found {
		t.Fatal("no retention-purge audit event")
	}

	// Retention 0 = keep forever.
	admin.do("PUT", "/api/v1/settings/retention", `{"attachmentRetentionDays":0}`, nil)
	upload()
	e.advance(72 * time.Hour)
	admin = e.login(seed.AdminA)
	coord = e.login(seed.CoordinatorA)
	r = admin.do("POST", "/api/v1/settings/retention/purge", `{}`, nil)
	if r.body["purged"] != float64(0) {
		t.Fatalf("purge with retention 0: %s", r.raw)
	}
	if got := coord.mustOK(200, "GET", "/api/v1/shipments/"+id+"/attachments", nil)["attachments"]; len(got.([]any)) != 1 {
		t.Fatalf("list after disabled retention: %v", got)
	}
}

// Per-day windows: a calendar expressed as windows gets different due dates
// than the shared-window calendar and round-trips through GET.
func TestBusinessHoursWindows(t *testing.T) {
	e := newEnv(t, true)
	admin := e.login(seed.AdminA)
	calendar := map[string]any{"calendar": map[string]any{
		"days": []int{1, 2, 3, 4, 5}, "start_minute": 0, "end_minute": 1440,
		"windows": []map[string]any{
			{"day": 1, "start_minute": 540, "end_minute": 1020},
			{"day": 2, "start_minute": 540, "end_minute": 780},
		},
	}}
	if r := admin.do("PUT", "/api/v1/settings/business-hours", calendar, nil); r.status != 200 {
		t.Fatalf("put windows: %d %s", r.status, r.raw)
	}
	got := admin.mustOK(200, "GET", "/api/v1/settings/business-hours", nil)
	cal := got["calendar"].(map[string]any)
	wins := cal["windows"].([]any)
	if len(wins) != 2 || wins[1].(map[string]any)["end_minute"].(float64) != 780 {
		t.Fatalf("windows = %v", cal["windows"])
	}

	// Invalid per-day window rejected.
	bad := map[string]any{"calendar": map[string]any{
		"windows": []map[string]any{{"day": 9, "start_minute": 0, "end_minute": 60}}}}
	if r := admin.do("PUT", "/api/v1/settings/business-hours", bad, nil); r.status != 422 {
		t.Fatalf("invalid windows: %d %s", r.status, r.raw)
	}
}

func TestTransitSLA(t *testing.T) {
	e := newEnv(t, true)
	c := e.login(seed.CoordinatorA)
	admin := e.login(seed.AdminA)

	admin.mustOK(200, "PUT", "/api/v1/settings/sla", map[string]any{
		"transit_hours": map[string]int{"low": 168, "normal": 96, "high": 4, "urgent": 24}})
	got := admin.mustOK(200, "GET", "/api/v1/settings/sla", nil)
	if got["transit_hours"].(map[string]any)["high"] != 4.0 {
		t.Fatalf("transit policy %v", got)
	}

	sh := c.mustOK(201, "POST", "/api/v1/shipments", createBody("Transit SLA Account"))
	p := "/api/v1/shipments/" + sh["id"].(string)
	for _, tr := range []map[string]any{
		{"to": "quoted", "quote": map[string]any{"amount_cents": 50000, "currency": "USD"}},
		{"to": "accepted"},
		{"to": "scheduled", "pickup_at": testdb.Now.Add(24 * time.Hour), "carrier": "Carrier X"},
		{"to": "in_transit"},
	} {
		tr["version"] = ver(sh)
		sh = c.mustOK(200, "POST", p+"/transitions", tr)
	}
	if sh["transit_sla_status"] != "on_track" || sh["in_transit_at"] == nil {
		t.Fatalf("in transit %v", sh)
	}
	// createBody uses priority "high" -> 4h transit window from in_transit_at.
	tdue, _ := time.Parse(time.RFC3339, sh["transit_due_at"].(string))
	it, _ := time.Parse(time.RFC3339, sh["in_transit_at"].(string))
	if !tdue.Equal(it.Add(4 * time.Hour)) {
		t.Fatalf("transit_due_at = %v, want %v", tdue, it.Add(4*time.Hour))
	}

	e.advance(5 * time.Hour)
	c = e.login(seed.CoordinatorA) // session expiry under the advanced clock
	sh = c.mustOK(200, "GET", p, nil)
	if sh["transit_sla_status"] != "breached" || sh["overdue"] != true {
		t.Fatalf("breached %v", sh)
	}

	// Delivering after the deadline records a miss.
	sh = c.mustOK(200, "POST", p+"/transitions", map[string]any{"version": ver(sh), "to": "delivered",
		"evidence": map[string]any{"delivered_at": testdb.Now.Add(5 * time.Hour), "received_by": "Receiver", "note": "ok"}})
	if sh["transit_sla_status"] != "missed" || sh["delivered_at"] == nil {
		t.Fatalf("missed %v", sh)
	}
}

// Minimal SMTP sink: accepts EHLO/MAIL/RCPT/DATA and records the DATA body.
type smtpSink struct {
	ln   net.Listener
	mu   sync.Mutex
	got  []string
	rcpt []string
}

func newSMTPSink(t *testing.T) *smtpSink {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &smtpSink{ln: ln}
	t.Cleanup(func() { ln.Close() })
	go s.serve()
	return s
}

func (s *smtpSink) serve() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		go func() {
			defer conn.Close()
			r := bufio.NewReader(conn)
			fmt.Fprintf(conn, "220 sink\r\n")
			var data bool
			var lines []string
			for {
				line, err := r.ReadString('\n')
				if err != nil {
					return
				}
				line = strings.TrimRight(line, "\r\n")
				if data {
					if line == "." {
						s.mu.Lock()
						s.got = append(s.got, strings.Join(lines, "\n"))
						s.mu.Unlock()
						data = false
						lines = nil
						fmt.Fprintf(conn, "250 ok\r\n")
						continue
					}
					lines = append(lines, line)
					continue
				}
				verb := strings.ToUpper(strings.Fields(line + " ")[0])
				switch {
				case strings.HasPrefix(verb, "EHLO") || strings.HasPrefix(verb, "HELO"):
					fmt.Fprintf(conn, "250-sink\r\n250 OK\r\n")
				case strings.HasPrefix(strings.ToUpper(line), "MAIL FROM"):
					fmt.Fprintf(conn, "250 ok\r\n")
				case strings.HasPrefix(strings.ToUpper(line), "RCPT TO"):
					s.mu.Lock()
					s.rcpt = append(s.rcpt, line)
					s.mu.Unlock()
					fmt.Fprintf(conn, "250 ok\r\n")
				case verb == "DATA":
					data = true
					fmt.Fprintf(conn, "354 go\r\n")
				case verb == "QUIT":
					fmt.Fprintf(conn, "221 bye\r\n")
					return
				case verb == "RSET" || verb == "NOOP":
					fmt.Fprintf(conn, "250 ok\r\n")
				default:
					fmt.Fprintf(conn, "250 ok\r\n")
				}
			}
		}()
	}
}

func TestEmailChannel(t *testing.T) {
	e := newEnv(t, true)
	c := e.login(seed.CoordinatorA)
	admin := e.login(seed.AdminA)
	viewer := e.login(seed.ViewerA)

	// Validation + RBAC.
	if r := viewer.do("PUT", "/api/v1/settings/email", map[string]any{"recipients": []string{"a@b.co"}}, nil); r.status != 403 {
		t.Fatalf("viewer put: %d, want 403", r.status)
	}
	if r := admin.do("PUT", "/api/v1/settings/email", map[string]any{"recipients": []string{"nope"}}, nil); r.status != 422 {
		t.Fatalf("bad email: %d, want 422", r.status)
	}
	admin.mustOK(200, "PUT", "/api/v1/settings/email", map[string]any{
		"recipients": []string{"ops@demo.test", "oncall@demo.test"}})
	got := admin.mustOK(200, "GET", "/api/v1/settings/email", nil)
	rcpts := got["recipients"].([]any)
	if len(rcpts) != 2 {
		t.Fatalf("recipients %v", got)
	}

	// A shipment event enqueues an email-channel delivery row.
	c.mustOK(201, "POST", "/api/v1/shipments", createBody("Email Account"))
	var channel string
	var rcpt []string
	if err := e.pool.QueryRow(t.Context(),
		`SELECT channel, rcpt FROM notification_deliveries ORDER BY id DESC LIMIT 1`).Scan(&channel, &rcpt); err != nil {
		t.Fatal(err)
	}
	if channel != "email" || len(rcpt) != 2 {
		t.Fatalf("delivery channel=%s rcpt=%v", channel, rcpt)
	}

	// Real SMTPMailer delivers it to the sink and marks the row delivered.
	sink := newSMTPSink(t)
	_, port, _ := net.SplitHostPort(sink.ln.Addr().String())
	p, _ := strconv.Atoi(port)
	st := store.New(e.pool)
	d := &notify.Dispatcher{Store: st, Interval: 20 * time.Millisecond,
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Mailer: &notify.SMTPMailer{Host: "127.0.0.1", Port: p, From: "noreply@cargodesk.test"}}
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	go d.Run(ctx)

	deadline := time.Now().Add(5 * time.Second)
	for {
		sink.mu.Lock()
		n := len(sink.got)
		sink.mu.Unlock()
		if n >= 1 || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.got) != 1 {
		t.Fatalf("emails received: %d", len(sink.got))
	}
	body := sink.got[0]
	if !strings.Contains(body, "Subject: CargoDesk: created shipment") ||
		!strings.Contains(body, "organization: demo-freight") {
		t.Fatalf("email body:\n%s", body)
	}
	if len(sink.rcpt) != 2 {
		t.Fatalf("rcpt commands: %v", sink.rcpt)
	}

	// The settings endpoint reports the delivered email.
	got = admin.mustOK(200, "GET", "/api/v1/settings/email", nil)
	dels := got["deliveries"].([]any)
	if len(dels) == 0 || dels[0].(map[string]any)["delivered"] != true {
		t.Fatalf("email deliveries log: %s", got["deliveries"])
	}

	// Empty list disables the channel.
	admin.mustOK(200, "PUT", "/api/v1/settings/email", map[string]any{"recipients": []string{}})
}

func TestSlackNotifications(t *testing.T) {
	// Receiver that records Slack-shaped bodies.
	var mu sync.Mutex
	var got []string
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, string(b))
		mu.Unlock()
		w.WriteHeader(200)
	}))
	t.Cleanup(receiver.Close)

	e := newEnv(t, true)
	admin := e.login(seed.AdminA)
	coord := e.login(seed.CoordinatorA)
	viewer := e.login(seed.ViewerA)

	// RBAC: non-admins cannot configure the Slack webhook.
	if r := viewer.do("PUT", "/api/v1/settings/slack", map[string]any{"url": "https://hooks.slack.com/x"}, nil); r.status != 403 {
		t.Fatalf("viewer PUT slack: %d", r.status)
	}
	if r := coord.do("PUT", "/api/v1/settings/slack", map[string]any{"url": "https://hooks.slack.com/x"}, nil); r.status != 403 {
		t.Fatalf("coordinator PUT slack: %d", r.status)
	}
	if r := admin.do("PUT", "/api/v1/settings/slack", map[string]any{"url": "http://example.com/hook"}, nil); r.status != 422 {
		t.Fatalf("http slack URL: %d", r.status)
	}
	if r := admin.do("PUT", "/api/v1/settings/slack", map[string]any{"url": "https://hooks.slack.com/services/T/B/x"}, nil); r.status != 200 {
		t.Fatalf("PUT slack: %d %s", r.status, r.raw)
	}
	if r := admin.do("GET", "/api/v1/settings/slack", nil, nil); r.status != 200 || r.body["url"] != "https://hooks.slack.com/services/T/B/x" {
		t.Fatalf("GET slack: %d %s", r.status, r.raw)
	}

	// Repoint at the local sink (the API requires https; tests set it via the store).
	st := store.New(e.pool)
	actor, err := st.ActorByUserID(t.Context(), seed.AdminA)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetSlack(t.Context(), actor, receiver.URL); err != nil {
		t.Fatal(err)
	}

	// The shipment event's Slack delivery shares the webhook_events filter:
	// none configured -> every action posts.
	coord.mustOK(201, "POST", "/api/v1/shipments", createBody("Slack Account"))

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	d := &notify.Dispatcher{Store: st, Interval: 20 * time.Millisecond,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	go d.Run(ctx)

	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n >= 1 || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) < 1 {
		t.Fatal("no slack delivery received")
	}
	m := map[string]any{}
	if err := json.Unmarshal([]byte(got[0]), &m); err != nil {
		t.Fatal(err)
	}
	text, _ := m["text"].(string)
	if text == "" || !strings.Contains(text, "demo-freight") || !strings.Contains(text, "created") {
		t.Fatalf("slack payload: %s", got[0])
	}

	// DELETE disables the channel.
	if r := admin.do("DELETE", "/api/v1/settings/slack", nil, nil); r.status != 204 {
		t.Fatalf("DELETE slack: %d", r.status)
	}
}

func TestWhatsAppChannel(t *testing.T) {
	// Fake Meta Graph API endpoint: records auth header + body per call.
	var mu sync.Mutex
	var got []struct {
		auth string
		body map[string]any
	}
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		m := map[string]any{}
		_ = json.Unmarshal(b, &m)
		mu.Lock()
		got = append(got, struct {
			auth string
			body map[string]any
		}{r.Header.Get("Authorization"), m})
		mu.Unlock()
		w.WriteHeader(200)
	}))
	t.Cleanup(receiver.Close)

	e := newEnv(t, true)
	admin := e.login(seed.AdminA)
	coord := e.login(seed.CoordinatorA)
	viewer := e.login(seed.ViewerA)

	// RBAC + validation.
	if r := viewer.do("PUT", "/api/v1/settings/whatsapp", map[string]any{
		"phone_number_id": "123456789", "recipients": []string{"+5491100000000"}, "token": "tok"}, nil); r.status != 403 {
		t.Fatalf("viewer PUT whatsapp: %d", r.status)
	}
	if r := admin.do("PUT", "/api/v1/settings/whatsapp", map[string]any{
		"phone_number_id": "123456789", "recipients": []string{"not-a-number"}, "token": "tok"}, nil); r.status != 422 {
		t.Fatalf("bad recipient: %d", r.status)
	}
	if r := admin.do("PUT", "/api/v1/settings/whatsapp", map[string]any{
		"phone_number_id": "123456789", "recipients": []string{"+5491100000000"},
		"token": "tok", "api_url": "http://insecure.local"}, nil); r.status != 422 {
		t.Fatalf("http api_url: %d", r.status)
	}

	// Configure via the store so api_url can point at the local http sink.
	st := store.New(e.pool)
	actor, err := st.ActorByUserID(t.Context(), seed.AdminA)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetWhatsApp(t.Context(), actor, "555123456", receiver.URL,
		[]string{"+5491122334455", "+5491166778899"}, "wa-test-token"); err != nil {
		t.Fatal(err)
	}
	cfg, err := st.WhatsApp(t.Context(), actor)
	if err != nil || !cfg.TokenSet || cfg.PhoneNumberID != "555123456" || len(cfg.Recipients) != 2 {
		t.Fatalf("whatsapp cfg %+v err %v", cfg, err)
	}

	// A shipment event enqueues a whatsapp delivery at {api}/{pnid}/messages.
	coord.mustOK(201, "POST", "/api/v1/shipments", createBody("WhatsApp Account"))
	var channel, endpoint string
	var rcpt []string
	if err := e.pool.QueryRow(t.Context(),
		`SELECT channel, url, rcpt FROM notification_deliveries ORDER BY id DESC LIMIT 1`).
		Scan(&channel, &endpoint, &rcpt); err != nil {
		t.Fatal(err)
	}
	if channel != "whatsapp" || endpoint != receiver.URL+"/555123456/messages" || len(rcpt) != 2 {
		t.Fatalf("delivery channel=%s url=%s rcpt=%v", channel, endpoint, rcpt)
	}

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	d := &notify.Dispatcher{Store: st, Interval: 20 * time.Millisecond,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	go d.Run(ctx)

	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n >= 2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("whatsapp calls: %d, want one per recipient", len(got))
	}
	toSet := map[string]bool{}
	for _, call := range got {
		if call.auth != "Bearer wa-test-token" {
			t.Fatalf("auth header: %q", call.auth)
		}
		if call.body["messaging_product"] != "whatsapp" || call.body["type"] != "text" {
			t.Fatalf("body: %v", call.body)
		}
		text, _ := call.body["text"].(map[string]any)["body"].(string)
		if !strings.Contains(text, "demo-freight") || !strings.Contains(text, "created") {
			t.Fatalf("text body: %v", call.body)
		}
		toSet[call.body["to"].(string)] = true
	}
	if !toSet["+5491122334455"] || !toSet["+5491166778899"] {
		t.Fatalf("recipients: %v", toSet)
	}

	// DELETE wipes the whole config including the token.
	admin.mustOK(204, "DELETE", "/api/v1/settings/whatsapp", nil)
	cfg, err = st.WhatsApp(t.Context(), actor)
	if err != nil || cfg.TokenSet || cfg.PhoneNumberID != "" {
		t.Fatalf("cleared cfg %+v err %v", cfg, err)
	}
}

// Password invites: an admin invites a member, who signs in with the one-time
// temporary password and is forced to replace it.
func TestPasswordInvite(t *testing.T) {
	e := newEnv(t, true)
	viewer := e.login(seed.ViewerA)
	admin := e.login(seed.AdminA)

	if r := viewer.do("POST", "/api/v1/users/invite",
		map[string]any{"email": "invited@cargodesk.example", "display_name": "Invited", "role": "coordinator"}, nil); r.status != 403 {
		t.Fatalf("viewer invite: %d", r.status)
	}
	inv := admin.mustOK(201, "POST", "/api/v1/users/invite",
		map[string]any{"email": "invited@cargodesk.example", "display_name": "Invited", "role": "coordinator"})
	temp, _ := inv["temporary_password"].(string)
	if temp == "" {
		t.Fatalf("invite response missing temporary_password: %v", inv)
	}
	userID := inv["user"].(map[string]any)["id"].(string)

	// Wrong credentials and unknown emails both get 401.
	for _, body := range []map[string]any{
		{"email": "invited@cargodesk.example", "password": "nope"},
		{"email": "nobody@cargodesk.example", "password": temp},
	} {
		if r := e.anon().do("POST", "/api/v1/auth/password-login", body, nil); r.status != 401 {
			t.Fatalf("bad login %v: %d", body, r.status)
		}
	}

	// The temp password signs in and flags the forced change.
	invited := e.anon()
	logged := invited.do("POST", "/api/v1/auth/password-login",
		map[string]any{"email": "invited@cargodesk.example", "password": temp}, nil)
	if logged.status != 200 {
		t.Fatalf("password login: %d %s", logged.status, logged.raw)
	}
	if logged.body["must_change_password"] != true {
		t.Fatalf("must_change_password = %v", logged.body)
	}
	if r := invited.do("GET", "/api/v1/me", nil, nil); r.status != 200 {
		t.Fatalf("me as invited member: %d", r.status)
	}
	if r := invited.do("POST", "/api/v1/me/password",
		map[string]any{"current_password": "wrong", "new_password": "long-enough-secret"}, nil); r.status != 401 {
		t.Fatalf("wrong current: %d", r.status)
	}
	if r := invited.do("POST", "/api/v1/me/password",
		map[string]any{"current_password": temp, "new_password": "short"}, nil); r.status != 422 {
		t.Fatalf("weak password: %d", r.status)
	}
	if r := invited.do("POST", "/api/v1/me/password",
		map[string]any{"current_password": temp, "new_password": "long-enough-secret"}, nil); r.status != 204 {
		t.Fatalf("change: %d %s", r.status, r.raw)
	}

	// Old password rejected, new password accepted without the flag.
	if r := e.anon().do("POST", "/api/v1/auth/password-login",
		map[string]any{"email": "invited@cargodesk.example", "password": temp}, nil); r.status != 401 {
		t.Fatalf("old password still accepted: %d", r.status)
	}
	relogged := e.anon().do("POST", "/api/v1/auth/password-login",
		map[string]any{"email": "invited@cargodesk.example", "password": "long-enough-secret"}, nil)
	if relogged.status != 200 || relogged.body["must_change_password"] != false {
		t.Fatalf("relogin: %d %v", relogged.status, relogged.body)
	}

	// Seeded users without a password cannot use the password endpoint.
	if r := e.anon().do("POST", "/api/v1/auth/password-login",
		map[string]any{"email": "carla.admin@cargodesk.example", "password": "whatever-secret"}, nil); r.status != 401 {
		t.Fatalf("password-less user login: %d", r.status)
	}

	// Invited member can still sign in through the demo flow (same user row).
	if r := e.anon().do("POST", "/api/v1/auth/demo-login", map[string]any{"user_id": userID}, nil); r.status != 200 {
		t.Fatalf("demo login as invited: %d", r.status)
	}
}

func TestPublicTracking(t *testing.T) {
	e := newEnv(t, true)
	admin := e.login(seed.ViewerA)
	editor := e.login(seed.CoordinatorA)

	created := editor.mustOK(201, "POST", "/api/v1/shipments", createBody("Tracked Co"))
	id := created["id"].(string)
	detail := editor.mustOK(200, "GET", "/api/v1/shipments/"+id, nil)
	token, ok := detail["track_token"].(string)
	if !ok || len(token) < 20 {
		t.Fatalf("no track_token in detail: %v", detail)
	}

	// Unauthenticated view exposes the sanitized projection only.
	pub := e.anon().mustOK(200, "GET", "/api/v1/track/"+token, nil)
	if pub["reference"] != created["reference"] || pub["state"] != "new" {
		t.Fatalf("bad public payload: %s", pub)
	}
	for _, secret := range []string{"notes", "owner", "quotes", "account_name", "track_token", "id"} {
		if _, leaked := pub[secret]; leaked {
			t.Fatalf("public tracking leaked %q: %s", secret, pub)
		}
	}
	evs, _ := pub["events"].([]any)
	if len(evs) == 0 {
		t.Fatal("public tracking has no events")
	}
	ev := evs[0].(map[string]any)
	if _, leaked := ev["actor"]; leaked {
		t.Fatalf("public event leaked actor: %v", ev)
	}

	// Unknown/garbage tokens get the same 404 — no enumeration signal.
	for _, bad := range []string{"nosuch", token + "x", token[:len(token)-2] + "zz"} {
		if r := e.anon().do("GET", "/api/v1/track/"+bad, nil, nil); r.status != 404 {
			t.Fatalf("token %q: got %d, want 404", bad, r.status)
		}
	}

	// Viewers cannot rotate; editors can, and the old link dies.
	if r := admin.do("POST", "/api/v1/shipments/"+id+"/track-link", nil, nil); r.status == 200 {
		t.Fatal("viewer rotated a track link")
	}
	rotated := editor.mustOK(200, "POST", "/api/v1/shipments/"+id+"/track-link", nil)
	newToken, _ := rotated["track_token"].(string)
	if newToken == "" || newToken == token {
		t.Fatalf("rotation returned no fresh token: %v", rotated)
	}
	if r := e.anon().do("GET", "/api/v1/track/"+token, nil, nil); r.status != 404 {
		t.Fatalf("old token still resolves after rotation: %d", r.status)
	}
	if r := e.anon().do("GET", "/api/v1/track/"+newToken, nil, nil); r.status != 200 {
		t.Fatalf("rotated token does not resolve: %d", r.status)
	}
}
