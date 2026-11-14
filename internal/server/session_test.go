package server_test

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestSessionEventIsStoredAsALog(t *testing.T) {
	base, db := start(t)
	body := `{"tool":"claude-code","session_id":"sess-1","edge":"open","reason":"startup"}`
	resp, err := http.Post(base+"/v1/sessions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(got) != "{}" {
		t.Fatalf("%d %s", resp.StatusCode, got)
	}
	rows, err := db.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("%d rows", len(rows))
	}
	row := rows[0]
	if row.Signal != "logs" || deref(row.Name) != "sdf.session.open" || deref(row.SessionID) != "sess-1" || deref(row.ServiceName) != "claude-code" {
		t.Fatalf("%+v", row)
	}
	if row.TimeUnixNano == nil || row.Attributes["sdf.session.reason"] != "startup" {
		t.Fatalf("%+v", row)
	}
}

func TestSessionEventRejectsABadPayloadAndStoresNothing(t *testing.T) {
	base, db := start(t)
	for _, body := range []string{
		`{"tool":"claude-code","session_id":"sess-1"}`,
		`{"tool":"nope","session_id":"sess-1","edge":"open"}`,
		`{"tool":"pi","session_id":"  ","edge":"close"}`,
		`not json`,
	} {
		resp, err := http.Post(base+"/v1/sessions", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 400 {
			t.Fatalf("%q got %d", body, resp.StatusCode)
		}
	}
	rows, err := db.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("stored %d", len(rows))
	}
}
