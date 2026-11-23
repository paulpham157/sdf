package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/paulpham157/sdf/internal/task"
)

const taskUsage = "usage: sdf task <declare|show> [flags]"

const taskDeclareUsage = `usage: sdf task declare [--file PATH|-] [--id ID] [--title TEXT] [--objective TEXT]
                        [--base REV] [--check NAME=COMMAND]... [--designated NAME=GLOB]...
                        [--timeout SECONDS] [--no-secret-scan]
                        [--repo DIR] [--write] [--data-dir DIR | --no-data-dir]

--file reads a JSON task input. Flags override its id, title, objective,
and base, and --check adds criteria. COMMAND is split on spaces with no
quoting and runs without a shell. --designated adds a glob (** for any
depth) to the criterion NAME. --write puts the task at .sdf/tasks/ID.json
in --repo, for committing. The data dir copy is $XDG_DATA_HOME/sdf/tasks.`

const taskShowUsage = "usage: sdf task show ID [--repo DIR] [--data-dir DIR | --no-data-dir] [--json]"

// taskLedger is the ledger sdf task writes to. It is nil until the ledger
// package lands; tests set it.
var taskLedger task.Ledger

func cmdTask(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, taskUsage)
		return 2
	}
	switch args[0] {
	case "declare":
		return cmdTaskDeclare(args[1:], stdout, stderr)
	case "show":
		return cmdTaskShow(args[1:], stdout, stderr)
	case "--help", "-h":
		fmt.Fprintln(stdout, taskUsage)
		return 0
	default:
		fmt.Fprintf(stderr, "unrecognized arguments: %s\n", args[0])
		return 2
	}
}

// flagValue handles "--name VALUE" and "--name=VALUE". It reports whether
// arg was this flag, and fails when the value is missing.
func flagValue(name string, i *int, args []string) (string, bool, error) {
	arg := args[*i]
	if arg == name {
		v, ok := take(i, args)
		if !ok {
			return "", true, fmt.Errorf("%s requires a value", name)
		}
		return v, true, nil
	}
	if strings.HasPrefix(arg, name+"=") {
		return strings.TrimPrefix(arg, name+"="), true, nil
	}
	return "", false, nil
}

func cmdTaskDeclare(args []string, stdout, stderr io.Writer) int {
	const prefix = "sdf task declare"
	var (
		file, id, title, objective, base string
		idSet, titleSet, objSet, baseSet bool
		checks, designated               []string
		timeout                          int64
		noSecretScan, write, noDataDir   bool
		repo                             = "."
		dataDir                          string
	)
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "--help", "-h":
			fmt.Fprintln(stdout, taskDeclareUsage)
			return 0
		case "--no-secret-scan":
			noSecretScan = true
			continue
		case "--write":
			write = true
			continue
		case "--no-data-dir":
			noDataDir = true
			continue
		}
		matched := false
		for _, f := range []struct {
			name string
			set  func(string) error
		}{
			{"--file", func(v string) error { file = v; return nil }},
			{"--id", func(v string) error { id, idSet = v, true; return nil }},
			{"--title", func(v string) error { title, titleSet = v, true; return nil }},
			{"--objective", func(v string) error { objective, objSet = v, true; return nil }},
			{"--base", func(v string) error { base, baseSet = v, true; return nil }},
			{"--check", func(v string) error { checks = append(checks, v); return nil }},
			{"--designated", func(v string) error { designated = append(designated, v); return nil }},
			{"--repo", func(v string) error { repo = v; return nil }},
			{"--data-dir", func(v string) error { dataDir = v; return nil }},
			{"--timeout", func(v string) error {
				n, err := strconv.ParseInt(v, 10, 64)
				if err != nil || n < 1 {
					return fmt.Errorf("--timeout must be a positive number of seconds")
				}
				timeout = n
				return nil
			}},
		} {
			v, ok, err := flagValue(f.name, &i, args)
			if !ok {
				continue
			}
			if err == nil {
				err = f.set(v)
			}
			if err != nil {
				fmt.Fprintf(stderr, "%s: %v\n", prefix, err)
				return 2
			}
			matched = true
			break
		}
		if !matched {
			fmt.Fprintf(stderr, "unrecognized arguments: %s\n", arg)
			return 2
		}
	}

	var in task.Input
	if file != "" {
		var data []byte
		var err error
		if file == "-" {
			data, err = io.ReadAll(os.Stdin)
		} else {
			data, err = os.ReadFile(file)
		}
		if err == nil {
			in, err = task.ParseInput(data)
		}
		if err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", prefix, err)
			return 1
		}
	}
	if idSet {
		in.ID = id
	}
	if titleSet {
		in.Title = title
	}
	if objSet {
		in.Objective = objective
	}
	if baseSet {
		in.Base = base
	}
	if noSecretScan {
		off := false
		in.SecretScan = &off
	}
	for _, c := range checks {
		name, cmd, ok := strings.Cut(c, "=")
		argv := strings.Fields(cmd)
		if !ok || name == "" || len(argv) == 0 {
			fmt.Fprintf(stderr, "%s: --check requires NAME=COMMAND\n", prefix)
			return 2
		}
		in.Criteria = append(in.Criteria, task.CriterionInput{Name: name, Argv: argv, TimeoutS: timeout})
	}
	for _, d := range designated {
		name, glob, ok := strings.Cut(d, "=")
		if !ok || name == "" || glob == "" {
			fmt.Fprintf(stderr, "%s: --designated requires NAME=GLOB\n", prefix)
			return 2
		}
		found := false
		for i := range in.Criteria {
			if in.Criteria[i].Name == name {
				in.Criteria[i].Designated = append(in.Criteria[i].Designated, glob)
				found = true
			}
		}
		if !found {
			fmt.Fprintf(stderr, "%s: --designated names no criterion %q\n", prefix, name)
			return 2
		}
	}
	if in.ID == "" || in.Title == "" || len(in.Criteria) == 0 {
		fmt.Fprintln(stderr, taskDeclareUsage)
		return 2
	}

	st, err := taskStore(repo, dataDir, noDataDir)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", prefix, err)
		return 1
	}
	st.WriteRepo = write
	if !write && st.DataDir == "" && st.Ledger == nil {
		fmt.Fprintf(stderr, "%s: nowhere to store the task: pass --write, or drop --no-data-dir\n", prefix)
		return 2
	}
	ctx := context.Background()
	spec, err := task.Build(ctx, repo, in)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", prefix, err)
		return 1
	}
	d, err := task.Declare(ctx, spec, st)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", prefix, err)
		return 1
	}
	fmt.Fprintf(stdout, "task %s %s\n", d.Spec.ID, d.Hash)
	fmt.Fprintf(stdout, "base %s\n", d.Spec.BaseSHA)
	fmt.Fprintf(stdout, "designated files %d\n", len(d.Spec.DesignatedFiles))
	for _, c := range d.Copies {
		state := "unchanged"
		if c.New {
			state = "written"
		}
		fmt.Fprintf(stdout, "%s %s\n", state, c.Where)
	}
	return 0
}

func taskStore(repo, dataDir string, noDataDir bool) (task.Store, error) {
	st := task.Store{RepoDir: repo, Ledger: taskLedger}
	if noDataDir {
		return st, nil
	}
	if dataDir == "" {
		d, err := task.DefaultDataDir()
		if err != nil {
			return st, err
		}
		dataDir = d
	}
	st.DataDir = dataDir
	return st, nil
}

func cmdTaskShow(args []string, stdout, stderr io.Writer) int {
	const prefix = "sdf task show"
	var (
		id, dataDir       string
		repo              = "."
		asJSON, noDataDir bool
	)
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "--help", "-h":
			fmt.Fprintln(stdout, taskShowUsage)
			return 0
		case "--json":
			asJSON = true
			continue
		case "--no-data-dir":
			noDataDir = true
			continue
		}
		if v, ok, err := flagValue("--repo", &i, args); ok {
			if err != nil {
				fmt.Fprintf(stderr, "%s: %v\n", prefix, err)
				return 2
			}
			repo = v
			continue
		}
		if v, ok, err := flagValue("--data-dir", &i, args); ok {
			if err != nil {
				fmt.Fprintf(stderr, "%s: %v\n", prefix, err)
				return 2
			}
			dataDir = v
			continue
		}
		if strings.HasPrefix(arg, "-") || id != "" {
			fmt.Fprintf(stderr, "unrecognized arguments: %s\n", arg)
			return 2
		}
		id = arg
	}
	if id == "" {
		fmt.Fprintln(stderr, taskShowUsage)
		return 2
	}
	st, err := taskStore(repo, dataDir, noDataDir)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", prefix, err)
		return 1
	}
	found, err := task.Load(context.Background(), id, st)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", prefix, err)
		return 1
	}
	spec := found[0].Spec
	if asJSON {
		b, err := spec.Canonical()
		if err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", prefix, err)
			return 1
		}
		var pretty bytes.Buffer
		if err := json.Indent(&pretty, b, "", "  "); err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", prefix, err)
			return 1
		}
		pretty.WriteByte('\n')
		stdout.Write(pretty.Bytes())
		return 0
	}
	fmt.Fprintf(stdout, "task %s %s\n", spec.ID, found[0].Hash)
	fmt.Fprintf(stdout, "title %s\n", spec.Title)
	if spec.Objective != "" {
		fmt.Fprintf(stdout, "objective %s\n", spec.Objective)
	}
	fmt.Fprintf(stdout, "base %s tree %s\n", spec.BaseSHA, spec.BaseTree)
	fmt.Fprintf(stdout, "secret scan %t\n", spec.SecretScan)
	for _, c := range spec.Criteria {
		fmt.Fprintf(stdout, "criterion %s: %s (timeout %ds, expect exit %d)\n", c.Name, strings.Join(c.Argv, " "), c.TimeoutS, c.ExpectExit)
		for _, g := range c.Designated {
			fmt.Fprintf(stdout, "  designated %s\n", g)
		}
	}
	for _, f := range spec.DesignatedFiles {
		fmt.Fprintf(stdout, "file %s %s sha256:%s\n", f.Path, f.GitBlob, f.SHA256)
	}
	for _, f := range found {
		fmt.Fprintf(stdout, "copy %s\n", f.Where)
	}
	return 0
}
