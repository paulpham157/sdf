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

const verifyUsage = `usage: sdf verify SHA --task ID [--task-ref REF] [--task-hash HASH]
                  [--repo DIR] [--data-dir DIR | --no-data-dir] [--ledger PATH]
                  [--out PATH]
                  [--artifacts DIR] [--work-dir DIR] [--path PATH]
                  [--go-cache DIR] [--go-mod-cache DIR] [--expect-prefix SEQ:HASH]
                  [--allow-unisolated] [--allow-network NAME]...
                  [--allow-tamper-capable] [--keep]

Runs task ID's checks against commit SHA (a full id) in a fresh checkout
outside the repository. The task is read from --task-ref (the protected
branch as just fetched, for example origin/main) and the sdf data dir;
every copy found has to agree, and --task-hash pins the hash they must
have. PASS needs --task-ref, --task-hash, or a ledger pinned with
--expect-prefix SEQ:sha256:<entry hash>: a task found only in the data dir
or an unpinned ledger, which the agent can write, gives at best ERROR
(TASK_UNTRUSTED). A local branch as --task-ref draws a warning: pass a
freshly fetched remote ref or a commit id.

Each check runs as pid 1's child in new Linux user, pid, mount and network
namespaces, with an empty /tmp, /run, /dev/shm and home, only loopback,
and no capabilities; everything it started is killed when it exits. Where
that is not possible the verdict is ERROR (ISOLATION_UNAVAILABLE) unless
--allow-unisolated, which runs the checks anyway and records isolation
"none". --allow-network NAME keeps the host network for criterion NAME.

For a go test criterion, new or changed code that could forge the test
harness's reports (unsafe, debug/*, cgo, assembly, raw syscalls, /proc
paths) is ERROR (TAMPER_CAPABLE_CODE); --allow-tamper-capable, after a
person has read it, makes that a warning. The
working tree's .sdf/tasks file is never read. Checks run in the checkout
root with --path (default: this PATH, relative entries dropped) as their
only PATH, a new HOME, and new Go caches unless --go-cache/--go-mod-cache
name ones the agent cannot write.

--ledger PATH (or $SDF_LEDGER) also reads the task from that ledger and
appends the evidence and then the verdict to it in one transaction; with
neither set no ledger is used. A ledger that cannot be written is exit 3,
and nothing is appended.

Prints the verdict and evidence as JSON. Exit 0 is PASS, 1 FAIL, 3 ERROR,
2 a usage error.`

func cmdVerify(args []string, stdout, stderr io.Writer) int {
	const prefix = "sdf verify"
	opt := verify.Options{RepoDir: "."}
	var out, dataDir, ledgerFlag string
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
		case "--allow-unisolated":
			opt.AllowUnisolated = true
			continue
		case "--allow-tamper-capable":
			opt.AllowTamperCapable = true
			continue
		}
		if v, ok, err := flagValue("--allow-network", &i, args); ok {
			if err == nil && v == "" {
				err = fmt.Errorf("--allow-network requires a criterion name")
			}
			if err != nil {
				fmt.Fprintf(stderr, "%s: %v\n", prefix, err)
				return 2
			}
			opt.AllowNetwork = append(opt.AllowNetwork, v)
			continue
		}
		if v, ok, err := flagValue("--ledger", &i, args); ok {
			if err == nil && v == "" {
				err = fmt.Errorf("--ledger requires a value")
			}
			if err != nil {
				fmt.Fprintf(stderr, "%s: %v\n", prefix, err)
				return 2
			}
			ledgerFlag = v
			continue
		}
		matched := false
		for _, f := range []struct {
			name string
			dst  *string
		}{
			{"--task", &opt.TaskID}, {"--task-ref", &opt.TaskRef}, {"--repo", &opt.RepoDir},
			{"--data-dir", &dataDir}, {"--out", &out}, {"--artifacts", &opt.ArtifactsDir},
			{"--work-dir", &opt.WorkDir}, {"--path", &opt.Path}, {"--task-hash", &opt.TaskHash},
			{"--go-cache", &opt.GoCache}, {"--go-mod-cache", &opt.GoModCache},
			{"--expect-prefix", &opt.ExpectPrefix},
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
	opt.DataDir = st.DataDir
	// Prepared before any check runs, so an unwritable ledger fails fast.
	lg, err := openTaskLedger(context.Background(), taskLedgerPath(ledgerFlag), true)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", prefix, err)
		return 3
	}
	if lg != nil {
		defer lg.Close()
		opt.Ledger = lg
	}

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
	for _, w := range res.Warnings {
		fmt.Fprintf(stderr, "warning: %s\n", w)
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
