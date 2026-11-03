package toolproxy_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paulpham157/sdf/go/internal/toolproxy"

	_ "modernc.org/sqlite"
)

func TestNilModeSourceStampsSynthetic(t *testing.T) {
	p := toolproxy.NewProxy(func(context.Context, toolproxy.ActionRequest) (any, error) {
		return map[string]any{"ok": true}, nil
	}, nil)
	p.RegisterScope(scope("att-nil"))
	req := toolproxy.ActionRequest{AttemptID: "att-nil", Tool: "filesystem", Action: "read", Resource: "/a"}
	res, err := p.Execute(context.Background(), req, "", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if res.EvidenceMode != toolproxy.EvidenceModeSynthetic {
		t.Fatalf("result mode = %q", res.EvidenceMode)
	}
	obs, err := p.Store().Observations(req.AttemptID, res.ActionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(obs) == 0 {
		t.Fatal("no observations")
	}
	for _, ev := range obs {
		if ev.EvidenceMode != toolproxy.EvidenceModeSynthetic {
			t.Fatalf("%s mode = %q", ev.Event, ev.EvidenceMode)
		}
	}
}

func TestEvidenceMode_StoreRulesAndLegacyColumn(t *testing.T) {
	t.Run("memory", func(t *testing.T) {
		assertStoreModeRules(t, toolproxy.NewMemoryStore())
	})
	t.Run("sqlite", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "fence.db")
		store, err := toolproxy.OpenSQLiteStore(path)
		if err != nil {
			t.Fatal(err)
		}
		assertStoreModeRules(t, store)
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		assertModeCheck(t, path)
	})
	t.Run("pre-column file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "old.db")
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		_, err = db.Exec(`
CREATE TABLE tool_outcome_events (
  event_id TEXT PRIMARY KEY,
  attempt_id TEXT NOT NULL,
  action_id TEXT NOT NULL,
  event TEXT NOT NULL,
  actor TEXT NOT NULL DEFAULT '',
  principal TEXT NOT NULL DEFAULT '',
  tool TEXT NOT NULL DEFAULT '',
  action TEXT NOT NULL DEFAULT '',
  resource TEXT NOT NULL DEFAULT '',
  decision TEXT NOT NULL DEFAULT '',
  reason TEXT NOT NULL DEFAULT '',
  policy TEXT NOT NULL DEFAULT '',
  value_json TEXT,
  error TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
)`)
		if err != nil {
			t.Fatal(err)
		}
		_, err = db.Exec(`
INSERT INTO tool_outcome_events (
  event_id, attempt_id, action_id, event, actor, principal, tool, action, resource,
  decision, reason, policy, value_json, error, created_at
) VALUES ('e1', 'att-old', 'ACTION-old', 'policy_decided', 'agent:codex', 'alice',
  'filesystem', 'read', '/a', 'allow', 'ok', 'allowlist', NULL, '', '2026-10-31T00:00:00Z')`)
		if err != nil {
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}

		store, err := toolproxy.OpenSQLiteStore(path)
		if err != nil {
			t.Fatal(err)
		}
		obs, err := store.Observations("att-old", "ACTION-old")
		if err != nil {
			t.Fatal(err)
		}
		if len(obs) != 1 || obs[0].EvidenceMode != toolproxy.EvidenceModeSynthetic {
			t.Fatalf("migrated observation = %+v", obs)
		}
		promoted, err := store.PromoteAttempt("att-old")
		if err != nil || promoted {
			t.Fatalf("promote unknown promoted=%v err=%v", promoted, err)
		}
		if _, err := store.EnsureAttempt("att-old"); err != nil {
			t.Fatal(err)
		}
		promoted, err = store.PromoteAttempt("att-old")
		if err != nil || !promoted {
			t.Fatalf("promote recorded promoted=%v err=%v", promoted, err)
		}
		obs, err = store.Observations("att-old", "ACTION-old")
		if err != nil {
			t.Fatal(err)
		}
		if obs[0].EvidenceMode != toolproxy.EvidenceModeSynthetic {
			t.Fatalf("promotion rewrote history: %+v", obs[0])
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		assertModeCheck(t, path)
	})
}

func assertStoreModeRules(t *testing.T, store toolproxy.OutcomeStore) {
	t.Helper()
	err := store.AppendPolicy(toolproxy.Observation{
		AttemptID: "att", ActionID: "ACTION-1", EvidenceMode: "bogus",
	})
	if err == nil || !strings.Contains(err.Error(), "ADR 0031") {
		t.Fatalf("bogus write err = %v", err)
	}
	obs, err := store.Observations("att", "ACTION-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(obs) != 0 {
		t.Fatalf("refused write stored %+v", obs)
	}
	if err := store.AppendPolicy(toolproxy.Observation{AttemptID: "att", ActionID: "ACTION-1"}); err != nil {
		t.Fatal(err)
	}
	obs, err = store.Observations("att", "ACTION-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(obs) != 1 || obs[0].EvidenceMode != toolproxy.EvidenceModeSynthetic {
		t.Fatalf("empty write = %+v", obs)
	}

	promoted, err := store.PromoteAttempt("missing")
	if err != nil || promoted {
		t.Fatalf("promote missing promoted=%v err=%v", promoted, err)
	}
	if _, ok, err := store.AttemptEvidenceMode("missing"); err != nil || ok {
		t.Fatalf("missing record ok=%v err=%v", ok, err)
	}
	created, err := store.EnsureAttempt("att-mode")
	if err != nil || !created {
		t.Fatalf("ensure created=%v err=%v", created, err)
	}
	created, err = store.EnsureAttempt("att-mode")
	if err != nil || created {
		t.Fatalf("second ensure created=%v err=%v", created, err)
	}
	promoted, err = store.PromoteAttempt("att-mode")
	if err != nil || !promoted {
		t.Fatalf("promote promoted=%v err=%v", promoted, err)
	}
	promoted, err = store.PromoteAttempt("att-mode")
	if err != nil || promoted {
		t.Fatalf("second promote promoted=%v err=%v", promoted, err)
	}
	created, err = store.EnsureAttempt("att-mode")
	if err != nil || created {
		t.Fatalf("ensure after live created=%v err=%v", created, err)
	}
	if err := store.ForgetSyntheticAttempt("att-mode"); err != nil {
		t.Fatal(err)
	}
	mode, ok, err := store.AttemptEvidenceMode("att-mode")
	if err != nil || !ok || mode != toolproxy.EvidenceModeLive {
		t.Fatalf("forget live mode=%q ok=%v err=%v", mode, ok, err)
	}
}

func assertModeCheck(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`UPDATE tool_outcome_events SET evidence_mode = 'bogus'`)
	if err == nil || !strings.Contains(err.Error(), "CHECK") {
		t.Fatalf("CHECK err = %v", err)
	}
}
