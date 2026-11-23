package check

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paulpham157/sdf/internal/pricing"
)

func TestCommittedCasesPassOrStayUnchecked(t *testing.T) {
	cases, err := Load(filepath.Join("testdata", "cases"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) < 4 {
		t.Fatalf("committed %d cases", len(cases))
	}
	report := Run(cases)
	if report.Failed != 0 {
		t.Fatal(Format(report))
	}
	status := map[string]string{}
	for _, result := range report.Results {
		status[result.ID] = result.Status
	}
	if status["priced-session"] != Pass || status["log-count"] != Pass || status["tool-name"] != Pass || status["summary-session"] != Pass {
		t.Fatalf("%v", status)
	}
	if status["unknown-price"] != NotChecked || status["unlinked-outcome"] != NotChecked {
		t.Fatalf("unchecked %v", status)
	}
	text := Format(report)
	if !strings.Contains(text, "unknown-price not_checked") || !strings.Contains(text, "unlinked-outcome not_checked") {
		t.Fatal(text)
	}
	if !strings.Contains(text, "priced-session pass") {
		t.Fatal(text)
	}
}

func TestWrongExpectFailsAndRightExpectPasses(t *testing.T) {
	good := caseWithExpect(t, "0.011988")
	bad := caseWithExpect(t, "0")
	report := Run([]Case{good, bad})
	if report.Passed != 1 || report.Failed != 1 {
		t.Fatal(Format(report))
	}
	if report.Results[0].Status != Pass || report.Results[1].Status != Fail {
		t.Fatal(Format(report))
	}
	text := Format(report)
	if !strings.Contains(text, good.ID+" pass\n") || !strings.Contains(text, bad.ID+" fail ") {
		t.Fatal(text)
	}
	if !strings.Contains(text, "1 passed, 1 failed, 0 not_checked") {
		t.Fatal(text)
	}
}

func TestUnknownPriceDoesNotFailTheOtherCase(t *testing.T) {
	priced := caseWithExpect(t, "0.011988")
	unknown := mustLoad(t, "unknown-price.json")
	unlinked := mustLoad(t, "unlinked-outcome.json")
	report := Run([]Case{unknown, priced, unlinked})
	if report.Failed != 0 || report.Passed != 1 || report.Skipped != 2 {
		t.Fatal(Format(report))
	}
	if report.Results[0].Status != NotChecked || report.Results[1].Status != Pass || report.Results[2].Status != NotChecked {
		t.Fatal(Format(report))
	}
}

func TestNilCatalogLeavesAPricedCaseUnchecked(t *testing.T) {
	report := RunWith([]Case{caseWithExpect(t, "0.011988")}, nil)
	if report.Failed != 0 || report.Skipped != 1 || report.Results[0].Status != NotChecked {
		t.Fatal(Format(report))
	}
	empty, err := pricing.NewFixture("2026-11-10T00:00:00Z", nil)
	if err != nil {
		t.Fatal(err)
	}
	again := RunWith([]Case{caseWithExpect(t, "0.011988")}, empty)
	if again.Results[0].Status != NotChecked {
		t.Fatal(Format(again))
	}
}

func TestTrimmedModulePathLoadsEmbeddedCases(t *testing.T) {
	trimmed := "github.com/paulpham157/sdf/internal/check/check.go"
	if _, err := os.Stat(filepath.Join(filepath.Dir(trimmed), "testdata", "cases")); err == nil {
		t.Fatal("trimmed module path exists on disk; this would not catch the -trimpath failure")
	}
	cases, err := loadDefaultAt(trimmed)
	if err != nil {
		t.Fatal(err)
	}
	disk, err := Load(filepath.Join("testdata", "cases"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) != len(disk) || len(cases) < 4 {
		t.Fatalf("embedded %d disk %d", len(cases), len(disk))
	}
	report := Run(cases)
	if report.Failed != 0 {
		t.Fatal(Format(report))
	}
	got := map[string]string{}
	for _, result := range report.Results {
		got[result.ID] = result.Status
	}
	if got["priced-session"] != Pass || got["unknown-price"] != NotChecked {
		t.Fatalf("%v", got)
	}
}

func TestLoadRejectsACaseWithTwoInputs(t *testing.T) {
	dir := t.TempDir()
	body := `{
	  "id": "both",
	  "field": "sessions.0.logs",
	  "expect": 1,
	  "otlp": {"signal": "logs", "body": {}},
	  "summary": {"sessions": [], "unattributed": []}
	}`
	if err := os.WriteFile(filepath.Join(dir, "both.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatal(err)
	}
}

func caseWithExpect(t *testing.T, expect string) Case {
	t.Helper()
	one := mustLoad(t, "priced-session.json")
	raw, err := json.Marshal(expect)
	if err != nil {
		t.Fatal(err)
	}
	one.Expect = raw
	one.ID = "priced-" + expect
	return one
}

func mustLoad(t *testing.T, name string) Case {
	t.Helper()
	cases, err := Load(filepath.Join("testdata", "cases"))
	if err != nil {
		t.Fatal(err)
	}
	id := strings.TrimSuffix(name, ".json")
	for _, one := range cases {
		if one.ID == id {
			return one
		}
	}
	t.Fatalf("missing %s", name)
	return Case{}
}

func TestEmbeddedCasesMatchTheCommittedFiles(t *testing.T) {
	fromDisk, err := Load(filepath.Join("testdata", "cases"))
	if err != nil {
		t.Fatal(err)
	}
	embedded, err := LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	if len(embedded) != len(fromDisk) {
		t.Fatalf("embedded %d, on disk %d", len(embedded), len(fromDisk))
	}
	for i := range fromDisk {
		if embedded[i].ID != fromDisk[i].ID || embedded[i].Field != fromDisk[i].Field {
			t.Fatalf("case %d: embedded %s, on disk %s", i, embedded[i].ID, fromDisk[i].ID)
		}
	}
}
