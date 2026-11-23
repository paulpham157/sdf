package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/paulpham157/sdf/internal/verify"
)

const verifyUsage = `usage: sdf verify SHA --task ID [--task-ref REF] [--repo DIR]
                  [--data-dir DIR | --no-data-dir] [--out PATH]
                  [--artifacts DIR] [--work-dir DIR] [--keep]

Runs task ID's checks against commit SHA (a full id) in a fresh checkout
outside the repository. The task is read from --task-ref (the protected
branch, for example origin/main) and the sdf data dir; every copy found has
to agree. The working tree's .sdf/tasks file is not trusted.

Prints the verdict and evidence as JSON. Exit 0 is PASS, 1 FAIL, 3 ERROR,
2 a usage error.`

func cmdVerify(args []string, stdout, stderr io.Writer) int {
	const prefix = "sdf verify"
	opt := verify.Options{RepoDir: "."}
	var out, dataDir string
	noDataDir := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "--help", "-h":
			fmt.Fprintln(stdout, verifyUsage)
			return 0
		case "--keep":
			opt.Keep = true
			continue
		case "--no-data-dir":
			noDataDir = true
			continue
		}
		matched := false
		for _, f := range []struct {
			name string
			dst  *string
		}{
			{"--task", &opt.TaskID}, {"--task-ref", &opt.TaskRef}, {"--repo", &opt.RepoDir},
			{"--data-dir", &dataDir}, {"--out", &out}, {"--artifacts", &opt.ArtifactsDir},
			{"--work-dir", &opt.WorkDir},
		} {
			v, ok, err := flagValue(f.name, &i, args)
			if !ok {
				continue
			}
			if err != nil {
				fmt.Fprintf(stderr, "%s: %v\n", prefix, err)
				return 2
			}
			*f.dst, matched = v, true
			break
		}
		if matched {
			continue
		}
		if strings.HasPrefix(arg, "-") || opt.SHA != "" {
			fmt.Fprintf(stderr, "unrecognized arguments: %s\n", arg)
			return 2
		}
		opt.SHA = arg
	}
	if opt.SHA == "" || opt.TaskID == "" {
		fmt.Fprintln(stderr, verifyUsage)
		return 2
	}
	st, err := taskStore(opt.RepoDir, dataDir, noDataDir)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", prefix, err)
		return 3
	}
	opt.DataDir, opt.Ledger = st.DataDir, st.Ledger

	res, err := verify.Run(context.Background(), opt)
	if errors.Is(err, verify.ErrUsage) {
		fmt.Fprintf(stderr, "%s: %v\n", prefix, err)
		return 2
	}
	if err != nil && res.Verdict.ID == "" {
		fmt.Fprintf(stderr, "%s: %v\n", prefix, err)
		return 3
	}
	doc, jerr := verdictJSON(res)
	if jerr != nil {
		fmt.Fprintf(stderr, "%s: %v\n", prefix, jerr)
		return 3
	}
	stdout.Write(doc)
	if out != "" {
		if werr := os.WriteFile(out, doc, 0o644); werr != nil {
			fmt.Fprintf(stderr, "%s: %v\n", prefix, werr)
			return 3
		}
	}
	fmt.Fprintf(stderr, "verdict %s %s\n", res.Status, res.Verdict.Hash)
	for _, r := range res.Reasons {
		fmt.Fprintf(stderr, "  %s: %s\n", r.Code, r.Detail)
	}
	if res.Checkout != "" {
		fmt.Fprintf(stderr, "checkout kept at %s\n", res.Checkout)
	}
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", prefix, err)
		return 3
	}
	switch res.Status {
	case verify.StatusPass:
		return 0
	case verify.StatusFail:
		return 1
	default:
		return 3
	}
}

// verdictJSON is the verdict and its evidence. Each record is its canonical
// JSON, indented; re-canonicalizing a record (RFC 8785) gives back the
// bytes its hash covers.
func verdictJSON(res verify.Result) ([]byte, error) {
	type rec struct {
		Kind   string          `json:"kind"`
		Hash   string          `json:"hash"`
		Record json.RawMessage `json:"record"`
	}
	doc := struct {
		Status   string `json:"status"`
		Verdict  rec    `json:"verdict"`
		Evidence []rec  `json:"evidence"`
	}{Status: res.Status}
	doc.Verdict = rec{string(res.Verdict.Kind), res.Verdict.Hash, res.Verdict.Canonical}
	doc.Evidence = []rec{}
	for _, e := range res.Evidence {
		doc.Evidence = append(doc.Evidence, rec{string(e.Kind), e.Hash, e.Canonical})
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
