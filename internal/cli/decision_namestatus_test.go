package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestParseNameStatusLog(t *testing.T) {
	const (
		empty = "5f3b3c49c93dbe986f99e3c213e8528f9d29c1b5"
		file  = "291b667e3a259479833d0074fb47d18ea799c4ec"
		both  = "4841e47646d780c292c40f725d4b5d21cdc2f2d0"
		ren   = "db7e1fa14b07f56e940cffbfbd57259675964a7a"
	)
	raw := []byte(ren + "\x1f\x1e\x00\nD\x00a.go\x00A\x00c.go\x00" +
		both + "\x1fone\x1ftwo\x1e\x00\nA\x00sub/b.go\x00" +
		file + "\x1f\x1e\x00\nA\x00a.go\x00" +
		empty + "\x1f\x1e\x00")
	got, err := parseNameStatusLog(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("%d commits", len(got))
	}
	if got[0].SHA != ren || strings.Join(got[0].Paths, ",") != "a.go,c.go" {
		t.Fatalf("rename %+v", got[0])
	}
	if len(got[1].Trailers) != 2 || got[1].Trailers[0] != "one" || got[1].Trailers[1] != "two" || strings.Join(got[1].Paths, ",") != "sub/b.go" {
		t.Fatalf("trailers %+v", got[1])
	}
	if len(got[3].Paths) != 0 || got[3].SHA != empty {
		t.Fatalf("empty %+v", got[3])
	}
	renamed := []byte(ren + "\x1f\x1e\x00\nR100\x00a.go\x00c.go\x00")
	got, err = parseNameStatusLog(renamed)
	if err != nil || len(got) != 1 || strings.Join(got[0].Paths, ",") != "a.go,c.go" {
		t.Fatalf("%v %+v", err, got)
	}
	copied := []byte(ren + "\x1f\x1e\x00\nC075\x00a.go\x00c.go\x00")
	got, err = parseNameStatusLog(copied)
	if err != nil || strings.Join(got[0].Paths, ",") != "a.go,c.go" {
		t.Fatalf("copy %v %+v", err, got)
	}
	if _, err := parseNameStatusLog([]byte("not a record")); err == nil {
		t.Fatal("accepted garbage")
	}
	if _, err := parseNameStatusLog([]byte(empty + "\x1f\x1e\x00\nZZZ\x00a.go\x00")); err == nil {
		t.Fatal("accepted a bad status")
	}
	if _, err := parseNameStatusLog(bytes.TrimSuffix(raw, []byte{0})); err == nil {
		t.Fatal("accepted a truncated record")
	}
}
