package ui_test

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/paulpham157/sdf/internal/ui"
)

func TestUIConnectionCap(t *testing.T) {
	db := openDB(t)
	srv, err := ui.Start(ui.Config{
		Addr:              "127.0.0.1:0",
		Store:             db,
		Prices:            "bundled",
		MaxConns:          1,
		MaxConnsPerIP:     1,
		ReadHeaderTimeout: 2 * time.Second,
		IdleTimeout:       5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
	addr := strings.TrimSuffix(strings.TrimPrefix(srv.URL(), "http://"), "/")
	hold, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = hold.Close() })
	_ = hold.SetDeadline(time.Now().Add(3 * time.Second))
	req := "GET /favicon.svg HTTP/1.1\r\nHost: " + addr + "\r\n\r\n"
	if _, err := hold.Write([]byte(req)); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(hold)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 || len(body) == 0 {
		t.Fatalf("held connection %d (%d bytes)", resp.StatusCode, len(body))
	}

	second, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	_ = second.SetDeadline(time.Now().Add(3 * time.Second))
	// The listener writes 503 as soon as it accepts. The request is not read.
	rejected, err := http.ReadResponse(bufio.NewReader(second), nil)
	if err != nil {
		t.Fatal(err)
	}
	msg, err := io.ReadAll(rejected.Body)
	_ = rejected.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if rejected.StatusCode != 503 || string(msg) != "too many connections" || rejected.Header.Get("Retry-After") != "1" {
		t.Fatalf("%d %q retry %q", rejected.StatusCode, msg, rejected.Header.Get("Retry-After"))
	}
}
