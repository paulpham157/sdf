package httphost

import "testing"

func TestAllowedLoopbackNames(t *testing.T) {
	const port = "4318"
	for _, host := range []string{
		"localhost",
		"localhost:4318",
		"LOCALHOST:4318",
		"127.0.0.1",
		"127.0.0.1:4318",
		"[::1]",
		"[::1]:4318",
		"::1",
	} {
		if !Allowed(host, port) {
			t.Fatalf("Allowed(%q) = false", host)
		}
	}
	for _, host := range []string{
		"",
		"attacker.example",
		"attacker.example:4318",
		"127.0.0.1:1",
		"127.0.0.1:43180",
		"localhost:80",
		"[::1]:9",
		"0.0.0.0:4318",
		"evil.com",
	} {
		if Allowed(host, port) {
			t.Fatalf("Allowed(%q) = true", host)
		}
	}
}

func TestAllowedConfiguredHost(t *testing.T) {
	if !Allowed("192.0.2.10:4320", "4320", "192.0.2.10") {
		t.Fatal("configured host")
	}
	if !Allowed("192.0.2.10", "4320", "192.0.2.10") {
		t.Fatal("configured host without port")
	}
	if Allowed("192.0.2.10:1", "4320", "192.0.2.10") {
		t.Fatal("configured host on the wrong port")
	}
	if !Allowed("0.0.0.0:4320", "4320", "0.0.0.0") {
		t.Fatal("wildcard bind name")
	}
	if Allowed("192.0.2.10:4320", "4320", "0.0.0.0") {
		t.Fatal("wildcard bind accepted a different address")
	}
	if !Allowed("127.0.0.1:4320", "4320", "0.0.0.0") {
		t.Fatal("loopback name on a wildcard bind")
	}
}

func TestLoopback(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "localhost", "::1", "127.0.0.2"} {
		if !Loopback(host) {
			t.Fatalf("Loopback(%q) = false", host)
		}
	}
	for _, host := range []string{"", "0.0.0.0", "::", "192.0.2.10", "attacker.example"} {
		if Loopback(host) {
			t.Fatalf("Loopback(%q) = true", host)
		}
	}
}
