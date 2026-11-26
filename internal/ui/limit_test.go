package ui_test

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"strconv"
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

func TestUIShutdownLetsARunningHandlerFinish(t *testing.T) {
	db := openDB(t)
	srv, err := ui.Start(ui.Config{
		Addr:              "127.0.0.1:0",
		Store:             db,
		Prices:            "bundled",
		ReadHeaderTimeout: 2 * time.Second,
		IdleTimeout:       5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	entered := make(chan struct{})
	srv.SetBlockHandler(func(context.Context) {
		close(entered)
		<-release
	})
	errc := make(chan error, 1)
	okc := make(chan struct{}, 1)
	go func() {
		resp, err := http.Get(srv.URL())
		if err != nil {
			errc <- err
			return
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			errc <- err
			return
		}
		if resp.StatusCode != 200 || len(body) == 0 {
			errc <- errStatus(resp.StatusCode, len(body))
			return
		}
		okc <- struct{}{}
	}()
	select {
	case <-entered:
	case err := <-errc:
		t.Fatal(err)
	case <-time.After(3 * time.Second):
		t.Fatal("handler did not start")
	}
	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		done <- srv.Shutdown(ctx)
	}()
	time.Sleep(100 * time.Millisecond)
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown did not return")
	}
	select {
	case err := <-errc:
		t.Fatal(err)
	case <-okc:
	case <-time.After(3 * time.Second):
		t.Fatal("client did not receive the handler response")
	}
}

type statusErr struct {
	code int
	n    int
}

func (e statusErr) Error() string {
	return "ui response " + strconv.Itoa(e.code) + " bytes " + strconv.Itoa(e.n)
}

func errStatus(code, n int) error { return statusErr{code, n} }
