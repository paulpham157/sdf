package ui_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/paulpham157/sdf/internal/ui"
)

func TestUILoopbackHostStillServesSummary(t *testing.T) {
	db := openDB(t)
	srv := startUI(t, db)
	_, port := splitUI(t, srv.URL())
	for _, host := range []string{
		net.JoinHostPort("127.0.0.1", port),
		"127.0.0.1",
		net.JoinHostPort("localhost", port),
		"localhost",
		net.JoinHostPort("::1", port),
		"[::1]",
	} {
		status, _, body := getHost(t, srv.URL()+"api/summary", host)
		if status != 200 {
			t.Fatalf("host %q status %d body %q", host, status, body)
		}
	}
}

func TestUIRefusesReboundHost(t *testing.T) {
	db := openDB(t)
	srv := startUI(t, db)
	_, port := splitUI(t, srv.URL())
	paths := []string{"api/summary", "api/snapshot", "api/decisions", "api/overview-charts", "", "app.js", "app.css", "tokens.css"}
	hosts := []string{
		"attacker.example",
		net.JoinHostPort("attacker.example", port),
		net.JoinHostPort("127.0.0.1", "1"),
	}
	for _, path := range paths {
		for _, host := range hosts {
			status, _, body := getHost(t, srv.URL()+path, host)
			if status != 403 && status != 421 {
				t.Errorf("GET /%s host %q status %d body %q", path, host, status, trimBody(body))
			}
		}
	}
}

func TestUIAllowsConfiguredNonLoopbackHost(t *testing.T) {
	db := openDB(t)
	srv, err := ui.Start(ui.Config{Addr: "0.0.0.0:0", Store: db, Prices: "bundled"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})
	_, port := splitUI(t, srv.URL())
	status, _, body := getHost(t, srv.URL()+"api/summary", net.JoinHostPort("0.0.0.0", port))
	if status != 200 {
		t.Fatalf("configured host status %d body %q", status, body)
	}
	status, _, body = getHost(t, srv.URL()+"api/summary", "attacker.example")
	if status != 403 && status != 421 {
		t.Fatalf("attacker on wide bind status %d body %q", status, body)
	}
	status, _, body = getHost(t, srv.URL()+"app.js", "attacker.example")
	if status != 403 && status != 421 {
		t.Fatalf("static attacker status %d body %q", status, body)
	}
}

func splitUI(t *testing.T, raw string) (string, string) {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	host, port, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatal(err)
	}
	return host, port
}

func getHost(t *testing.T, rawURL, host string) (int, http.Header, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = host
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, string(body)
}

func trimBody(body string) string {
	body = strings.TrimSpace(body)
	if len(body) > 120 {
		return body[:120]
	}
	return body
}
