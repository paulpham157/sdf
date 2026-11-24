package cli

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/paulpham157/sdf/internal/decision"
	"github.com/paulpham157/sdf/internal/store"
)

const decisionPrePushUsage = "usage: sdf decision pre-push [--db PATH] [--remote NAME] [--mode warn|block|off] < pre-push lines"

// The git pre-push hook from sdf init --git-hooks runs this command with
// the hook's stdin. Exit 0 lets the push go on: no finding, a warning, or
// any reason the check could not run (no store, unreadable store, not a
// git checkout, too slow). Exit 1 only in block mode with at least one
// recorded commit that implements no decision. Exit 2 is usage. A push
// never fails because SDF is missing or broken.
const exitPrePushBlocked = 1

// prePushBudget bounds the whole check. Past it the push goes on.
var prePushBudget = 5 * time.Second

// prePushMaxCommits bounds how many pushed commits are read. A first push
// of a long history checks the newest ones.
const prePushMaxCommits = 1000

// prePushStdin is the hook's stdin. Tests replace it.
var prePushStdin io.Reader = os.Stdin

var (
	prePushSHA    = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)
	prePushRemote = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
)

func cmdDecisionPrePush(args []string, stdout, stderr io.Writer) int {
	dbPath := ""
	remote := ""
	mode := ""
	for i := 0; i < len(args); i++ {
		arg := args[i]
		flag, value, hasValue := arg, "", false
		if j := strings.IndexByte(arg, '='); j > 0 && strings.HasPrefix(arg, "--") {
			flag, value, hasValue = arg[:j], arg[j+1:], true
		}
		switch flag {
		case "--help", "-h":
			fmt.Fprintln(stdout, decisionPrePushUsage)
			fmt.Fprintln(stdout, "")
			fmt.Fprintln(stdout, decisionPrePushHelp)
			return 0
		case "--db", "--remote", "--mode":
			if !hasValue {
				v, ok := take(&i, args)
				if !ok {
					fmt.Fprintf(stderr, "sdf decision pre-push: %s requires a value\n", flag)
					return 2
				}
				value = v
			}
			switch flag {
			case "--db":
				dbPath = value
			case "--remote":
				remote = value
			case "--mode":
				if !validPrePushMode(value) {
					fmt.Fprintln(stderr, "sdf decision pre-push: --mode must be warn, block, or off")
					return 2
				}
				mode = value
			}
		default:
			unrecognizedArgs(stderr, arg)
			return 2
		}
	}
	if mode == "" {
		mode = prePushMode(stderr)
	}
	if mode == "off" {
		return 0
	}
	input, err := io.ReadAll(io.LimitReader(prePushStdin, 1<<20))
	if err != nil {
		fmt.Fprintf(stderr, "sdf: pre-push decision check skipped: reading the hook input: %s\n", displayErr(err))
		return 0
	}
	if strings.TrimSpace(dbPath) == "" {
		dbPath = strings.TrimSpace(os.Getenv("SDF_PRE_PUSH_DB"))
	}
	type result struct {
		code int
		text string
	}
	done := make(chan result, 1)
	ctx, cancel := context.WithTimeout(context.Background(), prePushBudget)
	defer cancel()
	go func() {
		var buf bytes.Buffer
		code := runPrePush(ctx, string(input), dbPath, remote, mode == "block", &buf)
		done <- result{code, buf.String()}
	}()
	select {
	case r := <-done:
		fmt.Fprint(stderr, r.text)
		return r.code
	case <-ctx.Done():
		fmt.Fprintf(stderr, "sdf: pre-push decision check skipped: it took longer than %s. The push continues.\n", prePushBudget)
		return 0
	}
}

func validPrePushMode(v string) bool {
	return v == "warn" || v == "block" || v == "off"
}

// prePushMode is SDF_PRE_PUSH, else git config sdf.prePush, else warn. An
// unknown value is warn, with a note.
func prePushMode(stderr io.Writer) string {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("SDF_PRE_PUSH")))
	from := "SDF_PRE_PUSH"
	if v == "" {
		cmd := exec.Command("git", "config", "--get", "sdf.prePush")
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		if out, err := cmd.Output(); err == nil {
			v = strings.ToLower(strings.TrimSpace(string(out)))
			from = "git config sdf.prePush"
		}
	}
	switch v {
	case "":
		return "warn"
	case "0", "false", "no", "skip", "disable", "disabled":
		return "off"
	}
	if validPrePushMode(v) {
		return v
	}
	fmt.Fprintf(stderr, "sdf: %s is %q; using warn (valid: warn, block, off)\n", from, v)
	return "warn"
}

// runPrePush does the work and returns the exit code. Every failure to
// check is reported to w and returns 0.
func runPrePush(ctx context.Context, input, dbPath, remote string, block bool, w io.Writer) int {
	skip := func(format string, a ...any) int {
		fmt.Fprintf(w, "sdf: pre-push decision check skipped: "+format+" The push continues.\n", a...)
		return 0
	}
	commits, truncated, err := pushedCommits(ctx, input, remote)
	if err != nil {
		return skip("%s.", displayErr(err))
	}
	if len(commits) == 0 {
		return 0
	}
	if dbPath == "" {
		return skip("no SDF store is configured for this hook (re-run sdf init --git-hooks --db PATH, or set SDF_PRE_PUSH_DB).")
	}
	if _, err := os.Stat(dbPath); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return skip("no SDF store at %s.", displayPath(store.AbsPath(dbPath)))
		}
		return skip("%s.", displayErr(err))
	}
	db, err := store.OpenReadOnly(dbPath)
	if err != nil {
		return skip("%s.", displayErr(err))
	}
	report, err := decision.CheckPush(db, commits)
	_ = db.Close()
	if err != nil {
		return skip("%s.", displayErr(err))
	}
	blocked := block && len(report.Missing) > 0
	if err := decision.WritePushText(w, report, " --db "+shellWord(displayPath(store.AbsPath(dbPath))), blocked); err != nil {
		return 0
	}
	if truncated {
		fmt.Fprintf(w, "sdf: pre-push: only the newest %d pushed commits were checked.\n", prePushMaxCommits)
	}
	if blocked {
		return exitPrePushBlocked
	}
	return 0
}

// pushedCommits reads git's pre-push lines (local_ref local_sha remote_ref
// remote_sha) and lists the non-merge commits the push sends, newest first,
// with their SDF-Decision trailer values. A delete (local sha all zeros)
// sends no commits. A new branch (remote sha all zeros), or a remote sha
// this repository does not have, is compared with the remote-tracking
// branches of the remote (all remotes when the remote is a URL). Only
// %(trailers:key=SDF-Decision) is read from the message; the diff is not.
func pushedCommits(ctx context.Context, input, remote string) ([]decision.PushCommit, bool, error) {
	var out []decision.PushCommit
	seen := map[string]bool{}
	truncated := false
	sc := bufio.NewScanner(strings.NewReader(input))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 0 {
			continue
		}
		if len(fields) != 4 {
			return nil, false, fmt.Errorf("unexpected pre-push input %q", sc.Text())
		}
		local, remoteSHA := strings.ToLower(fields[1]), strings.ToLower(fields[3])
		if !prePushSHA.MatchString(local) || !prePushSHA.MatchString(remoteSHA) {
			return nil, false, fmt.Errorf("unexpected pre-push input %q", sc.Text())
		}
		if allZero(local) {
			continue
		}
		args := []string{"log", "--no-merges", fmt.Sprintf("--max-count=%d", prePushMaxCommits+1),
			"--format=%H%x1f%(trailers:key=SDF-Decision,valueonly=true,unfold=true,separator=%x1f)%x1e", local}
		if !allZero(remoteSHA) && gitHasCommit(ctx, remoteSHA) {
			args = append(args, "^"+remoteSHA)
		} else if prePushRemote.MatchString(remote) {
			args = append(args, "--not", "--remotes="+remote)
		} else {
			args = append(args, "--not", "--remotes")
		}
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		raw, err := cmd.Output()
		if err != nil {
			msg := strings.TrimSpace(stderr.String())
			if msg == "" {
				msg = err.Error()
			}
			return nil, false, fmt.Errorf("git log: %s", firstLine(msg))
		}
		for _, rec := range strings.Split(string(raw), "\x1e") {
			rec = strings.TrimLeft(rec, "\n")
			if rec == "" {
				continue
			}
			parts := strings.Split(rec, "\x1f")
			sha := strings.ToLower(strings.TrimSpace(parts[0]))
			if !prePushSHA.MatchString(sha) || seen[sha] {
				continue
			}
			if len(out) == prePushMaxCommits {
				truncated = true
				break
			}
			seen[sha] = true
			c := decision.PushCommit{SHA: sha}
			for _, v := range parts[1:] {
				if v = strings.TrimSpace(v); v != "" {
					c.Trailers = append(c.Trailers, v)
				}
			}
			out = append(out, c)
		}
	}
	return out, truncated, sc.Err()
}

func gitHasCommit(ctx context.Context, sha string) bool {
	cmd := exec.CommandContext(ctx, "git", "cat-file", "-e", sha+"^{commit}")
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	return cmd.Run() == nil
}

func allZero(sha string) bool {
	return strings.Trim(sha, "0") == ""
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	if len(line) > 200 {
		line = line[:200]
	}
	return line
}

const decisionPrePushHelp = `Checks the commits a git push sends. The pre-push hook from sdf init
--git-hooks runs it with the hook's stdin; you rarely run it by hand.

A pushed commit SDF recorded (through the git hooks) or that carries an
SDF-Decision: ID trailer should implement a decision: an implements edge
from commit:SHA, or the trailer, naming a recorded decision that is not
retracted. Commits SDF never saw are not judged. Only the SDF-Decision
trailer is read from commit messages; the diff is never read.

  --db PATH        the sdf serve store (default SDF_PRE_PUSH_DB; none: skip)
  --remote NAME    the remote being pushed to (the hook's first argument)
  --mode MODE      warn (default), block, or off; default SDF_PRE_PUSH,
                   then git config sdf.prePush

Exit 0 lets the push go on, including whenever the check cannot run. Exit 1
is block mode with a commit that implements no decision. Bypass with git
push --no-verify or SDF_PRE_PUSH=off.`
