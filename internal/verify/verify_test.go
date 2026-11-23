package verify

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paulpham157/sdf/internal/task"
)

func mustRun(t *testing.T, opt Options) Result {
	t.Helper()
	res, err := Run(context.Background(), opt)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestPassRecordsEvidenceAndVerdictInTheLedger(t *testing.T) {
	f := newFixture(t)
	sha := f.agentCommit(t, map[string]string{"src/feature.txt": "done\n"})
	work := t.TempDir()
	opt := f.opts(sha)
	opt.WorkDir = work
	res := mustRun(t, opt)
	if res.Status != StatusPass || len(res.Reasons) != 0 {
		t.Fatalf("%s %+v", res.Status, res.Reasons)
	}
	if got := kinds(res); got != "task_snapshot checkout revision_identity designated_integrity secret_scan criterion:unit" {
		t.Fatalf("evidence %q", got)
	}
	v := res.Verdict.Value
	if v["task_hash"] != f.hash || v["submitted_sha"] != sha || v["base_sha"] != f.base || v["tree"] != g(t, f.repo, "rev-parse", sha+"^{tree}") {
		t.Fatalf("verdict %v", v)
	}
	if len(v["task_sources"].([]any)) != 3 {
		t.Fatalf("sources %v", v["task_sources"])
	}
	crit := res.Evidence[len(res.Evidence)-1].Value
	if crit["exit_code"] != int64(0) || crit["timed_out"] != false || !strings.HasPrefix(crit["stdout_sha256"].(string), "sha256:") {
		t.Fatalf("criterion %v", crit)
	}
	// Ledger: task, then genesis-chained evidence, then the verdict.
	b, e, err := f.ledger.Get(context.Background(), task.KindVerdict, res.Verdict.ID)
	if err != nil || string(b) != string(res.Verdict.Canonical) || e.Hash != res.Verdict.Hash {
		t.Fatalf("ledger verdict %v", err)
	}
	if n := len(f.ledger.Entries()); n != 1+1+len(res.Evidence)+1 {
		t.Fatalf("%d ledger entries", n)
	}
	// The temporary checkout is gone and was not in the repository.
	if left, _ := os.ReadDir(work); len(left) != 0 {
		t.Fatalf("left behind: %v", left)
	}
}

func TestUnreferencedCommitIsFetchedAndVerified(t *testing.T) {
	f := newFixture(t)
	sha := f.agentCommit(t, map[string]string{"src/feature.txt": "done\n"})
	g(t, f.repo, "reset", "-q", "--hard", f.base) // sha is now on no branch
	if res := mustRun(t, f.opts(sha)); res.Status != StatusPass {
		t.Fatalf("%s %+v", res.Status, res.Reasons)
	}
}

func TestFailClosedCodes(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, f *fixture) Options
		code  string
		ran   bool // whether the criterion ran
	}{
		{"check fails", func(t *testing.T, f *fixture) Options {
			return f.opts(f.agentCommit(t, map[string]string{"src/feature.txt": "still todo\n"}))
		}, CodeCheckFailed, true},
		{"designated test edited", func(t *testing.T, f *fixture) Options {
			return f.opts(f.agentCommit(t, map[string]string{"tests/run.sh": "#!/bin/sh\nexit 0\n"}))
		}, CodeDesignatedFileModified, false},
		{"designated test deleted", func(t *testing.T, f *fixture) Options {
			return f.opts(f.agentCommit(t, map[string]string{"tests/run.sh": "\x00delete", "src/feature.txt": "done\n"}))
		}, CodeDesignatedFileModified, false},
		{"designated glob gains a file", func(t *testing.T, f *fixture) Options {
			return f.opts(f.agentCommit(t, map[string]string{"tests/zz.sh": "exit 0\n", "src/feature.txt": "done\n"}))
		}, CodeDesignatedFileAdded, false},
		{"secret in diff", func(t *testing.T, f *fixture) Options {
			key := "AKIA" + strings.Repeat("Q7", 8)
			return f.opts(f.agentCommit(t, map[string]string{"src/feature.txt": "done\n", "src/conf.txt": "id=" + key + "\n"}))
		}, CodeSecretInDiff, false},
		{"task file rewritten in the submission", func(t *testing.T, f *fixture) Options {
			other, err := task.Build(context.Background(), f.repo, task.Input{ID: "feat", Title: "Feature", Base: f.base,
				Criteria: []task.CriterionInput{{Name: "unit", Argv: []string{"true"}}}})
			if err != nil {
				t.Fatal(err)
			}
			c, _, _ := other.Hash()
			return f.opts(f.agentCommit(t, map[string]string{".sdf/tasks/feat.json": string(c) + "\n", "src/feature.txt": "done\n"}))
		}, CodeTaskFileModified, false},
		{"no trusted task", func(t *testing.T, f *fixture) Options {
			o := f.opts(f.agentCommit(t, map[string]string{"src/feature.txt": "done\n"}))
			o.TaskRef, o.DataDir, o.Ledger = f.base, "", nil
			return o
		}, CodeTaskSnapshotMissing, false},
		{"trusted copies disagree", func(t *testing.T, f *fixture) Options {
			o := f.opts(f.agentCommit(t, map[string]string{"src/feature.txt": "done\n"}))
			other, err := task.Build(context.Background(), f.repo, task.Input{ID: "feat", Title: "Other", Base: f.base,
				Criteria: []task.CriterionInput{{Name: "unit", Argv: []string{"true"}}}})
			if err != nil {
				t.Fatal(err)
			}
			o.DataDir, o.Ledger = t.TempDir(), nil
			if _, err := task.Declare(context.Background(), other, task.Store{DataDir: o.DataDir}); err != nil {
				t.Fatal(err)
			}
			return o
		}, CodeTaskHashMismatch, false},
		{"working-tree task file is not trusted", func(t *testing.T, f *fixture) Options {
			o := f.opts(f.agentCommit(t, map[string]string{"src/feature.txt": "done\n"}))
			o.TaskRef, o.DataDir, o.Ledger = "", "", nil
			write(t, f.repo, map[string]string{".sdf/tasks/feat.json": "anything"})
			return o
		}, CodeTaskSnapshotMissing, false},
		{"base not an ancestor", func(t *testing.T, f *fixture) Options {
			g(t, f.repo, "checkout", "-q", "-b", "elsewhere", f.root)
			return f.opts(f.agentCommit(t, map[string]string{"src/feature.txt": "done\n", "tests/run.sh": featureTest}))
		}, CodeBaseNotAncestor, false},
		{"unknown revision", func(t *testing.T, f *fixture) Options {
			return f.opts(strings.Repeat("ab", 20))
		}, CodeRevisionUnresolvable, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			res := mustRun(t, tc.setup(t, f))
			if res.Status != StatusFail || !hasCode(res, tc.code) {
				t.Fatalf("%s %+v", res.Status, res.Reasons)
			}
			ran := strings.Contains(kinds(res), "criterion:")
			if ran != tc.ran {
				t.Fatalf("criterion ran=%t, evidence %s", ran, kinds(res))
			}
			if res.Verdict.Value["status"] != StatusFail {
				t.Fatal("verdict record status")
			}
		})
	}
}

func TestSecretIsNotCopiedIntoEvidence(t *testing.T) {
	f := newFixture(t)
	key := "ghp_" + strings.Repeat("a1B2", 9)
	sha := f.agentCommit(t, map[string]string{"src/feature.txt": "done\n", "src/x.go": "package x\n\nvar k = \"" + key + "\"\n"})
	res := mustRun(t, f.opts(sha))
	if !hasCode(res, CodeSecretInDiff) {
		t.Fatalf("%+v", res.Reasons)
	}
	for _, r := range append(res.Evidence, res.Verdict) {
		if strings.Contains(string(r.Canonical), key) {
			t.Fatalf("%s holds the secret", r.ID)
		}
	}
	for _, r := range res.Reasons {
		if r.Code == CodeSecretInDiff && r.Detail != "src/x.go:3 (github-token)" {
			t.Fatalf("detail %q", r.Detail)
		}
	}
}

func TestCriterionTimeoutAndCheckoutChanges(t *testing.T) {
	f := newFixture(t,
		task.CriterionInput{Name: "slow", Argv: []string{"sh", "-c", "sleep 30; true"}, TimeoutS: 1},
		task.CriterionInput{Name: "writer", Argv: []string{"sh", "-c", "echo x >> README"}},
	)
	sha := f.agentCommit(t, map[string]string{"src/feature.txt": "done\n"})
	res := mustRun(t, f.opts(sha))
	if res.Status != StatusFail || !hasCode(res, CodeCheckTimeout) || !hasCode(res, CodeCheckoutModified) {
		t.Fatalf("%s %+v", res.Status, res.Reasons)
	}
	for _, e := range res.Evidence {
		if e.Value["name"] == "slow" {
			if d := e.Value["duration_ms"].(int64); d > 15000 {
				t.Fatalf("timeout took %dms", d)
			}
		}
	}
}

func TestMissingCommandIsError(t *testing.T) {
	f := newFixture(t, task.CriterionInput{Name: "unit", Argv: []string{"sdf-no-such-command-xyz"}})
	sha := f.agentCommit(t, map[string]string{"src/feature.txt": "done\n"})
	res := mustRun(t, f.opts(sha))
	if res.Status != StatusError || !hasCode(res, CodeCheckError) {
		t.Fatalf("%s %+v", res.Status, res.Reasons)
	}
}

func TestUsageAndLedgerConflict(t *testing.T) {
	f := newFixture(t)
	if _, err := Run(context.Background(), Options{RepoDir: f.repo, SHA: "abc123", TaskID: "feat"}); !errors.Is(err, ErrUsage) {
		t.Fatalf("short sha: %v", err)
	}
	if _, err := Run(context.Background(), Options{RepoDir: f.repo, SHA: f.base, TaskID: "../x"}); !errors.Is(err, ErrUsage) {
		t.Fatalf("bad id: %v", err)
	}
	// The work dir has to be outside the repository.
	sha := f.agentCommit(t, map[string]string{"src/feature.txt": "done\n"})
	o := f.opts(sha)
	o.WorkDir = filepath.Join(f.repo, "tmp")
	if err := os.MkdirAll(o.WorkDir, 0o755); err != nil {
		t.Fatal(err)
	}
	res := mustRun(t, o)
	if res.Status != StatusError || !hasCode(res, CodeInfraError) {
		t.Fatalf("%s %+v", res.Status, res.Reasons)
	}
}
