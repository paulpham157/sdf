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
	if !res.Refused || !strings.Contains(res.Text, "token refused") || len(got) != 1 || got[0] != "Bearer tok" {
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
