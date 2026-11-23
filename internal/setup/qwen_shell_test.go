package setup

import (
	"os/exec"
	"strings"
	"testing"
)

func TestQwenShQuote(t *testing.T) {
	for in, want := range map[string]string{
		"http://127.0.0.1:4318": `'http://127.0.0.1:4318'`,
		"":                      `''`,
		"a'b":                   `'a'\''b'`,
		"$(id) `x` ;":           "'$(id) `x` ;'",
	} {
		if got := qwenShQuote(in); got != want {
			t.Fatalf("qwenShQuote(%q) = %s, want %s", in, got, want)
		}
		sh, err := exec.LookPath("sh")
		if err != nil {
			continue
		}
		out, err := exec.Command(sh, "-c", "printf %s "+qwenShQuote(in)).Output()
		if err != nil || string(out) != in {
			t.Fatalf("sh round trip of %q: %q %v", in, out, err)
		}
	}
}

func TestQwenCheckEndpoint(t *testing.T) {
	good := map[string]string{"127.0.0.1": "127.0.0.1", "localhost": "localhost", "otel.example.com": "otel.example.com", "::1": "::1", "[::1]": "::1", "fe80::1": "fe80::1"}
	for in, want := range good {
		ep, err := qwenCheckEndpoint(Endpoint{Host: in, HTTPPort: 4318})
		if err != nil || ep.Host != want {
			t.Fatalf("%q: %v %q", in, err, ep.Host)
		}
	}
	for _, bad := range []string{"", "a b", "x;rm -rf ~", "$(id)", "a'b", "[127.0.0.1]", "1.2.3.4:5", "-x", "::1%eth0", "host/path", "ex\nample"} {
		if _, err := qwenCheckEndpoint(Endpoint{Host: bad, HTTPPort: 4318}); err == nil {
			t.Fatalf("host %q accepted", bad)
		}
	}
	for _, port := range []int{0, -1, 65536} {
		if _, err := qwenCheckEndpoint(Endpoint{Host: "127.0.0.1", HTTPPort: port}); err == nil {
			t.Fatalf("port %d accepted", port)
		}
	}
}

func TestQwenInitQuotesExportsAndRejectsBadEndpoint(t *testing.T) {
	clearQwenEnv(t)
	home := t.TempDir()
	req := qwenRequest(home, false)
	req.Endpoint.Host = "[::1]"
	res, err := Run(req)
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, res.Text,
		"export QWEN_TELEMETRY_ENABLED='true'\n",
		"export QWEN_TELEMETRY_OTLP_ENDPOINT='http://[::1]:4318'\n",
		"export QWEN_TELEMETRY_OTLP_PROTOCOL='http'\n",
		"export QWEN_TELEMETRY_LOG_PROMPTS='false'\n",
		`"otlpEndpoint": "http://[::1]:4318"`)
	req = qwenRequest(home, true)
	req.Endpoint.Host = "x;touch pwned"
	if _, err := Run(req); err == nil || !strings.Contains(err.Error(), "--host") {
		t.Fatalf("bad host: %v", err)
	}
	req = qwenRequest(home, true)
	req.Endpoint.HTTPPort = 70000
	if _, err := Run(req); err == nil || !strings.Contains(err.Error(), "--port") {
		t.Fatalf("bad port: %v", err)
	}
}
