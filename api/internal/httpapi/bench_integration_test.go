package httpapi_test

// Benchmarks run through the full HTTP API against real PostgreSQL —
// the same create/transition/list code paths the React app uses.
// Run: CARGODESK_TEST_DATABASE_URL=postgres://... go test -p 1 -run '^$' -bench . ./internal/httpapi/

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/tatitoxt/cargodesk/api/internal/httpapi"
	"github.com/tatitoxt/cargodesk/api/internal/seed"
	"github.com/tatitoxt/cargodesk/api/internal/store"
	"github.com/tatitoxt/cargodesk/api/internal/testdb"
)

type benchClient struct {
	srv  *httptest.Server
	http *http.Client
}

func newBenchClient(b *testing.B) *benchClient {
	pool := testdb.Fresh(b)
	now := testdb.Now
	h := httpapi.New(httpapi.Config{Store: store.New(pool), DemoAuth: true,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Clock: func() time.Time { return now }})
	srv := httptest.NewServer(h)
	b.Cleanup(srv.Close)
	jar, _ := cookiejar.New(nil)
	c := &benchClient{srv: srv, http: &http.Client{Jar: jar}}
	body, _ := json.Marshal(map[string]string{"user_id": seed.CoordinatorA})
	res, err := c.http.Post(srv.URL+"/api/v1/auth/demo-login", "application/json", bytes.NewReader(body))
	if err != nil || res.StatusCode != 200 {
		b.Fatalf("login: %v", err)
	}
	res.Body.Close()
	return c
}

func (c *benchClient) do(b *testing.B, method, path string, body any) (int, map[string]any) {
	var rd io.Reader
	if body != nil {
		buf, _ := json.Marshal(body)
		rd = bytes.NewReader(buf)
	}
	req, _ := http.NewRequest(method, c.srv.URL+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.http.Do(req)
	if err != nil {
		b.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	return res.StatusCode, out
}

// Throughput of the full create->deliver workflow: every op is a
// POST /shipments plus five happy-path transitions over HTTP + Postgres.
func BenchmarkShipmentLifecycle(b *testing.B) {
	c := newBenchClient(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		st, sh := c.do(b, "POST", "/api/v1/shipments", map[string]any{
			"account_name": fmt.Sprintf("Bench %d", i), "contact_label": "Bench contact",
			"origin": "A (demo)", "destination": "B (demo)",
			"cargo_type": "palletized", "required_date": "2026-03-20", "priority": "high",
		})
		if st != 201 {
			b.Fatalf("create: %d", st)
		}
		id, ver := sh["id"].(string), int(sh["version"].(float64))
		for _, body := range []map[string]any{
			{"to": "quoted", "quote": map[string]any{"amount_cents": 125000, "currency": "USD"}},
			{"to": "accepted"},
			{"to": "scheduled", "pickup_at": testdb.Now.Add(24 * time.Hour), "carrier": "Bench Carrier"},
			{"to": "in_transit"},
			{"to": "delivered", "evidence": map[string]any{"received_by": "Dock", "delivered_at": testdb.Now}},
		} {
			body["version"] = ver
			st, sh = c.do(b, "POST", fmt.Sprintf("/api/v1/shipments/%s/transitions", id), body)
			if st != 200 {
				b.Fatalf("transition %v: %d %v", body["to"], st, sh)
			}
			ver = int(sh["version"].(float64))
		}
	}
}

// Read-side throughput: the filtered board list after rows exist.
func BenchmarkListShipments(b *testing.B) {
	c := newBenchClient(b)
	for i := 0; i < 50; i++ {
		c.do(b, "POST", "/api/v1/shipments", map[string]any{
			"account_name": fmt.Sprintf("Seed %d", i), "contact_label": "Bench contact",
			"origin": "A (demo)", "destination": "B (demo)",
			"cargo_type": "palletized", "required_date": "2026-03-20", "priority": "high",
		})
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		st, _ := c.do(b, "GET", "/api/v1/shipments?status=in_transit&limit=50", nil)
		if st != 200 {
			b.Fatalf("list: %d", st)
		}
	}
}
