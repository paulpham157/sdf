package netlimit

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"
)

func TestResolveDefaultsClampsAndRejects(t *testing.T) {
	out, err := Resolve(Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if out.MaxConns != DefaultMaxConns || out.MaxConnsPerIP != DefaultMaxConnsPerIP || out.MaxConnsPerToken != DefaultMaxConnsPerToken {
		t.Fatalf("%+v", out)
	}
	if out.MaxBodyBytes != DefaultMaxBodyBytes || out.MaxInFlightBytes != DefaultMaxInFlightBytes {
		t.Fatalf("%+v", out)
	}
	if out.ReadHeaderTimeout != DefaultReadHeaderTimeout || out.ReadTimeout != DefaultReadTimeout || out.IdleTimeout != DefaultIdleTimeout {
		t.Fatalf("%+v", out)
	}

	clamped, err := Resolve(Limits{MaxConns: 4, MaxConnsPerIP: 100, MaxConnsPerToken: 9, MaxBodyBytes: 1000, MaxInFlightBytes: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if clamped.MaxConnsPerIP != 4 || clamped.MaxConnsPerToken != 4 {
		t.Fatalf("clamp %+v", clamped)
	}

	if _, err := Resolve(Limits{MaxBodyBytes: 1000, MaxInFlightBytes: 999}); err == nil {
		t.Fatal("in-flight below one body was accepted")
	}
	if _, err := Resolve(Limits{MaxConns: -1}); err == nil {
		t.Fatal("negative connection cap was accepted")
	}
	if _, err := Resolve(Limits{ReadTimeout: -time.Second}); err == nil {
		t.Fatal("negative timeout was accepted")
	}
}

func TestBudgetStaysWithinCapAndReleases(t *testing.T) {
	b := NewBudget(1000)
	var mu sync.Mutex
	var holds []*Hold
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h, ok := b.Reserve(100)
			if !ok {
				return
			}
			mu.Lock()
			holds = append(holds, h)
			used := b.Used()
			mu.Unlock()
			if used > 1000 {
				t.Errorf("used %d", used)
			}
		}()
	}
	wg.Wait()
	if got := b.Used(); got > 1000 || got%100 != 0 || got == 0 {
		t.Fatalf("used %d", got)
	}
	held := len(holds)
	for _, h := range holds {
		h.Release()
		h.Release()
	}
	if b.Used() != 0 {
		t.Fatalf("after release %d", b.Used())
	}
	again, ok := b.Reserve(int64(held) * 100)
	if !ok {
		t.Fatal("reserve after release")
	}
	if !again.Add(0) {
		t.Fatal("zero add")
	}
	if again.Add(-1) {
		t.Fatal("negative add")
	}
	again.Sub(50)
	if b.Used() != int64(held)*100-50 {
		t.Fatalf("after sub %d", b.Used())
	}
	again.Release()
	if b.Used() != 0 {
		t.Fatalf("final %d", b.Used())
	}
	if _, ok := NewBudget(0).Reserve(1); ok {
		t.Fatal("zero budget admitted a byte")
	}
}

func TestRemoteIPCanonicalizesMappedAddresses(t *testing.T) {
	mapped := &net.TCPAddr{IP: net.ParseIP("::ffff:127.0.0.1"), Port: 80}
	plain := &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 9}
	if RemoteIP(mapped) != RemoteIP(plain) || RemoteIP(plain) != "127.0.0.1" {
		t.Fatalf("mapped %q plain %q", RemoteIP(mapped), RemoteIP(plain))
	}
	if RemoteIP(nil) != "unknown" {
		t.Fatal(RemoteIP(nil))
	}
}

func TestListenerRejectsOneAddressThenAdmitsAgain(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	limited := Wrap(ln, 2, 1)
	t.Cleanup(func() { _ = limited.Close() })
	accepted := make(chan net.Conn, 2)
	go serveAccepted(limited, accepted)

	first := dial(t, limited.Addr().String())
	admitted := recvConn(t, accepted)
	second := dial(t, limited.Addr().String())
	status, hdr, body := readHTTP(t, second)
	if status != 429 || body != MsgTooManyIP || hdr.Get("Retry-After") != "1" {
		t.Fatalf("%d %q retry %q", status, body, hdr.Get("Retry-After"))
	}
	_ = admitted.Close()
	_ = first.Close()
	third := dial(t, limited.Addr().String())
	again := recvConn(t, accepted)
	_ = again.Close()
	_ = third.Close()
}

func TestListenerTotalIs503AndReleases(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	limited := Wrap(ln, 1, 1)
	t.Cleanup(func() { _ = limited.Close() })
	accepted := make(chan net.Conn, 1)
	go serveAccepted(limited, accepted)

	first := dial(t, limited.Addr().String())
	admitted := recvConn(t, accepted)
	second := dial(t, limited.Addr().String())
	status, hdr, body := readHTTP(t, second)
	if status != 503 || body != MsgTooManyConns || hdr.Get("Retry-After") != "1" {
		t.Fatalf("%d %q retry %q", status, body, hdr.Get("Retry-After"))
	}
	_ = admitted.Close()
	_ = first.Close()
	third := dial(t, limited.Addr().String())
	again := recvConn(t, accepted)
	_ = again.Close()
	_ = third.Close()
}

func serveAccepted(ln net.Listener, accepted chan<- net.Conn) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		accepted <- c
		go func(c net.Conn) {
			_, _ = io.Copy(io.Discard, c)
			_ = c.Close()
		}(c)
	}
}

func recvConn(t *testing.T, ch <-chan net.Conn) net.Conn {
	t.Helper()
	select {
	case c := <-ch:
		t.Cleanup(func() { _ = c.Close() })
		return c
	case <-time.After(2 * time.Second):
		t.Fatal("connection was not admitted")
		return nil
	}
}

func dial(t *testing.T, addr string) net.Conn {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	_ = c.SetDeadline(time.Now().Add(3 * time.Second))
	return c
}

func readHTTP(t *testing.T, c net.Conn) (int, http.Header, string) {
	t.Helper()
	resp, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, resp.Header, string(body)
}
