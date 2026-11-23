package setup

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestProbeAuthSendsTheTokenToALoopbackReceiver(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	_, portText, _ := net.SplitHostPort(srv.Listener.Addr().String())
	port, _ := strconv.Atoi(portText)
	res := ProbeAuth(Endpoint{Host: "127.0.0.1", HTTPPort: port}, "tok", "SDF_OTLP_TOKEN")
	if !res.Failed || !strings.Contains(res.Text, "token refused") || len(got) != 1 || got[0] != "Bearer tok" {
		t.Fatalf("%+v %q", res, got)
	}
	if strings.Contains(res.Text, "tok\"") || strings.Contains(res.Text, "Bearer") {
		t.Fatalf("token in output: %q", res.Text)
	}
}

// Like the hooks, the probe never sends the token off loopback.
func TestProbeTokenOnlyForLoopback(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "127.1.2.3", "localhost", "::1", "[::1]"} {
		if probeToken(host, "tok") != "tok" {
			t.Fatalf("%s: token dropped", host)
		}
	}
	for _, host := range []string{"0.0.0.0", "::", "192.0.2.1", "example.com", "10.0.0.5"} {
		if probeToken(host, "tok") != "" {
			t.Fatalf("%s: token kept", host)
		}
	}
}

// probeAt runs ProbeAuth against handler and returns the result and the
// paths the server saw.
func probeAt(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) (AuthCheck, []string) {
	t.Helper()
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		handler(w, r)
	}))
	defer srv.Close()
	_, portText, _ := net.SplitHostPort(srv.Listener.Addr().String())
	port, _ := strconv.Atoi(portText)
	return ProbeAuth(Endpoint{Host: "127.0.0.1", HTTPPort: port}, "tok", "SDF_OTLP_TOKEN"), paths
}

// A redirect is reported, not followed: the token must not go to the
// redirect target, and a 200 there would say nothing about this receiver.
func TestProbeAuthDoesNotFollowRedirects(t *testing.T) {
	res, paths := probeAt(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/logs" {
			http.Redirect(w, r, "/elsewhere", http.StatusTemporaryRedirect)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	if !res.Failed || !strings.Contains(res.Text, "HTTP 307") || len(paths) != 1 {
		t.Fatalf("%+v %q", res, paths)
	}
}

// Anything but 401 or 415 is a failed check, not a pass.
func TestProbeAuthFailsOnUnexpectedStatus(t *testing.T) {
	for _, code := range []int{http.StatusOK, http.StatusNotFound, http.StatusInternalServerError} {
		res, _ := probeAt(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(code) })
		if !res.Failed || !strings.Contains(res.Text, "unexpected answer") || !strings.Contains(res.Text, "HTTP "+strconv.Itoa(code)) {
			t.Fatalf("%d: %+v", code, res)
		}
	}
	res, _ := probeAt(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusUnsupportedMediaType) })
	if res.Failed || !strings.Contains(res.Text, "writes accepted with the token from SDF_OTLP_TOKEN") {
		t.Fatalf("415: %+v", res)
	}
}
