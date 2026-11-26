package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	// Published v0.1.0 is an annotated tag. The ref points at the tag
	// object; the commit below is what CITATION.cff must record.
	v010TagObject = "1041cc90c022ce273e77febc646e85581732baad"
	v010Commit    = "0fd24b622bf4f6e7a0db29d144e739a7c45a0d67"
	otherCommit   = "0123456789abcdef0123456789abcdef01234567"
)

func TestStableVersionAndDate(t *testing.T) {
	ver, ok := stableVersion("v0.2.0")
	if !ok || ver != "0.2.0" {
		t.Fatalf("stable got %q %v", ver, ok)
	}
	for _, tag := range []string{"v0.1.0-rc.1", "v0.1", "0.1.0", "v0.1.0+build", "vv0.1.0", "v0.1.0 ", "V0.1.0"} {
		if _, ok := stableVersion(tag); ok {
			t.Fatalf("accepted %q", tag)
		}
	}
	date, err := releaseDate("2026-11-24T07:46:11Z")
	if err != nil || date != "2026-11-24" {
		t.Fatalf("utc date %q %v", date, err)
	}
	date, err = releaseDate("2026-11-24T00:30:00+02:00")
	if err != nil || date != "2026-11-23" {
		t.Fatalf("offset before midnight UTC %q %v", date, err)
	}
	date, err = releaseDate("2026-11-24T23:30:00-05:00")
	if err != nil || date != "2026-11-25" {
		t.Fatalf("offset after midnight UTC %q %v", date, err)
	}
	if _, err := releaseDate("2026-02-29"); err == nil {
		t.Fatal("accepted a non-leap 29 February")
	}
	if date, err = releaseDate("2024-02-29"); err != nil || date != "2024-02-29" {
		t.Fatalf("leap day %q %v", date, err)
	}
	if _, err := releaseDate("2026-11-24T07:46:11"); err == nil {
		t.Fatal("accepted a timestamp without a zone")
	}
	if _, err := releaseDate(""); err == nil {
		t.Fatal("accepted an empty timestamp")
	}
}

func TestUpdateRewritesOnlyCitationFields(t *testing.T) {
	const original = "" +
		"cff-version: 1.2.0\n" +
		"title: \"Software Decision Fabric\"\n" +
		"authors:\n" +
		"  - family-names: Pham\n" +
		"    version: 9.9.9\n" +
		"license: Apache-2.0\n" +
		"version: \"0.1.0\" # cite\n" +
		"date-released: 2026-11-24\n" +
		"url: \"https://github.com/paulpham157/sdf\"\n" +
		"commit: 0fd24b622bf4f6e7a0db29d144e739a7c45a0d67\n"
	in := []byte(original)
	got, err := Update(in, "0.2.0", "2026-12-01", otherCommit)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusUpdated {
		t.Fatalf("status %s", got.Status)
	}
	if !bytes.Equal(in, []byte(original)) {
		t.Fatal("Update mutated the input")
	}
	assertOnlyKeys(t, in, got.Content, keyVersion, keyDate, keyCommit)
	text := string(got.Content)
	for _, want := range []string{
		"cff-version: 1.2.0\n",
		"title: \"Software Decision Fabric\"\n",
		"  - family-names: Pham\n",
		"    version: 9.9.9\n",
		"license: Apache-2.0\n",
		"version: \"0.2.0\" # cite\n",
		"date-released: 2026-12-01\n",
		"url: \"https://github.com/paulpham157/sdf\"\n",
		"commit: " + otherCommit + "\n",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in\n%s", want, text)
		}
	}
	if strings.Contains(text, v010TagObject) {
		t.Fatal("wrote the annotated tag object")
	}
}

func TestUpdateKeepsBytesWhenCurrent(t *testing.T) {
	in := []byte("version:  0.1.0\r\ndate-released: 2026-11-24\r\ncommit: " + v010Commit + "\r\nlicense: Apache-2.0")
	got, err := Update(in, "0.1.0", "2026-11-24", v010Commit)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusUnchanged || !bytes.Equal(got.Content, in) {
		t.Fatalf("status %s changed %v", got.Status, !bytes.Equal(got.Content, in))
	}
}

func TestUpdateSkipsOlderRelease(t *testing.T) {
	in := []byte("version: 0.2.0\ndate-released: 2026-12-01\ncommit: " + otherCommit + "\n")
	got, err := Update(in, "0.1.10", "2026-11-01", v010Commit)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusSkipped || !bytes.Equal(got.Content, in) {
		t.Fatalf("status %s reason %s", got.Status, got.Reason)
	}
	// "0.1.10" < "0.1.9" as text and 0.1.10 > 0.1.9 as versions.
	older := []byte("version: 0.1.9\ndate-released: 2026-11-01\ncommit: " + v010Commit + "\n")
	got, err = Update(older, "0.1.10", "2026-11-02", otherCommit)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusUpdated {
		t.Fatalf("status %s", got.Status)
	}
}

func TestUpdateSameVersionReplacesCommit(t *testing.T) {
	in := []byte("version: 0.2.0\ndate-released: 2026-12-01\ncommit: " + v010Commit + "\n")
	got, err := Update(in, "0.2.0", "2026-12-01", otherCommit)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusUpdated {
		t.Fatalf("status %s", got.Status)
	}
	assertOnlyKeys(t, in, got.Content, keyCommit)
	if !bytes.Contains(got.Content, []byte("commit: "+otherCommit+"\n")) {
		t.Fatalf("%s", got.Content)
	}
	if !bytes.Contains(got.Content, []byte("version: 0.2.0\n")) {
		t.Fatalf("version line changed:\n%s", got.Content)
	}
}

func TestUpdateRejectsBadFields(t *testing.T) {
	ok := []byte("version: 0.1.0\ndate-released: 2026-11-24\ncommit: " + v010Commit + "\n")
	cases := []struct {
		name, version, date, commit string
		file                        []byte
	}{
		{name: "leading v", version: "v0.2.0", date: "2026-12-01", commit: otherCommit, file: ok},
		{name: "bad date", version: "0.2.0", date: "2026-02-29", commit: otherCommit, file: ok},
		{name: "upper sha", version: "0.2.0", date: "2026-12-01", commit: strings.ToUpper(otherCommit), file: ok},
		{name: "short sha", version: "0.2.0", date: "2026-12-01", commit: "abcd", file: ok},
		{name: "missing commit", version: "0.2.0", date: "2026-12-01", commit: otherCommit, file: []byte("version: 0.1.0\ndate-released: 2026-11-24\n")},
		{name: "duplicate version", version: "0.2.0", date: "2026-12-01", commit: otherCommit, file: []byte("version: 0.1.0\nversion: 0.1.1\ndate-released: 2026-11-24\ncommit: " + v010Commit + "\n")},
		{name: "flow value", version: "0.2.0", date: "2026-12-01", commit: otherCommit, file: []byte("version: [0.1.0]\ndate-released: 2026-11-24\ncommit: " + v010Commit + "\n")},
		{name: "weird current version", version: "0.2.0", date: "2026-12-01", commit: otherCommit, file: []byte("version: 0.1.0-rc.1\ndate-released: 2026-11-24\ncommit: " + v010Commit + "\n")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Update(tc.file, tc.version, tc.date, tc.commit); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestUpdateCRLFAndNoFinalNewline(t *testing.T) {
	in := []byte("version: 0.1.0\r\ndate-released: 2026-11-24\r\ncommit: " + v010Commit + "\r\nlicense: Apache-2.0")
	got, err := Update(in, "0.2.0", "2026-11-24", v010Commit)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasSuffix(got.Content, []byte("license: Apache-2.0")) || !bytes.Contains(got.Content, []byte("\r\n")) {
		t.Fatalf("%q", got.Content)
	}
	if !bytes.Contains(got.Content, []byte("version: 0.2.0\r\n")) {
		t.Fatalf("%q", got.Content)
	}
}

func TestRepositoryCitationFile(t *testing.T) {
	body := readRepoFile(t, "CITATION.cff")
	parsed, err := parseFile(body)
	if err != nil {
		t.Fatal(err)
	}
	version := parsed.fields[keyVersion].value
	date := parsed.fields[keyDate].value
	commit := parsed.fields[keyCommit].value
	got, err := Update(body, version, date, commit)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusUnchanged || !bytes.Equal(got.Content, body) {
		t.Fatalf("reapplying the checked-in fields changed the file: %s", got.Status)
	}
	nextVer, err := bumpPatch(version)
	if err != nil {
		t.Fatal(err)
	}
	got, err = Update(body, nextVer, date, commit)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusUpdated {
		t.Fatalf("status %s", got.Status)
	}
	assertOnlyKeys(t, body, got.Content, keyVersion)
	for _, keep := range []string{
		"title: \"Software Decision Fabric\"\n",
		"family-names: Pham\n",
		"license: Apache-2.0\n",
		"url: \"https://github.com/paulpham157/sdf\"\n",
		"repository-code: \"https://github.com/paulpham157/sdf\"\n",
		"email: paulpham157@gmail.com\n",
	} {
		if !bytes.Contains(got.Content, []byte(keep)) {
			t.Fatalf("dropped %q", keep)
		}
	}
}

func TestPeel(t *testing.T) {
	nested := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	tests := []struct {
		name    string
		start   gitObj
		next    map[string]gitObj
		commit  string
		tag     string
		wantErr string
	}{
		{
			name:   "lightweight",
			start:  gitObj{Type: "commit", SHA: v010Commit},
			commit: v010Commit,
		},
		{
			name:   "annotated",
			start:  gitObj{Type: "tag", SHA: v010TagObject},
			next:   map[string]gitObj{v010TagObject: {Type: "commit", SHA: v010Commit}},
			commit: v010Commit,
			tag:    v010TagObject,
		},
		{
			name:  "nested annotated",
			start: gitObj{Type: "tag", SHA: v010TagObject},
			next: map[string]gitObj{
				v010TagObject: {Type: "tag", SHA: nested},
				nested:        {Type: "commit", SHA: v010Commit},
			},
			commit: v010Commit,
			tag:    v010TagObject,
		},
		{
			name:    "tag object used as commit",
			start:   gitObj{Type: "tag", SHA: v010TagObject},
			next:    map[string]gitObj{v010TagObject: {Type: "commit", SHA: v010TagObject}},
			wantErr: "annotated tag object",
		},
		{
			name:    "blob",
			start:   gitObj{Type: "tag", SHA: v010TagObject},
			next:    map[string]gitObj{v010TagObject: {Type: "blob", SHA: otherCommit}},
			wantErr: "not a commit",
		},
		{
			name:    "cycle",
			start:   gitObj{Type: "tag", SHA: v010TagObject},
			next:    map[string]gitObj{v010TagObject: {Type: "tag", SHA: v010TagObject}},
			wantErr: "cycle",
		},
		{
			name:   "uppercase lightweight",
			start:  gitObj{Type: "Commit", SHA: strings.ToUpper(v010Commit)},
			commit: v010Commit,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			commit, tag, err := peel(tc.start, func(sha string) (gitObj, error) {
				calls++
				obj, ok := tc.next[sha]
				if !ok {
					t.Fatalf("unexpected lookup %s", sha)
				}
				return obj, nil
			})
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if commit != tc.commit || tag != tc.tag {
				t.Fatalf("commit %s tag %s", commit, tag)
			}
			if tc.name == "lightweight" && calls != 0 {
				t.Fatalf("lightweight tag looked up %d annotated objects", calls)
			}
		})
	}
}

func TestSyncAnnotatedReleaseUsesCommit(t *testing.T) {
	// The checked-in file cites v0.1.0. Publish v0.2.0 and require the
	// peeled commit, not the annotated tag object.
	gh := &fakeGitHub{
		tagName: "v0.2.0",
		refType: "tag",
		refSHA:  v010TagObject,
		commit:  otherCommit,
		file:    readRepoFile(t, "CITATION.cff"),
		blob:    strings.Repeat("b", 40),
	}
	srv := gh.server(t)
	defer srv.Close()
	result, err := Sync(context.Background(), gh.config(srv, "v0.2.0", "2026-12-02T01:02:03Z"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Commit != otherCommit || result.TagObject != v010TagObject || result.Version != "0.2.0" || result.Date != "2026-12-02" {
		t.Fatalf("%+v", result)
	}
	if strings.Contains(result.String(), "test-token") {
		t.Fatal("status line contains the token")
	}
	if len(gh.puts) != 1 {
		t.Fatalf("puts %d", len(gh.puts))
	}
	body := gh.puts[0]
	if _, ok := body["force"]; ok {
		t.Fatal("contents update set force")
	}
	for _, key := range []string{"message", "content", "sha", "branch"} {
		if _, ok := body[key]; !ok {
			t.Fatalf("missing %s in %#v", key, body)
		}
	}
	if len(body) != 4 {
		t.Fatalf("unexpected fields %#v", body)
	}
	if body["sha"] != strings.Repeat("b", 40) {
		t.Fatalf("blob sha %v", body["sha"])
	}
	if body["branch"] != "citation/v0.2.0" {
		t.Fatalf("branch %v", body["branch"])
	}
	for _, put := range gh.puts {
		if put["branch"] == "main" {
			t.Fatal("contents update targeted the default branch")
		}
	}
	msg, _ := body["message"].(string)
	if !strings.Contains(msg, "docs: update CITATION.cff for 0.2.0") || !strings.Contains(msg, "The tag is not moved.") || strings.Contains(msg, "force") {
		t.Fatalf("message %q", msg)
	}
	decoded := decodePut(t, body["content"])
	if !bytes.Contains(decoded, []byte("commit: "+otherCommit+"\n")) {
		t.Fatalf("commit line missing:\n%s", decoded)
	}
	if bytes.Contains(decoded, []byte(v010TagObject)) {
		t.Fatal("wrote the annotated tag object into CITATION.cff")
	}
	if !bytes.Contains(decoded, []byte("family-names: Pham\n")) || !bytes.Contains(decoded, []byte("license: Apache-2.0\n")) {
		t.Fatalf("citation identity changed:\n%s", decoded)
	}
	headSHA := strings.Repeat("f", 40)
	if result.Status != StatusMerged || result.HeadBranch != "citation/v0.2.0" || result.HeadSHA != headSHA || result.PullRequest != 7 {
		t.Fatalf("%+v", result)
	}
	if len(gh.refs) != 1 || len(gh.refUpdates) != 0 || len(gh.pulls) != 1 || len(gh.dispatches) != 1 || len(gh.statuses) != 1 || len(gh.merges) != 1 {
		t.Fatalf("refs %d updates %d pulls %d dispatches %d statuses %d merges %d", len(gh.refs), len(gh.refUpdates), len(gh.pulls), len(gh.dispatches), len(gh.statuses), len(gh.merges))
	}
	if gh.refs[0]["ref"] != "refs/heads/citation/v0.2.0" || gh.refs[0]["sha"] != strings.Repeat("e", 40) {
		t.Fatalf("ref create %#v", gh.refs[0])
	}
	if _, ok := gh.refs[0]["force"]; ok || len(gh.refs[0]) != 2 {
		t.Fatalf("ref create %#v", gh.refs[0])
	}
	if !strings.Contains(gh.pulls[0], citationMarker) || !strings.Contains(gh.pulls[0], otherCommit) {
		t.Fatalf("pull body %s", gh.pulls[0])
	}
	if gh.dispatches[0]["ref"] != "citation/v0.2.0" {
		t.Fatalf("dispatch ref %#v", gh.dispatches[0])
	}
	inputs, _ := gh.dispatches[0]["inputs"].(map[string]any)
	if inputs["head_sha"] != headSHA || inputs["base_sha"] != strings.Repeat("e", 40) {
		t.Fatalf("dispatch inputs %#v", inputs)
	}
	if gh.statuses[0]["state"] != "success" || gh.statuses[0]["context"] != gitleaksCheck {
		t.Fatalf("status %#v", gh.statuses[0])
	}
	if gh.merges[0]["merge_method"] != "merge" || gh.merges[0]["sha"] != headSHA {
		t.Fatalf("merge %#v", gh.merges[0])
	}
	if _, ok := gh.merges[0]["force"]; ok || len(gh.merges[0]) != 2 {
		t.Fatalf("merge %#v", gh.merges[0])
	}
	gh.puts = nil
	again, err := Sync(context.Background(), gh.config(srv, "v0.2.0", "2026-12-02T01:02:03Z"))
	if err != nil {
		t.Fatal(err)
	}
	if again.Status != StatusUnchanged || len(gh.puts) != 0 || len(gh.pulls) != 1 || len(gh.refs) != 1 || len(gh.dispatches) != 1 || len(gh.merges) != 1 {
		t.Fatalf("republish %+v puts %d pulls %d refs %d dispatches %d merges %d", again, len(gh.puts), len(gh.pulls), len(gh.refs), len(gh.dispatches), len(gh.merges))
	}
	for _, path := range gh.paths {
		if strings.Contains(path, "/commits/") && !strings.Contains(path, "/statuses") {
			t.Fatalf("used the commits API instead of peeling the tag: %s", path)
		}
	}
}

func TestSyncAlreadyCurrentAnnotatedTag(t *testing.T) {
	gh := &fakeGitHub{
		tagName: "v0.1.0",
		refType: "tag",
		refSHA:  v010TagObject,
		commit:  v010Commit,
		file:    readRepoFile(t, "CITATION.cff"),
		blob:    strings.Repeat("b", 40),
	}
	srv := gh.server(t)
	defer srv.Close()
	result, err := Sync(context.Background(), gh.config(srv, "v0.1.0", "2026-11-24T07:46:11Z"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusUnchanged || result.Commit != v010Commit || result.TagObject != v010TagObject {
		t.Fatalf("%+v", result)
	}
	if len(gh.puts) != 0 {
		t.Fatalf("committed an unchanged file: %d", len(gh.puts))
	}
}

func TestSyncLightweightAndSkips(t *testing.T) {
	file := []byte("version: 0.1.0\ndate-released: 2026-11-24\ncommit: " + v010Commit + "\n")
	gh := &fakeGitHub{
		tagName: "v0.2.0",
		refType: "commit",
		refSHA:  strings.ToUpper(otherCommit),
		commit:  otherCommit,
		file:    file,
		blob:    strings.Repeat("b", 40),
	}
	srv := gh.server(t)
	defer srv.Close()
	cfg := gh.config(srv, "v0.2.0", "2026-12-02T00:00:00Z")
	result, err := Sync(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusMerged || result.Commit != otherCommit || result.TagObject != "" {
		t.Fatalf("%+v", result)
	}
	for _, path := range gh.paths {
		if strings.Contains(path, "/git/tags/") {
			t.Fatalf("lightweight tag fetched an annotated object: %s", path)
		}
	}

	gh.puts = nil
	gh.paths = nil
	cfg.Prerelease = true
	result, err = Sync(context.Background(), cfg)
	if err != nil || result.Status != StatusSkipped || result.Reason != "prerelease" || len(gh.paths) != 0 {
		t.Fatalf("result %+v err %v paths %v", result, err, gh.paths)
	}
	cfg.Prerelease = false
	cfg.Draft = true
	result, err = Sync(context.Background(), cfg)
	if err != nil || result.Status != StatusSkipped || result.Reason != "draft release" || len(gh.paths) != 0 {
		t.Fatalf("result %+v err %v paths %v", result, err, gh.paths)
	}
	cfg.Draft = false
	cfg.Tag = "v0.2.0-rc.1"
	result, err = Sync(context.Background(), cfg)
	if err != nil || result.Status != StatusSkipped || !strings.Contains(result.Reason, "v0.2.0-rc.1") || len(gh.paths) != 0 {
		t.Fatalf("result %+v err %v paths %v", result, err, gh.paths)
	}
}

func TestSyncSkipsOlderAndRetriesConflict(t *testing.T) {
	current := []byte("version: 0.3.0\ndate-released: 2026-12-01\ncommit: " + otherCommit + "\n")
	gh := &fakeGitHub{
		tagName: "v0.2.0",
		refType: "commit",
		refSHA:  v010Commit,
		commit:  v010Commit,
		file:    current,
		blob:    strings.Repeat("b", 40),
	}
	srv := gh.server(t)
	defer srv.Close()
	result, err := Sync(context.Background(), gh.config(srv, "v0.2.0", "2026-11-24T00:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusSkipped || len(gh.puts) != 0 {
		t.Fatalf("%+v puts %d", result, len(gh.puts))
	}

	gh.file = []byte("version: 0.1.0\ndate-released: 2026-11-24\ncommit: " + v010Commit + "\n")
	gh.conflictsLeft = 1
	result, err = Sync(context.Background(), gh.config(srv, "v0.2.0", "2026-12-02T00:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusMerged || len(gh.puts) != 2 {
		t.Fatalf("%+v puts %d", result, len(gh.puts))
	}
	if gh.puts[0]["sha"] != strings.Repeat("b", 40) || gh.puts[1]["sha"] != strings.Repeat("c", 40) {
		t.Fatalf("retry did not reread the blob: %#v then %#v", gh.puts[0]["sha"], gh.puts[1]["sha"])
	}
}

func TestSyncRefusesMissingFileAndRuleset(t *testing.T) {
	gh := &fakeGitHub{
		tagName:     "v0.2.0",
		refType:     "commit",
		refSHA:      otherCommit,
		commit:      otherCommit,
		missingFile: true,
		blob:        strings.Repeat("b", 40),
	}
	srv := gh.server(t)
	defer srv.Close()
	_, err := Sync(context.Background(), gh.config(srv, "v0.2.0", "2026-12-02T00:00:00Z"))
	if err == nil || !strings.Contains(err.Error(), "refusing to create") {
		t.Fatalf("err %v", err)
	}
	if len(gh.puts) != 0 {
		t.Fatal("created CITATION.cff")
	}

	gh.missingFile = false
	gh.file = []byte("version: 0.1.0\ndate-released: 2026-11-24\ncommit: " + v010Commit + "\n")
	gh.putStatus = http.StatusForbidden
	_, err = Sync(context.Background(), gh.config(srv, "v0.2.0", "2026-12-02T00:00:00Z"))
	if err == nil || !strings.Contains(err.Error(), "write CITATION.cff") {
		t.Fatalf("err %v", err)
	}
	if len(gh.puts) != 1 {
		t.Fatalf("retried a refusal: %d", len(gh.puts))
	}
}

func TestSyncStopsAfterRepeatedConflicts(t *testing.T) {
	gh := &fakeGitHub{
		tagName:       "v0.2.0",
		refType:       "commit",
		refSHA:        otherCommit,
		commit:        otherCommit,
		file:          []byte("version: 0.1.0\ndate-released: 2026-11-24\ncommit: " + v010Commit + "\n"),
		blob:          strings.Repeat("b", 40),
		conflictsLeft: 5,
	}
	srv := gh.server(t)
	defer srv.Close()
	_, err := Sync(context.Background(), gh.config(srv, "v0.2.0", "2026-12-02T00:00:00Z"))
	if err == nil {
		t.Fatal("expected a conflict error")
	}
	if len(gh.puts) != contentAttempts {
		t.Fatalf("puts %d", len(gh.puts))
	}
}

func TestSyncRefusesRedirect(t *testing.T) {
	gh := &fakeGitHub{tagName: "v0.2.0", redirectRef: true}
	srv := gh.server(t)
	defer srv.Close()
	cfg := gh.config(srv, "v0.2.0", "2026-12-02T00:00:00Z")
	cfg.Client = githubClient()
	_, err := Sync(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "redirect") {
		t.Fatalf("err %v", err)
	}
	if len(gh.paths) != 1 || strings.Contains(strings.Join(gh.paths, " "), "/stolen") {
		t.Fatalf("paths %v", gh.paths)
	}
}

func TestSyncScanFailureDoesNotMerge(t *testing.T) {
	gh := &fakeGitHub{
		tagName:  "v0.2.0",
		refType:  "commit",
		refSHA:   otherCommit,
		commit:   otherCommit,
		file:     []byte("version: 0.1.0\ndate-released: 2026-11-24\ncommit: " + v010Commit + "\n"),
		blob:     strings.Repeat("b", 40),
		scanFail: true,
	}
	srv := gh.server(t)
	defer srv.Close()
	_, err := Sync(context.Background(), gh.config(srv, "v0.2.0", "2026-12-02T00:00:00Z"))
	if err == nil || !strings.Contains(err.Error(), "finished with failure") {
		t.Fatalf("err %v", err)
	}
	if len(gh.dispatches) != 1 || len(gh.statuses) != 0 || len(gh.merges) != 0 || len(gh.pulls) != 1 {
		t.Fatalf("dispatches %d statuses %d merges %d pulls %d", len(gh.dispatches), len(gh.statuses), len(gh.merges), len(gh.pulls))
	}
}

func TestMergeOnlyCitationPR(t *testing.T) {
	sha := otherCommit
	older := "2026-11-01T00:00:00Z"
	newer := "2026-11-24T00:00:00Z"
	type want struct {
		status   Status
		reason   string
		err      string
		method   string
		mergeSHA string
	}
	cases := []struct {
		name     string
		head     string
		sha      string
		body     string
		files    []map[string]string
		statuses []map[string]string
		merge    int
		merged   bool
		httpOff  bool
		want     want
	}{
		{
			name:    "wrong branch",
			head:    "citation/vuln",
			sha:     sha,
			httpOff: true,
			want:    want{status: StatusSkipped, reason: "not a citation branch"},
		},
		{
			name:    "prerelease branch",
			head:    "citation/v0.2.0-rc.1",
			sha:     sha,
			httpOff: true,
			want:    want{status: StatusSkipped, reason: "not a citation branch"},
		},
		{
			name: "missing marker",
			head: "citation/v0.2.0",
			sha:  sha,
			body: "please merge",
			want: want{status: StatusSkipped, reason: "not opened by the citation updater"},
		},
		{
			name:  "extra file",
			head:  "citation/v0.2.0",
			sha:   sha,
			body:  citationMarker,
			files: []map[string]string{{"filename": "CITATION.cff", "status": "modified"}, {"filename": "README.md", "status": "modified"}},
			want:  want{err: "does not change only CITATION.cff"},
		},
		{
			name:  "removed citation",
			head:  "citation/v0.2.0",
			sha:   sha,
			body:  citationMarker,
			files: []map[string]string{{"filename": "CITATION.cff", "status": "removed"}},
			want:  want{err: "does not change only CITATION.cff"},
		},
		{
			name:    "bad sha",
			head:    "citation/v0.2.0",
			sha:     "HEAD",
			httpOff: true,
			want:    want{err: "not 40 lowercase hex"},
		},
		{
			name:     "gitleaks failed",
			head:     "citation/v0.2.0",
			sha:      sha,
			body:     citationMarker,
			files:    []map[string]string{{"filename": "CITATION.cff", "status": "modified"}},
			statuses: []map[string]string{{"context": gitleaksCheck, "state": "failure", "created_at": newer}},
			want:     want{status: StatusSkipped, reason: "has not passed"},
		},
		{
			name:     "gitleaks missing",
			head:     "citation/v0.2.0",
			sha:      sha,
			body:     citationMarker,
			files:    []map[string]string{{"filename": "CITATION.cff", "status": "modified"}},
			statuses: nil,
			want:     want{status: StatusSkipped, reason: "has not passed"},
		},
		{
			name:     "older success does not beat a newer failure",
			head:     "citation/v0.2.0",
			sha:      sha,
			body:     citationMarker,
			files:    []map[string]string{{"filename": "CITATION.cff", "status": "modified"}},
			statuses: []map[string]string{{"context": gitleaksCheck, "state": "success", "created_at": older}, {"context": gitleaksCheck, "state": "failure", "created_at": newer}},
			want:     want{status: StatusSkipped, reason: "has not passed"},
		},
		{
			name:     "merge after the latest check succeeds",
			head:     "citation/v0.2.0",
			sha:      sha,
			body:     citationMarker + "\n",
			files:    []map[string]string{{"filename": "CITATION.cff", "status": "modified"}},
			statuses: []map[string]string{{"context": "other", "state": "failure", "created_at": newer}, {"context": gitleaksCheck, "state": "failure", "created_at": older}, {"context": gitleaksCheck, "state": "success", "created_at": newer}},
			want:     want{status: StatusMerged, method: "merge", mergeSHA: sha},
		},
		{
			name:     "already merged",
			head:     "citation/v0.2.0",
			sha:      sha,
			body:     citationMarker,
			files:    []map[string]string{{"filename": "CITATION.cff", "status": "modified"}},
			statuses: []map[string]string{{"context": gitleaksCheck, "state": "success", "created_at": newer}},
			merge:    http.StatusMethodNotAllowed,
			merged:   true,
			want:     want{status: StatusMerged, method: "merge", mergeSHA: sha},
		},
		{
			name:     "refuses an unmerged conflict",
			head:     "citation/v0.2.0",
			sha:      sha,
			body:     citationMarker,
			files:    []map[string]string{{"filename": "CITATION.cff", "status": "modified"}},
			statuses: []map[string]string{{"context": gitleaksCheck, "state": "success", "created_at": newer}},
			merge:    http.StatusConflict,
			want:     want{err: "merge citation pull request"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var merges []map[string]any
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if tc.httpOff {
					t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
					return
				}
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/repos/paulpham157/sdf/pulls":
					writeJSON(t, w, []map[string]any{{
						"number":  7,
						"node_id": "PR_node_citation",
						"state":   "open",
						"body":    tc.body,
						"head":    map[string]string{"ref": tc.head, "sha": tc.sha},
						"base":    map[string]string{"ref": "main"},
					}})
				case r.Method == http.MethodGet && r.URL.Path == "/repos/paulpham157/sdf/pulls/7/files":
					writeJSON(t, w, tc.files)
				case r.Method == http.MethodGet && r.URL.Path == "/repos/paulpham157/sdf/commits/"+tc.sha+"/statuses":
					if tc.statuses == nil {
						writeJSON(t, w, []any{})
						return
					}
					writeJSON(t, w, tc.statuses)
				case r.Method == http.MethodPut && r.URL.Path == "/repos/paulpham157/sdf/pulls/7/merge":
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Errorf("merge body: %v", err)
					}
					merges = append(merges, body)
					if tc.merge != 0 {
						w.WriteHeader(tc.merge)
						_, _ = w.Write([]byte(`{"message":"not merged"}`))
						return
					}
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(`{"merged":true}`))
				case r.Method == http.MethodGet && r.URL.Path == "/repos/paulpham157/sdf/pulls/7":
					writeJSON(t, w, map[string]any{"number": 7, "merged": tc.merged})
				default:
					t.Errorf("unexpected %s %s", r.Method, r.URL.RequestURI())
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			result, err := Merge(context.Background(), MergeConfig{
				Token:      "test-token",
				Repo:       "paulpham157/sdf",
				APIBase:    srv.URL,
				BaseBranch: "main",
				HeadBranch: tc.head,
				HeadSHA:    tc.sha,
				Client:     srv.Client(),
			})
			if tc.want.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.want.err) {
					t.Fatalf("err %v", err)
				}
				if len(merges) != 0 && tc.name == "extra file" {
					t.Fatal("merged a pull request that changes more than CITATION.cff")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != tc.want.status || !strings.Contains(result.Reason, tc.want.reason) {
				t.Fatalf("%+v", result)
			}
			if tc.httpOff && calls != 0 {
				t.Fatalf("calls %d", calls)
			}
			if tc.want.method != "" {
				if len(merges) != 1 || merges[0]["merge_method"] != tc.want.method || merges[0]["sha"] != tc.want.mergeSHA {
					t.Fatalf("merge %#v", merges)
				}
				if _, ok := merges[0]["force"]; ok || len(merges[0]) != 2 {
					t.Fatalf("merge %#v", merges)
				}
			} else if len(merges) != 0 {
				t.Fatalf("merged unexpectedly %#v", merges)
			}
		})
	}

	noPR := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/paulpham157/sdf/pulls" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		writeJSON(t, w, []any{})
	}))
	defer noPR.Close()
	result, err := Merge(context.Background(), MergeConfig{
		Token:      "test-token",
		Repo:       "paulpham157/sdf",
		APIBase:    noPR.URL,
		BaseBranch: "main",
		HeadBranch: "citation/v0.2.0",
		HeadSHA:    sha,
		Client:     noPR.Client(),
	})
	if err != nil || result.Status != StatusSkipped || !strings.Contains(result.Reason, "no open") {
		t.Fatalf("%+v err %v", result, err)
	}
}

func TestSyncRefusesForeignCitationBranch(t *testing.T) {
	old := []byte("version: 0.1.0\ndate-released: 2026-11-24\ncommit: " + v010Commit + "\n")
	foreign := strings.Repeat("a", 40)
	cases := []struct {
		name    string
		status  string
		ahead   int
		behind  int
		total   int
		next    bool
		commits []map[string]any
		want    string
		patch   bool
		puts    int
	}{
		{
			name:    "foreign commit",
			status:  "ahead",
			ahead:   1,
			total:   1,
			commits: []map[string]any{foreignCommitJSON(foreign, strings.Repeat("e", 40))},
			want:    "not authored by the citation workflow",
		},
		{
			name:    "unverified workflow commit",
			status:  "ahead",
			ahead:   1,
			total:   1,
			commits: []map[string]any{workflowCommitJSON(foreign, strings.Repeat("e", 40), false)},
			want:    "not a verified GitHub commit",
		},
		{
			name:    "diverged from the default branch",
			status:  "diverged",
			ahead:   1,
			behind:  2,
			total:   1,
			commits: []map[string]any{workflowCommitJSON(foreign, strings.Repeat("1", 40), true)},
			want:    "not based on",
		},
		{
			name:   "compare has another page",
			status: "ahead",
			ahead:  1,
			total:  1,
			next:   true,
			commits: []map[string]any{
				workflowCommitJSON(foreign, strings.Repeat("e", 40), true),
			},
			want: "incomplete",
		},
		{
			name:    "compare truncated",
			status:  "ahead",
			ahead:   3,
			total:   3,
			commits: []map[string]any{workflowCommitJSON(foreign, strings.Repeat("e", 40), true)},
			want:    "incomplete",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gh := &fakeGitHub{
				tagName:        "v0.2.0",
				refType:        "commit",
				refSHA:         otherCommit,
				commit:         otherCommit,
				file:           old,
				blob:           strings.Repeat("b", 40),
				citationExists: true,
				citationSHA:    foreign,
				citationFile:   old,
				citationBlob:   strings.Repeat("b", 40),
				compareStatus:  tc.status,
				compareAhead:   tc.ahead,
				compareBehind:  tc.behind,
				compareTotal:   tc.total,
				compareCommits: tc.commits,
				compareNext:    tc.next,
			}
			srv := gh.server(t)
			defer srv.Close()
			_, err := Sync(context.Background(), gh.config(srv, "v0.2.0", "2026-12-02T00:00:00Z"))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err %v", err)
			}
			if len(gh.puts) != tc.puts || len(gh.refs) != 0 || len(gh.refUpdates) != 0 || len(gh.merges) != 0 || len(gh.statuses) != 0 {
				t.Fatalf("puts %d refs %d updates %d merges %d statuses %d", len(gh.puts), len(gh.refs), len(gh.refUpdates), len(gh.merges), len(gh.statuses))
			}
		})
	}
}

func TestSyncRefusesBranchThatAppearsDuringCreate(t *testing.T) {
	old := []byte("version: 0.1.0\ndate-released: 2026-11-24\ncommit: " + v010Commit + "\n")
	foreign := strings.Repeat("a", 40)
	gh := &fakeGitHub{
		tagName:        "v0.2.0",
		refType:        "commit",
		refSHA:         otherCommit,
		commit:         otherCommit,
		file:           old,
		blob:           strings.Repeat("b", 40),
		citationSHA:    foreign,
		citationFile:   old,
		citationBlob:   strings.Repeat("b", 40),
		createConflict: true,
		compareStatus:  "ahead",
		compareAhead:   1,
		compareTotal:   1,
		compareCommits: []map[string]any{foreignCommitJSON(foreign, strings.Repeat("e", 40))},
	}
	srv := gh.server(t)
	defer srv.Close()
	_, err := Sync(context.Background(), gh.config(srv, "v0.2.0", "2026-12-02T00:00:00Z"))
	if err == nil || !strings.Contains(err.Error(), "not authored by the citation workflow") {
		t.Fatalf("err %v", err)
	}
	if len(gh.puts) != 0 || len(gh.refUpdates) != 0 || len(gh.merges) != 0 || len(gh.statuses) != 0 {
		t.Fatalf("puts %d updates %d merges %d statuses %d", len(gh.puts), len(gh.refUpdates), len(gh.merges), len(gh.statuses))
	}
	if len(gh.refs) != 1 {
		t.Fatalf("refs %#v", gh.refs)
	}
	if _, ok := gh.refs[0]["force"]; ok {
		t.Fatal("create set force")
	}
}

func TestSyncReusesWorkflowBranchAndFastForwardsBehind(t *testing.T) {
	old := []byte("version: 0.1.0\ndate-released: 2026-11-24\ncommit: " + v010Commit + "\n")
	tip := strings.Repeat("a", 40)
	base := strings.Repeat("e", 40)

	t.Run("ahead by a workflow commit", func(t *testing.T) {
		gh := &fakeGitHub{
			tagName:        "v0.2.0",
			refType:        "commit",
			refSHA:         otherCommit,
			commit:         otherCommit,
			file:           old,
			blob:           strings.Repeat("b", 40),
			citationExists: true,
			citationSHA:    tip,
			citationFile:   old,
			citationBlob:   strings.Repeat("b", 40),
			compareStatus:  "ahead",
			compareAhead:   1,
			compareTotal:   1,
			compareCommits: []map[string]any{workflowCommitJSON(tip, base, true)},
		}
		srv := gh.server(t)
		defer srv.Close()
		result, err := Sync(context.Background(), gh.config(srv, "v0.2.0", "2026-12-02T00:00:00Z"))
		if err != nil {
			t.Fatal(err)
		}
		if result.Status != StatusMerged || len(gh.refs) != 0 || len(gh.refUpdates) != 0 || len(gh.puts) != 1 {
			t.Fatalf("%+v refs %d updates %d puts %d", result, len(gh.refs), len(gh.refUpdates), len(gh.puts))
		}
	})

	t.Run("behind the default branch fast-forwards", func(t *testing.T) {
		gh := &fakeGitHub{
			tagName:        "v0.2.0",
			refType:        "commit",
			refSHA:         otherCommit,
			commit:         otherCommit,
			file:           old,
			blob:           strings.Repeat("b", 40),
			mainSHA:        base,
			citationExists: true,
			citationSHA:    strings.Repeat("1", 40),
			citationFile:   old,
			citationBlob:   strings.Repeat("b", 40),
			compareStatus:  "behind",
			compareBehind:  4,
		}
		srv := gh.server(t)
		defer srv.Close()
		result, err := Sync(context.Background(), gh.config(srv, "v0.2.0", "2026-12-02T00:00:00Z"))
		if err != nil {
			t.Fatal(err)
		}
		if result.Status != StatusMerged || len(gh.refs) != 0 || len(gh.refUpdates) != 1 {
			t.Fatalf("%+v refs %d updates %#v", result, len(gh.refs), gh.refUpdates)
		}
		if gh.refUpdates[0]["sha"] != base || len(gh.refUpdates[0]) != 1 {
			t.Fatalf("fast-forward %#v", gh.refUpdates[0])
		}
		if _, ok := gh.refUpdates[0]["force"]; ok {
			t.Fatal("fast-forward set force")
		}
	})

	t.Run("identical branch is not recreated", func(t *testing.T) {
		gh := &fakeGitHub{
			tagName:        "v0.2.0",
			refType:        "commit",
			refSHA:         otherCommit,
			commit:         otherCommit,
			file:           old,
			blob:           strings.Repeat("b", 40),
			mainSHA:        base,
			citationExists: true,
			citationSHA:    base,
			citationFile:   old,
			citationBlob:   strings.Repeat("b", 40),
		}
		srv := gh.server(t)
		defer srv.Close()
		result, err := Sync(context.Background(), gh.config(srv, "v0.2.0", "2026-12-02T00:00:00Z"))
		if err != nil {
			t.Fatal(err)
		}
		if result.Status != StatusMerged || len(gh.refs) != 0 || len(gh.refUpdates) != 0 || len(gh.puts) != 1 {
			t.Fatalf("%+v refs %d updates %d puts %d", result, len(gh.refs), len(gh.refUpdates), len(gh.puts))
		}
		for _, path := range gh.paths {
			if strings.Contains(path, "/compare/") {
				t.Fatalf("identical branch was compared: %s", path)
			}
		}
	})
}

func TestSyncDoesNotPostStatusWithoutConfirmedRun(t *testing.T) {
	old := []byte("version: 0.1.0\ndate-released: 2026-11-24\ncommit: " + v010Commit + "\n")
	cases := []struct {
		name string
		set  func(*fakeGitHub)
		want string
	}{
		{
			name: "run head is a different commit",
			set:  func(gh *fakeGitHub) { gh.runHeadSHA = otherCommit },
			want: "not " + strings.Repeat("f", 40),
		},
		{
			name: "run conclusion is not success",
			set:  func(gh *fakeGitHub) { gh.runConclusion = "failure" },
			want: "completed/failure",
		},
		{
			name: "run is a different workflow",
			set:  func(gh *fakeGitHub) { gh.runPath = ".github/workflows/pr.yml" },
			want: "is not " + citationWorkflowPath,
		},
		{
			name: "run lookup is missing",
			set:  func(gh *fakeGitHub) { gh.runMissing = true },
			want: "not found",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gh := &fakeGitHub{
				tagName: "v0.2.0",
				refType: "commit",
				refSHA:  otherCommit,
				commit:  otherCommit,
				file:    old,
				blob:    strings.Repeat("b", 40),
			}
			tc.set(gh)
			srv := gh.server(t)
			defer srv.Close()
			_, err := Sync(context.Background(), gh.config(srv, "v0.2.0", "2026-12-02T00:00:00Z"))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err %v", err)
			}
			if len(gh.statuses) != 0 || len(gh.merges) != 0 {
				t.Fatalf("statuses %d merges %d", len(gh.statuses), len(gh.merges))
			}
		})
	}
}

func TestCitationPRFilesFollowsPagesOrFailsClosed(t *testing.T) {
	one := []map[string]string{{"filename": "CITATION.cff", "status": "modified"}}
	extra := []map[string]string{{"filename": "README.md", "status": "modified"}}
	type page struct {
		files []map[string]string
		link  string
	}
	cases := []struct {
		name  string
		pages []page
		want  string
		calls int
	}{
		{
			name:  "second page has another file",
			pages: []page{{one, "next"}, {extra, ""}},
			want:  "does not change only CITATION.cff",
			calls: 2,
		},
		{
			name:  "empty second page still counts the first",
			pages: []page{{one, "next"}, {nil, ""}},
			calls: 2,
		},
		{
			name:  "next link on another host",
			pages: []page{{one, "https://evil.example/files?page=2"}},
			want:  "another host",
			calls: 1,
		},
		{
			name:  "malformed link",
			pages: []page{{one, "not-a-link"}},
			want:  "malformed",
			calls: 1,
		},
		{
			name:  "repeated page",
			pages: []page{{one, "repeat"}},
			want:  "repeated a page",
			calls: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			var srv *httptest.Server
			srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != "/repos/paulpham157/sdf/pulls/7/files" {
					t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
					return
				}
				page := 1
				if r.URL.Query().Get("page") == "2" {
					page = 2
				}
				if page > len(tc.pages) {
					t.Errorf("page %d", page)
					http.NotFound(w, r)
					return
				}
				got := tc.pages[page-1]
				switch got.link {
				case "":
				case "next":
					w.Header().Set("Link", `<`+srv.URL+`/repos/paulpham157/sdf/pulls/7/files?per_page=100&page=2>; rel="next"`)
				case "repeat":
					w.Header().Set("Link", `<`+srv.URL+r.URL.RequestURI()+`>; rel="next"`)
				case "not-a-link":
					w.Header().Set("Link", `not-a-link`)
				default:
					w.Header().Set("Link", `<`+got.link+`>; rel="next"`)
				}
				files := got.files
				if files == nil {
					files = []map[string]string{}
				}
				writeJSON(t, w, files)
			}))
			defer srv.Close()
			client := srv.Client()
			client.Timeout = 2 * time.Second
			err := citationPRFiles(context.Background(), client, Config{
				Token:   "test-token",
				Repo:    "paulpham157/sdf",
				APIBase: srv.URL,
				Branch:  "main",
			}, "paulpham157", "sdf", 7)
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err %v", err)
			}
			if calls != tc.calls {
				t.Fatalf("calls %d", calls)
			}
		})
	}

	t.Run("too many pages", func(t *testing.T) {
		calls := 0
		var srv *httptest.Server
		srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			page := calls + 1
			w.Header().Set("Link", fmt.Sprintf(`<%s/repos/paulpham157/sdf/pulls/7/files?page=%d>; rel="next"`, srv.URL, page))
			writeJSON(t, w, []map[string]string{{"filename": "CITATION.cff", "status": "modified"}})
		}))
		defer srv.Close()
		err := citationPRFiles(context.Background(), srv.Client(), Config{
			Token:   "test-token",
			APIBase: srv.URL,
		}, "paulpham157", "sdf", 7)
		if err == nil || !strings.Contains(err.Error(), "too many pages") {
			t.Fatalf("err %v", err)
		}
		if calls != maxCitationFilePages {
			t.Fatalf("calls %d", calls)
		}
	})
}

func workflowCommitJSON(sha, parent string, verified bool) map[string]any {
	reason := "valid"
	if !verified {
		reason = "unsigned"
	}
	return map[string]any{
		"sha": sha,
		"parents": []map[string]string{{
			"sha": parent,
		}},
		"author":    map[string]string{"login": githubActionsBot, "type": "Bot"},
		"committer": map[string]string{"login": githubActionsBot, "type": "Bot"},
		"commit": map[string]any{
			"message": "docs: update CITATION.cff for 0.2.0\n\nSet version 0.2.0.\n",
			"author": map[string]string{
				"name":  githubActionsBot,
				"email": githubActionsEmail,
			},
			"committer": map[string]string{
				"name":  githubActionsBot,
				"email": githubActionsEmail,
			},
			"verification": map[string]any{"verified": verified, "reason": reason},
		},
	}
}

func foreignCommitJSON(sha, parent string) map[string]any {
	commit := workflowCommitJSON(sha, parent, true)
	commit["author"] = map[string]string{"login": "octocat", "type": "User"}
	commit["committer"] = map[string]string{"login": "octocat", "type": "User"}
	return commit
}

func TestConfigFromEnv(t *testing.T) {
	env := map[string]string{
		"GITHUB_TOKEN":          "test-token",
		"GITHUB_REPOSITORY":     "paulpham157/sdf",
		"CITATION_TAG":          "v0.2.0",
		"CITATION_PUBLISHED_AT": "2026-12-02T00:00:00Z",
		"CITATION_BRANCH":       "main",
		"CITATION_PRERELEASE":   "false",
		"CITATION_DRAFT":        "false",
	}
	cfg, err := configFromEnv(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.APIBase != "https://api.github.com" || cfg.Tag != "v0.2.0" || cfg.Prerelease || cfg.Draft {
		t.Fatalf("%+v", cfg)
	}
	env["GITHUB_API_URL"] = "http://169.254.169.254/"
	if _, err := configFromEnv(func(k string) string { return env[k] }); err == nil {
		t.Fatal("accepted an http API URL")
	}
	env["GITHUB_API_URL"] = "https://api.github.com/extra"
	if _, err := configFromEnv(func(k string) string { return env[k] }); err == nil {
		t.Fatal("accepted an API URL with a path")
	}
	env["GITHUB_API_URL"] = "https://user:pass@api.github.com"
	if _, err := configFromEnv(func(k string) string { return env[k] }); err == nil {
		t.Fatal("accepted credentials in the API URL")
	}
	delete(env, "GITHUB_API_URL")
	env["CITATION_DRAFT"] = "yes"
	if _, err := configFromEnv(func(k string) string { return env[k] }); err == nil {
		t.Fatal("accepted a non-boolean draft flag")
	}
	err = run(func(string) string { return "" })
	if err == nil {
		t.Fatal("run accepted an empty environment")
	}
}

func TestWorkflowFiles(t *testing.T) {
	root := moduleRoot(t)
	citation := string(readRepoFile(t, ".github/workflows/citation.yml"))
	for _, want := range []string{
		"types: [published]",
		"github.event.action == 'published'",
		"github.event.release.draft == false",
		"github.event.release.prerelease == false",
		"persist-credentials: false",
		"contents: write",
		"pull-requests: write",
		"actions: write",
		"statuses: write",
		"permissions: {}",
		"github.event.repository.default_branch",
		"go run ./internal/citeupdate",
		"actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1",
		"actions/setup-go@d8cbb8045d7f43706b9177cceb36c1f91a6644d5 # v7.1.0",
	} {
		if !strings.Contains(citation, want) {
			t.Fatalf("citation.yml missing %q", want)
		}
	}
	for _, banned := range []string{"git push", "--force", "force:", "workflow_dispatch", "pull_request:"} {
		if strings.Contains(citation, banned) {
			t.Fatalf("citation.yml contains %q", banned)
		}
	}
	scanWF := string(readRepoFile(t, ".github/workflows/citation-gitleaks.yml"))
	for _, want := range []string{
		"workflow_dispatch:",
		"base_sha:",
		"head_sha:",
		"permissions: {}",
		"contents: read",
		"persist-credentials: false",
		"fetch-depth: 1",
		"GITLEAKS_EVENT: pull_request",
		"GITLEAKS_BASE_SHA:",
		"GITLEAKS_HEAD_SHA:",
		"bash .github/scripts/gitleaks-scan.sh",
		"name: scan citation range",
		`^refs/heads/citation/v[0-9]+\.[0-9]+\.[0-9]+$`,
	} {
		if !strings.Contains(scanWF, want) {
			t.Fatalf("citation-gitleaks.yml missing %q", want)
		}
	}
	for _, banned := range []string{"git push", "--force", "force:", "pull-requests: write", "name: gitleaks (full history)", "statuses: write"} {
		if strings.Contains(scanWF, banned) {
			t.Fatalf("citation-gitleaks.yml contains %q", banned)
		}
	}
	prCI := string(readRepoFile(t, ".github/workflows/pr.yml"))
	for _, want := range []string{
		"name: PR CI",
		"pull_request:",
		"name: gitleaks (full history)",
		"GITLEAKS_EVENT: pull_request",
		"bash .github/scripts/gitleaks-scan.sh",
	} {
		if !strings.Contains(prCI, want) {
			t.Fatalf("pr.yml missing %q", want)
		}
	}
	if _, err := os.Stat(filepath.Join(root, ".github/workflows/citation-merge.yml")); !os.IsNotExist(err) {
		t.Fatal("citation-merge.yml waits for PR CI, which a GITHUB_TOKEN pull request does not run")
	}
	release := string(readRepoFile(t, ".github/workflows/release.yml"))
	if !strings.Contains(release, "\non:\n  push:\n") || strings.Contains(release, "types: [published]") {
		t.Fatal("release.yml no longer only runs on a tag push")
	}
	gore := string(readRepoFile(t, ".goreleaser.yaml"))
	if !strings.Contains(gore, "draft: true") || !strings.Contains(gore, "prerelease: auto") {
		t.Fatal("goreleaser no longer creates a draft")
	}
	if _, err := os.Stat(filepath.Join(root, "internal/citeupdate/main.go")); err != nil {
		t.Fatal(err)
	}
}

type fakeGitHub struct {
	tagName        string
	refType        string
	refSHA         string
	commit         string
	file           []byte
	blob           string
	puts           []map[string]any
	paths          []string
	refs           []map[string]any
	pulls          []string
	dispatches     []map[string]any
	statuses       []map[string]any
	merges         []map[string]any
	conflictsLeft  int
	putStatus      int
	missingFile    bool
	redirectRef    bool
	scanFail       bool
	runHeadSHA     string
	runHeadBranch  string
	runConclusion  string
	runPath        string
	runEvent       string
	runStatus      string
	runMissing     bool
	compareStatus  string
	compareAhead   int
	compareBehind  int
	compareTotal   int
	compareCommits []map[string]any
	compareNext    bool
	createConflict bool
	refUpdates     []map[string]any
	citationExists bool
	citationFile   []byte
	citationBlob   string
	citationSHA    string
	mainSHA        string
	pullMerged     bool
	dispatchCount  int
}

func (f *fakeGitHub) config(srv *httptest.Server, tag, published string) Config {
	return Config{
		Token:       "test-token",
		Repo:        "paulpham157/sdf",
		APIBase:     srv.URL,
		Tag:         tag,
		PublishedAt: published,
		Branch:      "main",
		Client:      srv.Client(),
	}
}

func (f *fakeGitHub) server(t *testing.T) *httptest.Server {
	t.Helper()
	if f.mainSHA == "" {
		f.mainSHA = strings.Repeat("e", 40)
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("authorization %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("User-Agent") != userAgent {
			t.Errorf("user agent %q", r.Header.Get("User-Agent"))
		}
		if strings.Contains(r.URL.RequestURI(), "test-token") {
			t.Errorf("token leaked into %s", r.URL.RequestURI())
		}
		f.paths = append(f.paths, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/paulpham157/sdf/git/ref/tags/"+f.tagName:
			if f.redirectRef {
				http.Redirect(w, r, "/stolen", http.StatusFound)
				return
			}
			writeJSON(t, w, map[string]any{"object": map[string]string{"type": f.refType, "sha": f.refSHA}})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/paulpham157/sdf/git/tags/"+f.refSHA:
			if f.refType != "tag" {
				t.Errorf("looked up an annotated tag for a %s ref", f.refType)
				http.NotFound(w, r)
				return
			}
			writeJSON(t, w, map[string]any{"object": map[string]string{"type": "commit", "sha": f.commit}})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/paulpham157/sdf/git/ref/heads/main":
			writeJSON(t, w, map[string]any{"object": map[string]string{"type": "commit", "sha": f.mainSHA}})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/repos/paulpham157/sdf/compare/"):
			if f.compareStatus == "" {
				t.Errorf("unexpected compare %s", r.URL.Path)
				http.NotFound(w, r)
				return
			}
			if f.compareNext {
				w.Header().Set("Link", `<http://`+r.Host+`/repos/paulpham157/sdf/compare/next>; rel="next"`)
			}
			commits := f.compareCommits
			if commits == nil {
				commits = []map[string]any{}
			}
			writeJSON(t, w, map[string]any{
				"status":        f.compareStatus,
				"ahead_by":      f.compareAhead,
				"behind_by":     f.compareBehind,
				"total_commits": f.compareTotal,
				"commits":       commits,
			})
		case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/repos/paulpham157/sdf/git/refs/heads/citation/"):
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("ref update: %v", err)
				http.Error(w, `{"message":"bad json"}`, http.StatusBadRequest)
				return
			}
			f.refUpdates = append(f.refUpdates, body)
			if _, ok := body["force"]; ok {
				t.Errorf("ref update set force")
			}
			sha, _ := body["sha"].(string)
			f.citationSHA = sha
			f.citationExists = true
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ref":"ok"}`))
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/repos/paulpham157/sdf/git/ref/heads/citation/"):
			if !f.citationExists {
				http.NotFound(w, r)
				return
			}
			sha := f.citationSHA
			if sha == "" {
				sha = f.mainSHA
			}
			writeJSON(t, w, map[string]any{"object": map[string]string{"type": "commit", "sha": sha}})
		case r.Method == http.MethodPost && r.URL.Path == "/repos/paulpham157/sdf/git/refs":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("ref body: %v", err)
				http.Error(w, `{"message":"bad json"}`, http.StatusBadRequest)
				return
			}
			f.refs = append(f.refs, body)
			if _, ok := body["force"]; ok {
				t.Errorf("ref create set force")
			}
			if f.createConflict {
				f.citationExists = true
				w.WriteHeader(http.StatusUnprocessableEntity)
				_, _ = w.Write([]byte(`{"message":"Reference already exists"}`))
				return
			}
			f.citationExists = true
			f.citationFile = append([]byte(nil), f.file...)
			f.citationBlob = f.blob
			f.citationSHA = f.mainSHA
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"ref":"ok"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/repos/paulpham157/sdf/contents/CITATION.cff":
			ref := r.URL.Query().Get("ref")
			if ref == "main" {
				if f.missingFile {
					http.NotFound(w, r)
					return
				}
				writeJSON(t, w, map[string]any{
					"sha":      f.blob,
					"encoding": "base64",
					"type":     "file",
					"content":  wrapBase64(f.file),
				})
				return
			}
			if !strings.HasPrefix(ref, "citation/") || !f.citationExists {
				http.NotFound(w, r)
				return
			}
			writeJSON(t, w, map[string]any{
				"sha":      f.citationBlob,
				"encoding": "base64",
				"type":     "file",
				"content":  wrapBase64(f.citationFile),
			})
		case r.Method == http.MethodPut && r.URL.Path == "/repos/paulpham157/sdf/contents/CITATION.cff":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("put body: %v", err)
				http.Error(w, `{"message":"bad json"}`, http.StatusBadRequest)
				return
			}
			f.puts = append(f.puts, body)
			if body["branch"] == "main" || !strings.HasPrefix(fmt.Sprint(body["branch"]), "citation/") {
				t.Errorf("contents update targeted %v", body["branch"])
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"message":"refusing default branch"}`))
				return
			}
			if f.conflictsLeft > 0 {
				f.conflictsLeft--
				f.citationBlob = strings.Repeat("c", 40)
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(`{"message":"sha mismatch"}`))
				return
			}
			if f.putStatus != 0 {
				w.WriteHeader(f.putStatus)
				_, _ = w.Write([]byte(`{"message":"refused by ruleset"}`))
				return
			}
			decoded := decodePut(t, body["content"])
			f.citationFile = decoded
			f.citationBlob = strings.Repeat("d", 40)
			f.citationSHA = strings.Repeat("f", 40)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"content":{"sha":"` + f.citationBlob + `"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/repos/paulpham157/sdf/pulls":
			if len(f.pulls) == 0 || f.pullMerged {
				writeJSON(t, w, []any{})
				return
			}
			writeJSON(t, w, []map[string]any{f.pullJSON()})
		case r.Method == http.MethodPost && r.URL.Path == "/repos/paulpham157/sdf/pulls":
			var body struct {
				Title string `json:"title"`
				Head  string `json:"head"`
				Base  string `json:"base"`
				Body  string `json:"body"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("pull body: %v", err)
				http.Error(w, `{"message":"bad json"}`, http.StatusBadRequest)
				return
			}
			if len(f.pulls) != 0 {
				t.Errorf("opened a duplicate pull request")
				w.WriteHeader(http.StatusUnprocessableEntity)
				_, _ = w.Write([]byte(`{"message":"A pull request already exists"}`))
				return
			}
			f.pulls = append(f.pulls, body.Body)
			writeJSON(t, w, f.pullJSON())
		case r.Method == http.MethodGet && r.URL.Path == "/repos/paulpham157/sdf/pulls/7":
			pr := f.pullJSON()
			pr["merged"] = f.pullMerged
			if f.pullMerged {
				pr["state"] = "closed"
			}
			writeJSON(t, w, pr)
		case r.Method == http.MethodGet && r.URL.Path == "/repos/paulpham157/sdf/pulls/7/files":
			writeJSON(t, w, []map[string]string{{"filename": "CITATION.cff", "status": "modified"}})
		case r.Method == http.MethodPut && r.URL.Path == "/repos/paulpham157/sdf/pulls/7/merge":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("merge body: %v", err)
				http.Error(w, `{"message":"bad json"}`, http.StatusBadRequest)
				return
			}
			f.merges = append(f.merges, body)
			if body["merge_method"] != "merge" || body["sha"] != f.citationSHA {
				t.Errorf("merge %#v citation %s", body, f.citationSHA)
			}
			f.pullMerged = true
			f.file = append([]byte(nil), f.citationFile...)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"merged":true}`))
		case r.Method == http.MethodPost && r.URL.Path == "/repos/paulpham157/sdf/actions/workflows/citation-gitleaks.yml/dispatches":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("dispatch body: %v", err)
				http.Error(w, `{"message":"bad json"}`, http.StatusBadRequest)
				return
			}
			f.dispatches = append(f.dispatches, body)
			f.dispatchCount++
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/repos/paulpham157/sdf/actions/workflows/citation-gitleaks.yml/runs":
			if f.dispatchCount == 0 {
				writeJSON(t, w, map[string]any{"workflow_runs": []any{}})
				return
			}
			conclusion := "success"
			if f.scanFail {
				conclusion = "failure"
			}
			writeJSON(t, w, map[string]any{"workflow_runs": []map[string]any{{
				"id":         2,
				"head_sha":   r.URL.Query().Get("head_sha"),
				"event":      "workflow_dispatch",
				"status":     "completed",
				"conclusion": conclusion,
				"html_url":   "https://example.test/runs/2",
				"created_at": "2026-11-24T00:00:01Z",
			}}})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/paulpham157/sdf/actions/runs/2":
			if f.runMissing {
				http.NotFound(w, r)
				return
			}
			sha := f.citationSHA
			if f.runHeadSHA != "" {
				sha = f.runHeadSHA
			}
			conclusion := "success"
			if f.runConclusion != "" {
				conclusion = f.runConclusion
			}
			headBranch := "citation/" + f.tagName
			if f.runHeadBranch != "" {
				headBranch = f.runHeadBranch
			}
			path := citationWorkflowPath
			if f.runPath != "" {
				path = f.runPath
			}
			event := "workflow_dispatch"
			if f.runEvent != "" {
				event = f.runEvent
			}
			runStatus := "completed"
			if f.runStatus != "" {
				runStatus = f.runStatus
			}
			writeJSON(t, w, map[string]any{
				"id":          2,
				"head_sha":    sha,
				"head_branch": headBranch,
				"event":       event,
				"status":      runStatus,
				"conclusion":  conclusion,
				"html_url":    "https://example.test/runs/2",
				"path":        path,
				"created_at":  "2026-11-24T00:00:01Z",
			})
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/repos/paulpham157/sdf/statuses/"):
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("status body: %v", err)
				http.Error(w, `{"message":"bad json"}`, http.StatusBadRequest)
				return
			}
			if !strings.HasSuffix(r.URL.Path, "/"+f.citationSHA) {
				t.Errorf("status path %s", r.URL.Path)
			}
			f.statuses = append(f.statuses, body)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"state":"success"}`))
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/repos/paulpham157/sdf/commits/") && strings.HasSuffix(r.URL.Path, "/statuses"):
			writeJSON(t, w, f.statuses)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.RequestURI())
			http.NotFound(w, r)
		}
	}))
}

func (f *fakeGitHub) pullJSON() map[string]any {
	sha := f.citationSHA
	if sha == "" {
		sha = f.mainSHA
	}
	return map[string]any{
		"number":  7,
		"node_id": "PR_node_citation",
		"state":   "open",
		"body":    f.pulls[0],
		"head":    map[string]string{"ref": "citation/" + f.tagName, "sha": sha},
		"base":    map[string]string{"ref": "main", "sha": f.mainSHA},
	}
}

func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Errorf("encode: %v", err)
	}
}

func wrapBase64(b []byte) string {
	s := base64.StdEncoding.EncodeToString(b)
	var out strings.Builder
	for len(s) > 60 {
		out.WriteString(s[:60])
		out.WriteByte('\n')
		s = s[60:]
	}
	out.WriteString(s)
	return out.String()
}

func decodePut(t *testing.T, content any) []byte {
	t.Helper()
	s, ok := content.(string)
	if !ok {
		t.Fatalf("content type %T", content)
	}
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func assertOnlyKeys(t *testing.T, before, after []byte, keys ...string) {
	t.Helper()
	b := splitKeep(before)
	a := splitKeep(after)
	if len(b) != len(a) {
		t.Fatalf("line count %d vs %d", len(b), len(a))
	}
	allowed := map[string]bool{}
	for _, key := range keys {
		allowed[key] = true
	}
	for i := range b {
		if bytes.Equal(b[i], a[i]) {
			continue
		}
		body, _ := trimNL(a[i])
		key, _, ok := splitKey(body)
		if !ok || !allowed[key] {
			t.Fatalf("line %d changed outside %v:\n-%s\n+%s", i+1, keys, b[i], a[i])
		}
	}
}

func bumpPatch(version string) (string, error) {
	n, err := parseVer(version)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%d.%d.%d", n[0], n[1], n[2]+1), nil
}

func readRepoFile(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(moduleRoot(t), name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}
