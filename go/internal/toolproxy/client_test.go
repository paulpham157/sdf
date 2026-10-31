package toolproxy_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/paulpham157/sdf/go/internal/toolproxy"
)

func TestHTTPClientRoundTrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/attempts/att-http/actions" || r.Method != http.MethodPost {
			t.Errorf("bad path/method %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"attempt_id": "att-http",
			"action_id":  "ACTION-x",
			"actor":      "agent:codex",
			"principal":  "alice",
			"status":     "executed",
			"effect":     "allow",
			"reason":     "ok",
			"value":      map[string]any{"n": 1},
			"error":      "",
		})
	}))
	defer srv.Close()

	c := toolproxy.NewClient(srv.URL, srv.Client())
	c.Token = "tok"
	res, err := c.Execute(context.Background(), "att-http", toolproxy.ActionRequest{
		Tool: "filesystem", Action: "read", Resource: "/f",
	}, "agent:codex")
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != toolproxy.StatusExecuted || res.Value.(map[string]any)["n"].(float64) != 1 {
		t.Fatalf("%+v", res)
	}
}

func TestHTTPClientForbidden(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"detail": map[string]any{
				"error":  "actor_mismatch",
				"reason": "claimed actor mismatch",
				"actor":  "agent:codex",
			},
		})
	}))
	defer srv.Close()
	c := toolproxy.NewClient(srv.URL, srv.Client())
	_, err := c.Execute(context.Background(), "att", toolproxy.ActionRequest{
		Tool: "filesystem", Action: "read", Resource: "/f",
	}, "agent:other")
	if !errors.Is(err, toolproxy.ErrDenied) {
		t.Fatalf("err=%v", err)
	}
}
