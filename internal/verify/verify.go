// Package verify runs a declared task's checks against a submitted revision,
// in a fresh checkout the agent's workspace does not share, and records
// what happened as canonical evidence and a verdict.
//
// The order is fixed. The task is read from trusted copies only. The
// revision is fetched into a new repository (so git re-hashes every object
// it receives) and checked out detached. Revision identity, designated file
// integrity, and the secret scan run first. If any of them fails, no
// criterion runs: the submitted code is not executed. Every criterion then
// runs in the checkout, and the checkout must be unchanged afterwards.
//
// A verdict is PASS, FAIL, or ERROR. Any FAIL reason makes it FAIL. Any
// ERROR reason (the verifier could not tell) makes it ERROR. Otherwise it is
// PASS. Only PASS is acceptance.
package verify

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/paulpham157/sdf/internal/ledger"
	"github.com/paulpham157/sdf/internal/task"
)

// Statuses.
const (
	StatusPass  = "PASS"
	StatusFail  = "FAIL"
	StatusError = "ERROR"
)

// Reason codes. The ones in failCodes make a verdict FAIL; the rest make it
// ERROR.
const (
	CodeTaskSnapshotMissing    = "TASK_SNAPSHOT_MISSING"
	CodeTaskHashMismatch       = "TASK_HASH_MISMATCH"
	CodeTaskFileModified       = "TASK_FILE_MODIFIED"
	CodeRevisionUnresolvable   = "REVISION_UNRESOLVABLE"
	CodeRevisionMismatch       = "REVISION_MISMATCH"
	CodeBaseMismatch           = "BASE_MISMATCH"
	CodeBaseNotAncestor        = "BASE_NOT_ANCESTOR"
	CodeDesignatedFileModified = "DESIGNATED_FILE_MODIFIED"
	CodeDesignatedFileAdded    = "DESIGNATED_FILE_ADDED"
	CodeDesignatedTestMissing  = "DESIGNATED_TEST_NOT_PASSED"
	CodeSymlinkOutside         = "SYMLINK_OUTSIDE_CHECKOUT"
	CodeTestReportInvalid      = "TEST_REPORT_INVALID"
	CodeSecretInDiff           = "SECRET_IN_DIFF"
	CodeCheckoutDirty          = "CHECKOUT_DIRTY"
	CodeCheckoutModified       = "CHECKOUT_MODIFIED"
	CodeCheckFailed            = "CHECK_FAILED"
	CodeCheckTimeout           = "CHECK_TIMEOUT"
	CodeCheckError             = "CHECK_ERROR"
	CodeTaskUntrusted          = "TASK_UNTRUSTED"
	CodeIsolationUnavailable   = "ISOLATION_UNAVAILABLE"
	CodeTamperCapable          = "TAMPER_CAPABLE_CODE"
	CodeCommandChanged         = "COMMAND_CHANGED"
	CodeCheckAddedCode         = "CHECK_ADDED_CODE"
	CodeInfraError             = "INFRA_ERROR"
)

var failCodes = map[string]bool{
	CodeTaskSnapshotMissing: true, CodeTaskHashMismatch: true, CodeTaskFileModified: true,
	CodeRevisionUnresolvable: true, CodeRevisionMismatch: true, CodeBaseMismatch: true,
	CodeBaseNotAncestor: true, CodeDesignatedFileModified: true, CodeDesignatedFileAdded: true,
	CodeSecretInDiff: true, CodeCheckoutDirty: true, CodeCheckoutModified: true,
	CodeCheckFailed: true, CodeCheckTimeout: true, CodeDesignatedTestMissing: true,
	CodeSymlinkOutside: true, CodeTestReportInvalid: true, CodeCommandChanged: true,
	CodeCheckAddedCode: true,
}

// Record schemas, also the ledger kinds.
const (
	EvidenceSchema = string(task.KindEvidence)
	VerdictSchema  = string(task.KindVerdict)
	// Verifier is the identity written on every record.
	Verifier = "sdf-verify"
)

// Options configures one verification.
type Options struct {
	RepoDir string // repository holding the submitted revision
	SHA     string // full commit id to verify
	TaskID  string

	// Task copies. At least one has to exist; all that exist have to
	// agree. The working tree's .sdf/tasks file is never one of them.
	//
	// PASS needs a trusted source: TaskRef, a ledger entry, or TaskHash.
	// The data dir copy is written by whoever runs sdf on the machine, the
	// agent included, so a task found only there gives at best ERROR
	// (TASK_UNTRUSTED).
	TaskRef  string      // a ref, normally the protected branch, holding .sdf/tasks/<id>.json
	DataDir  string      // the sdf data dir copy (untrusted on its own)
	Ledger   task.Ledger // also receives the evidence and verdict
	TaskHash string      // "sha256:..." pinned by the caller; every copy has to have it

	// ExpectPrefix pins the ledger: "SEQ:sha256:<entry hash>". A task read
	// from the ledger is trusted only when the ledger's chain verifies, its
	// entry SEQ has that entry hash, and the task entry is at or before SEQ.
	// Without it, a ledger is no more trusted than the data dir: anyone can
	// make one and point --ledger or $SDF_LEDGER at it.
	ExpectPrefix string

	// AllowUnisolated lets criteria run without Linux namespaces. The
	// verdict then records isolation "none"; without this, no isolation is
	// ERROR (ISOLATION_UNAVAILABLE) and no criterion runs.
	AllowUnisolated bool

	// AllowTamperCapable turns the tripwire's TAMPER_CAPABLE_CODE (new code
	// that could forge go test reports: unsafe, debug/*, cgo, assembly,
	// raw syscalls, /proc paths) from ERROR into a warning.
	AllowTamperCapable bool

	// AllowNetwork names criteria that keep the host network. Every other
	// criterion runs in its own network namespace with only loopback.
	AllowNetwork []string
	// WritableCheckout names go test/vet criteria whose checkout is
	// writable. Every other go test/vet criterion runs on a read-only
	// checkout (isolated), so a test binary cannot plant a file that a
	// package built later in the same run compiles.
	WritableCheckout []string

	// GoCache and GoModCache, when set, are the GOCACHE and GOMODCACHE
	// checks use. They have to be directories the agent cannot write.
	// Empty means new, empty ones for this verification.
	GoCache, GoModCache string

	// Path is the PATH the criteria run with, and the only one argv[0] is
	// looked up in. Empty means the verifier's own PATH. Relative entries
	// are dropped, so "." cannot resolve a command from the checkout.
	Path string

	WorkDir      string // parent of the temporary checkout; default os.TempDir()
	ArtifactsDir string // when set, check output is kept here as <sha256> files
	Keep         bool   // keep the temporary checkout
	Now          func() time.Time
}

// Reason is one cause of a non-PASS verdict.
type Reason struct {
	Code       string
	Detail     string
	EvidenceID string
}

// Record is one canonical evidence or verdict record.
type Record struct {
	Kind      task.Kind
	ID        string
	Value     map[string]any
	Canonical []byte
	Hash      string
}

// Result is a finished verification.
type Result struct {
	Status   string
	Reasons  []Reason
	Evidence []Record
	Verdict  Record
	Checkout string   // set when Options.Keep
	Warnings []string // for the person running verify; not in the records
}

var (
	fullSHA    = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)
	taskHashRe = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

// ErrUsage is returned for options that cannot be verified at all.
var ErrUsage = errors.New("usage")

type run struct {
	opt      Options
	ctx      context.Context
	id       string
	started  time.Time
	reasons  []Reason
	evidence []Record
	spec     task.Spec
	taskHash string
	sources  []any
	checkout string
	tree     string
	// treeAtSHA is the submitted tree, read from the fetched objects before
	// any check runs; checkDisk compares the checkout with it.
	treeAtSHA map[string]treeEntry
	critN     int
	gitHidden bool
	pristine  string
	ranCheck  bool
	pins      map[string]commandPin
	isolation string
	// reasonLater holds reasons found before their evidence record exists.
	reasonLater []Reason
	warnings    []string
}

// Run verifies opt.SHA against task opt.TaskID. A returned error means no
// verdict was produced (bad options, or the ledger refused a record).
// Everything else, including a missing task or an unreachable revision, is
// a verdict.
func Run(ctx context.Context, opt Options) (Result, error) {
	if !fullSHA.MatchString(opt.SHA) {
		return Result{}, fmt.Errorf("%w: %q is not a full commit id", ErrUsage, opt.SHA)
	}
	if !task.ValidID(opt.TaskID) {
		return Result{}, fmt.Errorf("%w: task id %q", ErrUsage, opt.TaskID)
	}
	if opt.ExpectPrefix != "" && !prefixRe.MatchString(opt.ExpectPrefix) {
		return Result{}, fmt.Errorf("%w: --expect-prefix %q is not SEQ:sha256:<64 hex>", ErrUsage, opt.ExpectPrefix)
	}
	if opt.TaskHash != "" && !taskHashRe.MatchString(opt.TaskHash) {
		return Result{}, fmt.Errorf("%w: task hash %q is not sha256:<64 hex>", ErrUsage, opt.TaskHash)
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	repo, err := filepath.Abs(opt.RepoDir)
	if err != nil {
		return Result{}, err
	}
	if root, err := task.RepoRoot(ctx, repo); err == nil {
		repo = root
	}
	opt.RepoDir = repo
	if opt.Path == "" {
		opt.Path = os.Getenv("PATH")
	}
	opt.Path = absolutePath(opt.Path)
	var idb [8]byte
	if _, err := rand.Read(idb[:]); err != nil {
		return Result{}, err
	}
	r := &run{opt: opt, ctx: ctx, started: opt.Now().UTC()}
	r.id = "verify-" + r.started.Format("20060102T150405Z") + "-" + hex.EncodeToString(idb[:])

	r.steps()
	if r.checkout != "" && !opt.Keep {
		_ = removeTree(filepath.Dir(r.checkout))
	}
	res, err := r.finish()
	if err != nil {
		return Result{}, err
	}
	if opt.Keep {
		res.Checkout = r.checkout
	}
	return res, nil
}

func (r *run) steps() {
	if !r.loadTask() {
		return
	}
	if !r.prepareCheckout() {
		return
	}
	r.checkRevision()
	r.checkIntegrity()
	for _, w := range task.Warnings(r.spec) {
		if strings.HasPrefix(w, "designated test file") {
			r.warnings = append(r.warnings, w)
		}
	}
	if r.spec.SecretScan {
		r.scanSecrets()
	}
	if r.failed() {
		return
	}
	if !r.prepareChecks() {
		return
	}
	for _, c := range r.spec.Criteria {
		r.runCriterion(c)
	}
}

// prepareChecks moves .git out of the checkout, decides isolation and pins
// every criterion's command.
func (r *run) prepareChecks() bool {
	if ok, why := isolationAvailable(); ok {
		r.isolation = IsolationNamespace
	} else {
		r.isolation = IsolationNone
		if !r.opt.AllowUnisolated {
			id := r.addEvidence("isolation", "", map[string]any{"status": "error", "isolation": IsolationNone, "error": why})
			r.reason(CodeIsolationUnavailable, id, "criteria were not run: %s (--allow-unisolated runs them anyway, and the verdict says isolation none)", why)
			return false
		}
	}
	err := r.hideGitDir()
	if err == nil {
		err = r.savePristine()
	}
	if err != nil {
		id := r.addEvidence("checkout", "", map[string]any{"status": "error", "error": err.Error()})
		r.reason(CodeInfraError, id, "preparing checks: %v", err)
		return false
	}
	r.pinCommands()
	return true
}

// commandPin is a criterion's argv[0] as resolved before any check ran.
type commandPin struct {
	path, sha256 string
}

// pinCommands resolves every criterion's argv[0] (in --path, or in the
// checkout) and hashes the file, before any check runs. Each criterion
// re-resolves and re-hashes its command before it starts: a check that
// drops a new `sh` into a PATH directory, or rewrites the `go` binary,
// changes what a later criterion would run (atk_pathjack), and that is
// COMMAND_CHANGED. Isolated checks cannot write PATH directories at all;
// this also covers unisolated runs and changes made from outside.
func (r *run) pinCommands() {
	r.pins = map[string]commandPin{}
	var list []any
	for _, c := range r.spec.Criteria {
		p, sum, err := r.resolveCommand(c.Argv[0])
		e := map[string]any{"criterion": c.Name, "argv0": c.Argv[0]}
		if err != nil {
			e["error"] = err.Error()
		} else {
			r.pins[c.Name] = commandPin{p, sum}
			e["path"], e["sha256"] = p, sum
		}
		list = append(list, e)
	}
	r.addEvidence("command_pins", "", map[string]any{"status": "pass", "commands": list})
}

// resolveCommand is argv0's path (lookPath) and the sha256 of the file it
// names, symlinks followed.
func (r *run) resolveCommand(argv0 string) (string, string, error) {
	p, err := lookPath(argv0, r.checkout, r.opt.Path)
	if err != nil {
		return "", "", err
	}
	f, err := os.Open(p)
	if err != nil {
		return "", "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", "", err
	}
	return p, "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// criterionDirs makes the new directories criterion c's check gets.
func (r *run) criterionDirs(c task.Criterion) (checkDirs, error) {
	hostNet := contains(r.opt.AllowNetwork, c.Name)
	r.critN++
	root := filepath.Join(filepath.Dir(r.checkout), fmt.Sprintf("c%02d", r.critN))
	d := checkDirs{
		root:       root,
		home:       filepath.Join(root, "home"),
		tmp:        filepath.Join(root, "tmp"),
		gopath:     filepath.Join(root, "gopath"),
		gocache:    filepath.Join(root, "gocache"),
		gomodcache: filepath.Join(root, "gomodcache"),
		gotmp:      filepath.Join(root, "gotmp"),
	}
	dirs := []string{d.home, d.tmp, d.gopath, d.gocache, d.gomodcache, d.gotmp}
	for _, tc := range []struct {
		trusted string
		dir     *string
		name    string
	}{{r.opt.GoCache, &d.gocache, "gocache"}, {r.opt.GoModCache, &d.gomodcache, "gomodcache"}} {
		if tc.trusted == "" {
			continue
		}
		if r.isolation != IsolationNamespace {
			// Unisolated: the check uses the trusted cache itself and can
			// write it (the verdict says isolation none).
			*tc.dir = tc.trusted
			continue
		}
		if tc.name == "gomodcache" && !hostNet {
			// Nothing to download without the network: the trusted module
			// cache is bound read-only, so not even a test binary of this
			// run can change a module another package compiles later.
			d.overlays = append(d.overlays, overlayMount{target: *tc.dir, lower: tc.trusted})
			continue
		}
		o := overlayMount{target: *tc.dir, lower: tc.trusted, upper: filepath.Join(root, tc.name+".upper"), work: filepath.Join(root, tc.name+".work")}
		d.overlays = append(d.overlays, o)
		dirs = append(dirs, o.upper, o.work)
	}
	for _, p := range dirs {
		if p == r.opt.GoCache || p == r.opt.GoModCache {
			continue
		}
		if err := os.MkdirAll(p, 0o700); err != nil {
			return d, err
		}
	}
	return d, nil
}

func (r *run) failed() bool {
	for _, x := range r.reasons {
		if failCodes[x.Code] {
			return true
		}
	}
	return false
}

func (r *run) reason(code, evidenceID, format string, args ...any) {
	r.reasons = append(r.reasons, Reason{Code: code, Detail: fmt.Sprintf(format, args...), EvidenceID: evidenceID})
}

func (r *run) now() string { return r.opt.Now().UTC().Format(task.TimeLayout) }

// evidence canonicalizes v as one evidence record and returns its id.
func (r *run) addEvidence(kind, name string, v map[string]any) string {
	// <verification id>.<NN>.<kind>: at most 72 bytes, inside the ledger's
	// id rule. The criterion name is in the record, not the id, so a long
	// name cannot push the id past 128 bytes.
	id := fmt.Sprintf("%s.%02d.%s", r.id, len(r.evidence)+1, kind)
	v["schema"] = EvidenceSchema
	v["id"] = id
	v["verification_id"] = r.id
	v["kind"] = kind
	v["name"] = name
	v["task_id"] = r.opt.TaskID
	v["task_hash"] = r.taskHash
	v["submitted_sha"] = r.opt.SHA
	v["verifier"] = Verifier
	rec, err := makeRecord(task.KindEvidence, id, v)
	if err != nil {
		// Only a value outside the canonical subset gets here: a bug.
		panic(err)
	}
	r.evidence = append(r.evidence, rec)
	return id
}

func makeRecord(kind task.Kind, id string, v map[string]any) (Record, error) {
	b, err := task.Canonical(v)
	if err != nil {
		return Record{}, err
	}
	return Record{Kind: kind, ID: id, Value: v, Canonical: b, Hash: task.PayloadHash(kind, b)}, nil
}

// loadTask reads every trusted copy of the task.
func (r *run) loadTask() bool {
	type source struct{ where, hash string }
	var found []source
	var ledgerEntry *task.Entry
	var specs []task.Spec
	var problems []string
	refKind := ""
	if r.opt.TaskRef != "" {
		refKind = taskRefKind(r.ctx, r.opt.RepoDir, r.opt.TaskRef)
		if refKind != "commit" && refKind != "remote" {
			r.warnings = append(r.warnings, fmt.Sprintf("--task-ref %s is a %s ref, which anyone with this repository on disk (the agent too) can move; use a freshly fetched remote ref (refs/remotes/origin/<branch>) or a commit id", r.opt.TaskRef, refKind))
		}
		where, s, h, err := readTaskAtRef(r.ctx, r.opt.RepoDir, r.opt.TaskRef, r.opt.TaskID)
		if err != nil {
			problems = append(problems, err.Error())
		} else {
			found = append(found, source{where, h})
			specs = append(specs, s)
		}
	}
	if r.opt.DataDir != "" || r.opt.Ledger != nil {
		copies, err := task.Load(r.ctx, r.opt.TaskID, task.Store{DataDir: r.opt.DataDir, Ledger: r.opt.Ledger})
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil && copies == nil:
			problems = append(problems, err.Error())
		}
		for _, c := range copies {
			found = append(found, source{c.Where, c.Hash})
			specs = append(specs, c.Spec)
			if c.Where == "ledger" {
				ledgerEntry = c.Entry
			}
		}
	}
	trusted := false
	for i, f := range found {
		r.sources = append(r.sources, map[string]any{"where": f.where, "hash": f.hash})
		if r.opt.TaskRef != "" && i == 0 && strings.HasPrefix(f.where, "git:") {
			trusted = true
		}
	}
	// A ledger is trusted only against a pinned prefix.
	ledgerTrust := ""
	if ledgerEntry != nil {
		ok, detail, broken := r.ledgerPinned(*ledgerEntry)
		switch {
		case broken:
			problems = append(problems, "ledger: "+detail)
		case ok:
			trusted = true
			ledgerTrust = "pinned"
		default:
			ledgerTrust = detail
		}
	}
	if r.opt.TaskHash != "" && len(found) > 0 {
		if found[0].hash != r.opt.TaskHash {
			problems = append(problems, fmt.Sprintf("%s has %s, --task-hash is %s", found[0].where, found[0].hash, r.opt.TaskHash))
		} else {
			trusted = true
		}
	}
	ev := map[string]any{"sources": r.sources, "problems": problems, "trusted": trusted, "pinned_hash": r.opt.TaskHash,
		"ledger_prefix": r.opt.ExpectPrefix, "ledger_trust": ledgerTrust, "task_ref_kind": refKind}
	if len(found) == 0 {
		id := r.addEvidence("task_snapshot", "", withStatus(ev, "fail"))
		detail := "no trusted copy of the task was found"
		if len(problems) > 0 {
			detail += ": " + strings.Join(problems, "; ")
		}
		r.reason(CodeTaskSnapshotMissing, id, "%s", detail)
		return false
	}
	for _, f := range found[1:] {
		if f.hash != found[0].hash {
			id := r.addEvidence("task_snapshot", "", withStatus(ev, "fail"))
			r.reason(CodeTaskHashMismatch, id, "%s has %s, %s has %s", found[0].where, found[0].hash, f.where, f.hash)
			return false
		}
	}
	if len(problems) > 0 {
		id := r.addEvidence("task_snapshot", "", withStatus(ev, "fail"))
		r.reason(CodeTaskHashMismatch, id, "%s", strings.Join(problems, "; "))
		return false
	}
	r.spec, r.taskHash = specs[0], found[0].hash
	if !trusted {
		// Run everything, but the verdict cannot be PASS.
		id := r.addEvidence("task_snapshot", "", withStatus(ev, "untrusted"))
		detail := fmt.Sprintf("the task came only from %s, which the agent can write; pass --task-ref (a freshly fetched protected ref), --task-hash, or --expect-prefix for the ledger", found[0].where)
		if ledgerTrust != "" {
			detail += " (ledger: " + ledgerTrust + ")"
		}
		r.reason(CodeTaskUntrusted, id, "%s", detail)
		return true
	}
	r.addEvidence("task_snapshot", "", withStatus(ev, "pass"))
	return true
}

// verifiableLedger is the part of ledger.Store a prefix pin needs.
type verifiableLedger interface {
	Verify(ctx context.Context) (ledger.Report, error)
}

var prefixRe = regexp.MustCompile(`^(0|[1-9][0-9]{0,18}):(sha256:[0-9a-f]{64})$`)

// ledgerPinned checks the ledger against Options.ExpectPrefix. ok means the
// task entry is inside the pinned, verified prefix. broken means the
// ledger contradicts the pin (FAIL); otherwise detail says why the ledger
// is not trusted.
func (r *run) ledgerPinned(taskEntry task.Entry) (ok bool, detail string, broken bool) {
	if r.opt.ExpectPrefix == "" {
		return false, "no --expect-prefix; an unpinned ledger is not trusted", false
	}
	m := prefixRe.FindStringSubmatch(r.opt.ExpectPrefix)
	seq, _ := strconv.ParseInt(m[1], 10, 64)
	vl, can := r.opt.Ledger.(verifiableLedger)
	if !can {
		return false, "this ledger cannot be verified", false
	}
	rep, err := vl.Verify(r.ctx)
	switch {
	case err != nil:
		return false, "verify: " + err.Error(), true
	case !rep.OK:
		return false, "the chain does not verify", true
	case rep.Head == nil || rep.Head.Seq < seq:
		return false, fmt.Sprintf("the ledger ends before the pinned seq %d", seq), true
	case rep.Head.Seq == seq && rep.Head.EntryHash != m[2]:
		return false, fmt.Sprintf("entry %d is %s, the pin says %s", seq, rep.Head.EntryHash, m[2]), true
	case rep.Head.Seq > seq:
		// TODO(ledger): check entry seq by lookup once internal/ledger has
		// one; until then only an exact head pin can be checked.
		return false, fmt.Sprintf("the ledger has grown past the pinned seq %d (head %d); only the current head can be checked", seq, rep.Head.Seq), false
	case taskEntry.Seq > seq:
		return false, fmt.Sprintf("the task entry (seq %d) is after the pinned seq %d", taskEntry.Seq, seq), false
	}
	return true, "", false
}

func withStatus(v map[string]any, status string) map[string]any {
	v["status"] = status
	return v
}

// taskRefKind classifies a --task-ref: "commit" (a full id), "remote"
// (refs/remotes/...), or "local" / "tag" / "other" (movable on this disk).
func taskRefKind(ctx context.Context, repo, ref string) string {
	if fullSHA.MatchString(ref) {
		return "commit"
	}
	if strings.HasPrefix(ref, "-") {
		return "other"
	}
	// No --end-of-options: rev-parse echoes it back instead of parsing it.
	full, err := gitLine(ctx, repo, "rev-parse", "--symbolic-full-name", ref)
	switch {
	case err != nil || full == "":
		return "other"
	case strings.HasPrefix(full, "refs/remotes/"):
		return "remote"
	case strings.HasPrefix(full, "refs/heads/"):
		return "local branch"
	case strings.HasPrefix(full, "refs/tags/"):
		return "tag"
	}
	return "other"
}

func readTaskAtRef(ctx context.Context, repo, ref, id string) (string, task.Spec, string, error) {
	if strings.HasPrefix(ref, "-") {
		return "", task.Spec{}, "", fmt.Errorf("task ref %q is not a ref", ref)
	}
	commit, err := gitLine(ctx, repo, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err != nil {
		return "", task.Spec{}, "", fmt.Errorf("task ref %q does not resolve", ref)
	}
	p := task.RepoTaskDir + "/" + id + ".json"
	where := fmt.Sprintf("git:%s(%s):%s", ref, commit, p)
	b, err := git(ctx, repo, "cat-file", "blob", commit+":"+p)
	if err != nil {
		return "", task.Spec{}, "", fmt.Errorf("%s: not found", where)
	}
	s, h, err := task.Parse(b)
	if err != nil {
		return "", task.Spec{}, "", fmt.Errorf("%s: %w", where, err)
	}
	if s.ID != id {
		return "", task.Spec{}, "", fmt.Errorf("%s: holds task %q", where, s.ID)
	}
	return where, s, h, nil
}

// prepareCheckout fetches the submitted and base commits into a new
// repository in a temporary directory outside the workspace, and checks
// out the submitted commit detached. git verifies every fetched object's
// id against its content, so an object rewritten in the workspace's object
// store cannot pass as the submitted revision.
func (r *run) prepareCheckout() bool {
	ev := map[string]any{"method": "git-init-fetch-detach"}
	fail := func(code, format string, args ...any) bool {
		ev["error"] = fmt.Sprintf(format, args...)
		id := r.addEvidence("checkout", "", withStatus(ev, "fail"))
		r.reason(code, id, format, args...)
		return false
	}
	parent := r.opt.WorkDir
	if parent == "" {
		parent = os.TempDir()
	}
	tmp, err := os.MkdirTemp(parent, "sdf-verify-")
	if err != nil {
		return fail(CodeInfraError, "temporary directory: %v", err)
	}
	r.checkout = filepath.Join(tmp, "checkout")
	if inside(r.opt.RepoDir, tmp) {
		return fail(CodeInfraError, "the checkout %s would be inside the repository %s", tmp, r.opt.RepoDir)
	}
	if _, err := git(r.ctx, tmp, "init", "-q", "checkout"); err != nil {
		return fail(CodeInfraError, "%v", err)
	}
	uploadPack := "git -c uploadpack.allowAnySHA1InWant=true upload-pack"
	fetch := func(sha string) error {
		_, err := git(r.ctx, r.checkout, "fetch", "-q", "--no-tags", "--no-write-fetch-head", "--upload-pack="+uploadPack, r.opt.RepoDir, sha)
		return err
	}
	if err := fetch(r.opt.SHA); err != nil {
		return fail(CodeRevisionUnresolvable, "submitted %s cannot be fetched from %s: %v", r.opt.SHA, r.opt.RepoDir, err)
	}
	if err := fetch(r.spec.BaseSHA); err != nil {
		return fail(CodeBaseMismatch, "base %s cannot be fetched from %s: %v", r.spec.BaseSHA, r.opt.RepoDir, err)
	}
	if _, err := git(r.ctx, r.checkout, "-c", "advice.detachedHead=false", "checkout", "-q", "--detach", r.opt.SHA); err != nil {
		return fail(CodeRevisionUnresolvable, "checkout %s: %v", r.opt.SHA, err)
	}
	if afterCheckout != nil {
		afterCheckout(r.checkout)
	}
	sum := sha256.Sum256([]byte(r.checkout))
	ev["path_sha256"] = hex.EncodeToString(sum[:])
	r.addEvidence("checkout", "", withStatus(ev, "pass"))
	return true
}

// afterCheckout, when set by a test, runs right after the checkout: it
// stands in for another process writing the directory.
var afterCheckout func(dir string)

// inside reports whether p is dir or below it, after resolving symlinks.
func inside(dir, p string) bool {
	d, err := filepath.EvalSymlinks(dir)
	if err != nil {
		d = dir
	}
	q, err := filepath.EvalSymlinks(p)
	if err != nil {
		q = p
	}
	rel, err := filepath.Rel(d, q)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func (r *run) checkRevision() {
	c := r.checkout
	head, _ := gitLine(r.ctx, c, "rev-parse", "HEAD")
	tree, _ := gitLine(r.ctx, c, "rev-parse", "HEAD^{tree}")
	baseTree, _ := gitLine(r.ctx, c, "rev-parse", r.spec.BaseSHA+"^{tree}")
	_, ancErr := git(r.ctx, c, "merge-base", "--is-ancestor", r.spec.BaseSHA, r.opt.SHA)
	status, statusErr := git(r.ctx, c, "status", "--porcelain", "--untracked-files=all")
	r.tree = tree
	clean := statusErr == nil && len(status) == 0
	ev := map[string]any{
		"head_sha":         head,
		"tree":             tree,
		"base_sha":         r.spec.BaseSHA,
		"base_tree":        baseTree,
		"base_is_ancestor": ancErr == nil,
		"clean":            clean,
	}
	var codes []func(id string)
	if head != r.opt.SHA {
		codes = append(codes, func(id string) {
			r.reason(CodeRevisionMismatch, id, "checkout HEAD is %s, submitted %s", head, r.opt.SHA)
		})
	}
	if baseTree != r.spec.BaseTree {
		codes = append(codes, func(id string) {
			r.reason(CodeBaseMismatch, id, "base %s has tree %s, the task says %s", r.spec.BaseSHA, baseTree, r.spec.BaseTree)
		})
	}
	if ancErr != nil {
		codes = append(codes, func(id string) {
			r.reason(CodeBaseNotAncestor, id, "base %s is not an ancestor of %s", r.spec.BaseSHA, r.opt.SHA)
		})
	}
	if !clean {
		codes = append(codes, func(id string) {
			r.reason(CodeCheckoutDirty, id, "fresh checkout is not clean")
		})
	}
	status2 := "pass"
	if len(codes) > 0 {
		status2 = "fail"
	}
	id := r.addEvidence("revision_identity", "", withStatus(ev, status2))
	for _, f := range codes {
		f(id)
	}
}

type treeEntry struct{ mode, oid string }

func lsTree(ctx context.Context, dir, rev string) (map[string]treeEntry, error) {
	b, err := git(ctx, dir, "ls-tree", "-r", "-z", "--full-tree", rev)
	if err != nil {
		return nil, err
	}
	out := map[string]treeEntry{}
	for _, rec := range strings.Split(string(b), "\x00") {
		meta, p, ok := strings.Cut(rec, "\t")
		if !ok {
			continue
		}
		f := strings.Fields(meta)
		if len(f) == 3 && f[1] == "blob" {
			out[p] = treeEntry{mode: f[0], oid: f[2]}
		}
	}
	return out, nil
}

// checkIntegrity compares every designated file at the submitted revision
// with the task: same mode, same blob, and the same sha256 on disk in the
// checkout (which also catches a checkout filter). A new file matching a
// designated glob, or a changed copy of this task's file, fails too.
func (r *run) checkIntegrity() {
	ev := map[string]any{}
	at, err := lsTree(r.ctx, r.checkout, r.opt.SHA)
	atBase, err2 := lsTree(r.ctx, r.checkout, r.spec.BaseSHA)
	if err = errors.Join(err, err2); err != nil {
		ev["error"] = err.Error()
		id := r.addEvidence("designated_integrity", "", withStatus(ev, "error"))
		r.reason(CodeInfraError, id, "%v", err)
		return
	}
	r.treeAtSHA = at
	r.checkTamperCapable(at, atBase)
	// The base has to hold exactly what the task pinned, or the task does
	// not describe this repository's base.
	{
		var off []string
		for _, f := range r.spec.DesignatedFiles {
			if b, ok := atBase[f.Path]; !ok || b.mode != f.Mode || b.oid != f.GitBlob {
				off = append(off, f.Path)
			}
		}
		ev["base_matches_task"] = len(off) == 0
		if len(off) > 0 {
			r.reasonLater = append(r.reasonLater, Reason{Code: CodeBaseMismatch, Detail: "designated files at the base differ from the task: " + strings.Join(off, ", ")})
		}
	}
	var modified, added []any
	var msgs []string
	known := map[string]bool{}
	for _, f := range r.spec.DesignatedFiles {
		known[f.Path] = true
		got, ok := at[f.Path]
		problem := ""
		switch {
		case !ok:
			problem = "deleted"
		case got.mode != f.Mode:
			problem = "mode " + f.Mode + " -> " + got.mode
		case got.oid != f.GitBlob:
			problem = "content changed"
		default:
			sum, err := fileSHA256(filepath.Join(r.checkout, filepath.FromSlash(f.Path)), f.Mode)
			if err != nil {
				problem = "unreadable in checkout: " + err.Error()
			} else if sum != f.SHA256 {
				problem = "checkout content differs from the base"
			}
		}
		if problem != "" {
			blob := ""
			if ok {
				blob = got.oid
			}
			modified = append(modified, map[string]any{"path": f.Path, "problem": problem, "git_blob": blob})
			msgs = append(msgs, f.Path+": "+problem)
		}
	}
	paths := make([]string, 0, len(at))
	for p := range at {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		if known[p] {
			continue
		}
		for _, c := range r.spec.Criteria {
			hit := false
			for _, g := range c.Designated {
				if task.MatchGlob(g, p) {
					hit = true
					break
				}
			}
			// A Go test input (a _test.go file, testdata, go.mod, go.work)
			// of a package the criterion tests that is new or changed is as
			// good as a new test: a TestMain there decides the result.
			if !hit && goInput(c.Argv, p) {
				b, ok := atBase[p]
				hit = !ok || b != at[p]
			}
			if hit {
				added = append(added, map[string]any{"path": p, "criterion": c.Name, "git_blob": at[p].oid})
				break
			}
		}
	}
	// A vendored file deleted since the base changes what gets built too.
	for _, c := range r.spec.Criteria {
		if !isGoTool(c.Argv) {
			continue
		}
		var gone []string
		for p := range atBase {
			if _, ok := at[p]; !ok && strings.HasPrefix(p, "vendor/") {
				gone = append(gone, p)
			}
		}
		sort.Strings(gone)
		for _, p := range gone {
			added = append(added, map[string]any{"path": p, "criterion": c.Name, "git_blob": "", "deleted": true})
		}
		break
	}
	// A symlink that is new or changed since the base and points outside
	// the checkout would let a check read or run something the task never
	// saw.
	var escapes []any
	var escapeMsgs []string
	for _, p := range paths {
		e := at[p]
		if e.mode != "120000" || atBase[p] == e {
			continue
		}
		target, err := git(r.ctx, r.checkout, "cat-file", "blob", e.oid)
		if err != nil || isSymlinkOutside(p, string(target)) {
			escapes = append(escapes, map[string]any{"path": p, "git_blob": e.oid})
			escapeMsgs = append(escapeMsgs, p)
		}
	}
	ev["symlinks_outside"] = nonNilAny(escapes)
	taskFile := task.RepoTaskDir + "/" + r.spec.ID + ".json"
	taskFileState := "absent"
	if e, ok := at[taskFile]; ok {
		taskFileState = "same"
		b, err := git(r.ctx, r.checkout, "cat-file", "blob", e.oid)
		if err != nil {
			taskFileState = "unreadable"
		} else if _, h, err := task.Parse(b); err != nil || h != r.taskHash {
			taskFileState = "changed"
		}
	}
	ev["modified"] = nonNilAny(modified)
	ev["added"] = nonNilAny(added)
	ev["task_file"] = taskFileState
	ev["checked"] = int64(len(r.spec.DesignatedFiles))
	status := "pass"
	if len(modified) > 0 || len(added) > 0 || len(escapes) > 0 || (taskFileState != "absent" && taskFileState != "same") || len(r.reasonLater) > 0 {
		status = "fail"
	}
	id := r.addEvidence("designated_integrity", "", withStatus(ev, status))
	for _, x := range r.reasonLater {
		x.EvidenceID = id
		r.reasons = append(r.reasons, x)
	}
	r.reasonLater = nil
	if len(modified) > 0 {
		r.reason(CodeDesignatedFileModified, id, "%s", strings.Join(msgs, "; "))
	}
	if len(added) > 0 {
		var ps []string
		for _, a := range added {
			ps = append(ps, a.(map[string]any)["path"].(string))
		}
		r.reason(CodeDesignatedFileAdded, id, "new files match designated globs: %s", strings.Join(ps, ", "))
	}
	if len(escapes) > 0 {
		r.reason(CodeSymlinkOutside, id, "new symlinks point outside the checkout: %s", strings.Join(escapeMsgs, ", "))
	}
	if taskFileState != "absent" && taskFileState != "same" {
		r.reason(CodeTaskFileModified, id, "%s at the submitted revision is %s", taskFile, taskFileState)
	}
}

func nonNilAny(v []any) []any {
	if v == nil {
		return []any{}
	}
	return v
}

// fileSHA256 hashes the checkout file at p without following a symlink:
// the on-disk type has to match the git mode, and a link's hash is of its
// target string.
func fileSHA256(p, mode string) (string, error) {
	info, err := os.Lstat(p)
	if err != nil {
		return "", err
	}
	isLink := info.Mode()&fs.ModeSymlink != 0
	if isLink != (mode == "120000") || (!isLink && !info.Mode().IsRegular()) {
		return "", fmt.Errorf("on-disk type %s does not match git mode %s", info.Mode().Type(), mode)
	}
	var b []byte
	if mode == "120000" {
		var target string
		target, err = os.Readlink(p)
		b = []byte(filepath.ToSlash(target))
	} else {
		b, err = os.ReadFile(p)
	}
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// scanSecrets checks every commit in base..SHA, not only the net diff: the
// message, and each added or changed blob's new lines, read from the
// objects (no diff, so no textconv or -diff attribute hides a file; binary
// blobs are scanned too and listed).
func (r *run) scanSecrets() {
	ev := map[string]any{"ruleset": SecretRulesetID, "ruleset_sha256": SecretRulesetHash(), "range": r.spec.BaseSHA + ".." + r.opt.SHA}
	findings, commits, binary, err := r.secretFindings()
	if err != nil {
		ev["error"] = err.Error()
		id := r.addEvidence("secret_scan", "", withStatus(ev, "error"))
		r.reason(CodeInfraError, id, "secret scan: %v", err)
		return
	}
	ev["commits"] = int64(commits)
	ev["binary_scanned"] = nonNilStrings(binary)
	var fs []any
	var where []string
	for _, f := range findings {
		fs = append(fs, map[string]any{"rule": f.Rule, "commit": f.Commit, "path": f.Path, "line": f.Line, "match_sha256": f.MatchSHA256})
		loc := f.Path
		if loc == "" {
			loc = "commit header or message"
		}
		where = append(where, fmt.Sprintf("%s %q:%d (%s)", f.Commit[:12], loc, f.Line, f.Rule))
	}
	ev["findings"] = nonNilAny(fs)
	status := "pass"
	if len(findings) > 0 {
		status = "fail"
	}
	id := r.addEvidence("secret_scan", "", withStatus(ev, status))
	if len(findings) > 0 {
		r.reason(CodeSecretInDiff, id, "%s", strings.Join(where, ", "))
	}
}

func (r *run) secretFindings() ([]SecretFinding, int, []string, error) {
	list, err := git(r.ctx, r.checkout, "rev-list", "--reverse", r.spec.BaseSHA+".."+r.opt.SHA)
	if err != nil {
		return nil, 0, nil, err
	}
	commits := strings.Fields(string(list))
	var findings []SecretFinding
	var binary []string
	scanned := map[string]bool{}
	for _, c := range commits {
		obj, err := git(r.ctx, r.checkout, "cat-file", "commit", c)
		if err != nil {
			return nil, 0, nil, err
		}
		// The whole commit object: author, committer, any extra header,
		// and the message.
		findings = append(findings, scanText(c, "", obj, nil)...)
		raw, err := git(r.ctx, r.checkout, "diff-tree", "-r", "-z", "--no-renames", "--no-abbrev", c+"^", c)
		if err != nil {
			return nil, 0, nil, err
		}
		for _, ch := range parseRaw(raw) {
			if ch.status == "D" || ch.newMode == "160000" || scanned[ch.newOID+"\x00"+ch.oldOID] {
				continue
			}
			scanned[ch.newOID+"\x00"+ch.oldOID] = true
			// The path name itself.
			for _, f := range scanText(c, "", []byte(ch.path), nil) {
				f.Path = safePath(ch.path)
				f.Line = 0
				findings = append(findings, f)
			}
			blob, err := git(r.ctx, r.checkout, "cat-file", "blob", ch.newOID)
			if err != nil {
				return nil, 0, nil, err
			}
			var old map[string]bool
			if strings.Trim(ch.oldOID, "0") != "" && ch.oldMode != "160000" {
				ob, err := git(r.ctx, r.checkout, "cat-file", "blob", ch.oldOID)
				if err != nil {
					return nil, 0, nil, err
				}
				old = lineSet(ob)
			}
			if bytes.IndexByte(blob, 0) >= 0 {
				binary = append(binary, safePath(ch.path))
			}
			findings = append(findings, scanText(c, safePath(ch.path), blob, old)...)
		}
	}
	sort.Strings(binary)
	return findings, len(commits), binary, nil
}

func nonNilStrings(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func (r *run) runCriterion(c task.Criterion) {
	d, dirErr := r.criterionDirs(c)
	defer func() { _ = removeTree(d.root) }()
	env, envNames, envHash := checkEnv(r.opt.Path, d)
	ev := map[string]any{
		"cwd":         ".",
		"path":        r.opt.Path,
		"argv":        c.Argv,
		"timeout_s":   c.TimeoutS,
		"expect_exit": c.ExpectExit,
		"isolation":   r.isolation,
		"tools":       toolVersions(r.ctx),
	}
	fail := func(code, format string, args ...any) {
		ev["error"] = fmt.Sprintf(format, args...)
		st := "error"
		if failCodes[code] {
			st = "fail"
		}
		id := r.addEvidence("criterion", c.Name, withStatus(ev, st))
		r.reason(code, id, "%s: %s", c.Name, ev["error"])
	}
	// The checkout must still be the submitted tree before the check runs:
	// an earlier check may have changed a file this one uses.
	if problems := r.checkDisk(); len(problems) > 0 {
		ev["checkout_before"] = problems
		fail(CodeCheckoutModified, "not run: the checkout differs from %s: %s", r.opt.SHA, strings.Join(problems, "; "))
		return
	}
	if dirErr != nil {
		fail(CodeInfraError, "criterion directories: %v", dirErr)
		return
	}
	// Every criterion after the first starts from a fresh copy of the
	// submitted tree: nothing an earlier check left (an untracked .go file,
	// a build output, a config) carries over (atk_plant).
	if r.ranCheck {
		if err := r.resetCheckout(); err != nil {
			fail(CodeCheckoutModified, "not run: %v", err)
			return
		}
		ev["fresh_checkout"] = true
	}
	// The command must still be the file pinned before any check ran.
	if pin, ok := r.pins[c.Name]; ok {
		p, sum, err := r.resolveCommand(c.Argv[0])
		ev["command_sha256"] = sum
		if err != nil || p != pin.path || sum != pin.sha256 {
			got := p + " " + sum
			if err != nil {
				got = err.Error()
			}
			fail(CodeCommandChanged, "not run: %s was %s %s before the checks, now %s", c.Argv[0], pin.path, pin.sha256, got)
			return
		}
	}
	goTest := task.IsGoTest(c.Argv)
	var h *harness
	var wantTests []string
	goroot := ""
	argv := c.Argv
	if goTest {
		var err error
		if wantTests, err = designatedTests(r.checkout, r.spec, c); err != nil {
			fail(CodeInfraError, "reading designated tests: %v", err)
			return
		}
		ev["designated_tests"] = nonNilStrings(wantTests)
		if c.ExpectExit == 0 && len(wantTests) == 0 {
			fail(CodeDesignatedTestMissing, "no designated test to require: designate the package's _test.go files")
			return
		}
		if _, err := goTestArgv(argv, "", ""); err != nil {
			fail(CodeCheckError, "%v", err)
			return
		}
		goCmd, err := lookPath(argv[0], r.checkout, r.opt.Path)
		if err != nil {
			fail(CodeCheckError, "%v", err)
			return
		}
		goroot, err = goEnvGOROOT(r.ctx, goCmd, env, filepath.Dir(r.checkout))
		if err != nil {
			fail(CodeInfraError, "go env GOROOT: %v", err)
			return
		}
		ev["goroot"] = goroot
		if h, err = r.newHarness(c, wantTests, goroot, d); err != nil {
			fail(CodeInfraError, "go test harness: %v", err)
			return
		}
		argv, _ = goTestArgv(argv, h.overlay, h.exec)
		gotmp := d.gotmp
		// No cgo: a C constructor would run before the hook's init.
		env = append(env, "CGO_ENABLED=0", "GOTMPDIR="+gotmp)
		envNames = append(envNames, "CGO_ENABLED", "GOTMPDIR")
		sort.Strings(envNames)
	}
	ev["run_argv"] = argv
	ev["env_names"], ev["env_sha256"] = envNames, envHash
	outDir := filepath.Dir(r.checkout)
	stdout, errOut := filepath.Join(outDir, c.Name+".stdout"), filepath.Join(outDir, c.Name+".stderr")
	ctx, cancel := context.WithTimeout(r.ctx, time.Duration(c.TimeoutS)*time.Second)
	defer cancel()
	command, err := lookPath(argv[0], r.checkout, r.opt.Path)
	if err != nil {
		if h != nil {
			h.finish()
		}
		fail(CodeCheckError, "%v", err)
		return
	}
	ev["command"] = command
	var cmd *exec.Cmd
	ev["network"] = "host"
	ev["checkout_writable"] = r.isolation != IsolationNamespace || !isGoTool(c.Argv) || contains(r.opt.WritableCheckout, c.Name)
	if r.isolation == IsolationNamespace {
		cfg, unmasked := r.sandboxConfig(c, command, goroot, h, d)
		if !cfg.hostNet {
			ev["network"] = "loopback"
		}
		ev["masked"], ev["unmasked"] = nonNilStrings(cfg.mask), nonNilStrings(unmasked)
		ev["writable"] = nonNilStrings(cfg.rw)
		var ovl []string
		for _, o := range cfg.overlay {
			ovl = append(ovl, o.lower)
		}
		ev["read_only_caches"] = nonNilStrings(ovl)
		cfg.reportPid = h != nil
		if cmd, err = isolatedCommand(ctx, command, argv, cfg); err != nil {
			if h != nil {
				h.finish()
			}
			fail(CodeInfraError, "%v", err)
			return
		}
	} else {
		cmd = exec.CommandContext(ctx, command, argv[1:]...)
		cmd.Args[0] = argv[0]
		isolateProcess(cmd)
	}
	cmd.Dir = r.checkout
	cmd.Env = env
	cmd.WaitDelay = 5 * time.Second
	so, err1 := os.Create(stdout)
	se, err2 := os.Create(errOut)
	if err := errors.Join(err1, err2); err != nil {
		if h != nil {
			h.finish()
		}
		fail(CodeInfraError, "%v", err)
		return
	}
	cmd.Stdout, cmd.Stderr = so, se
	var pidR, pidW *os.File
	if h != nil && r.isolation == IsolationNamespace {
		if pidR, pidW, err = os.Pipe(); err != nil {
			h.finish()
			fail(CodeInfraError, "%v", err)
			return
		}
		cmd.ExtraFiles = []*os.File{pidW}
	}
	started := r.opt.Now()
	r.ranCheck = true
	runErr := cmd.Start()
	if pidW != nil {
		pidW.Close()
	}
	if runErr == nil {
		if h != nil {
			h.rootPid, h.isolated = cmd.Process.Pid, r.isolation == IsolationNamespace
			if pidR != nil {
				// pid 1 writes the go command's pid once it has started
				// it, or exits without writing (no launcher is accepted).
				var n int
				if _, err := fmt.Fscan(pidR, &n); err == nil {
					h.goNsPid = n
				}
			}
			close(h.started)
		}
		runErr = cmd.Wait()
	}
	if pidR != nil {
		pidR.Close()
	}
	// Isolated: pid 1 has exited, so the kernel has killed everything else
	// in the namespace. Not isolated: kill the process group (a setsid
	// child escapes; the verdict records isolation "none").
	if r.isolation != IsolationNamespace {
		reapGroup(cmd)
	}
	ended := r.opt.Now()
	so.Close()
	se.Close()
	var notPassed, violations []string
	keyed := 0
	if h != nil {
		notPassed, violations = h.finish()
		keyed = h.keyed
		ev["harness_test_binaries"] = keyed
		ev["designated_tests_not_passed"] = nonNilStrings(notPassed)
		ev["test_report_violations"] = nonNilStrings(violations)
	}
	ev["started_at"] = started.UTC().Format(task.TimeLayout)
	ev["ended_at"] = ended.UTC().Format(task.TimeLayout)
	ev["duration_ms"] = ended.Sub(started).Milliseconds()
	for _, s := range []struct{ key, path string }{{"stdout", stdout}, {"stderr", errOut}} {
		h, n, err := r.keepOutput(s.path)
		if err != nil {
			ev[s.key+"_sha256"], ev[s.key+"_bytes"] = nil, nil
			continue
		}
		ev[s.key+"_sha256"], ev[s.key+"_bytes"] = h, n
	}
	timedOut := errors.Is(ctx.Err(), context.DeadlineExceeded)
	ev["timed_out"] = timedOut
	var exitCode any
	if cmd.ProcessState != nil && cmd.ProcessState.ExitCode() >= 0 {
		exitCode = int64(cmd.ProcessState.ExitCode())
	}
	ev["exit_code"] = exitCode

	// The checkout must be the submitted tree, unmodified, afterwards,
	// read from disk (not from git's index, which a check could change).
	// Nothing the check started is running any more (isolated), so this
	// read cannot race a writer.
	after, addedCode := r.checkDiskAll()
	unchanged := len(after) == 0 && len(addedCode) == 0
	ev["checkout_unchanged"] = unchanged
	ev["checkout_after"] = nonNilStrings(append(append([]string{}, after...), addedCode...))

	var code, detail string
	switch {
	case len(violations) > 0:
		code, detail = CodeTestReportInvalid, strings.Join(violations, "; ")
	case timedOut:
		code, detail = CodeCheckTimeout, fmt.Sprintf("timed out after %ds", c.TimeoutS)
	case exitCode == nil && runErr != nil:
		code, detail = CodeCheckError, runErr.Error()
	case exitCode != c.ExpectExit:
		code, detail = CodeCheckFailed, fmt.Sprintf("exit %v, expected %d", exitCode, c.ExpectExit)
	case goTest && c.ExpectExit == 0 && len(notPassed) > 0:
		code, detail = CodeDesignatedTestMissing, fmt.Sprintf("%d of %d designated tests did not pass: %s", len(notPassed), len(wantTests), testList(notPassed))
		if keyed == 0 {
			detail += "; no test binary reached the verifier's harness"
		}
	}
	st := "pass"
	if code == CodeCheckError {
		st = "error"
	} else if code != "" || !unchanged {
		st = "fail"
	}
	id := r.addEvidence("criterion", c.Name, withStatus(ev, st))
	if code != "" {
		r.reason(code, id, "%s: %s", c.Name, detail)
	}
	if len(after) > 0 {
		r.reason(CodeCheckoutModified, id, "%s changed the checkout: %s", c.Name, strings.Join(after, "; "))
	}
	if len(addedCode) > 0 {
		r.reason(CodeCheckAddedCode, id, "%s left code files in the checkout that are not in the submitted tree (a later step could compile them): %s", c.Name, strings.Join(addedCode, "; "))
	}
}

// testList prints "<import path> <Test>" names as "TestX (pkg)".
func testList(names []string) string {
	out := make([]string, len(names))
	for i, n := range names {
		pkg, test, _ := strings.Cut(n, " ")
		out[i] = test + " (package " + pkg + ")"
	}
	return strings.Join(out, ", ")
}

// keepOutput hashes a captured stream, and copies it to ArtifactsDir as
// <sha256> when that is set.
func (r *run) keepOutput(p string) (string, int64, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	sum := hex.EncodeToString(h.Sum(nil))
	if r.opt.ArtifactsDir != "" {
		if err := os.MkdirAll(r.opt.ArtifactsDir, 0o755); err == nil {
			dst := filepath.Join(r.opt.ArtifactsDir, sum)
			if _, err := os.Stat(dst); errors.Is(err, fs.ErrNotExist) {
				if b, err := os.ReadFile(p); err == nil {
					_ = os.WriteFile(dst, b, 0o444)
				}
			}
		}
	}
	return "sha256:" + sum, n, nil
}

func toolVersions(ctx context.Context) map[string]any {
	out := map[string]any{"goos": runtime.GOOS, "goarch": runtime.GOARCH, "sdf_go": runtime.Version()}
	if v, err := gitLine(ctx, ".", "--version"); err == nil {
		out["git"] = v
	}
	if b, err := exec.CommandContext(ctx, "go", "version").Output(); err == nil {
		out["go"] = strings.TrimSpace(string(b))
	}
	rev := ""
	if bi, ok := debug.ReadBuildInfo(); ok {
		out["sdf_version"] = bi.Main.Version
		for _, s := range bi.Settings {
			if s.Key == "vcs.revision" {
				rev = s.Value
			}
		}
	}
	out["sdf_revision"] = rev
	return out
}

// finish builds the verdict and appends evidence and verdict to the ledger.
func (r *run) finish() (Result, error) {
	status := StatusPass
	for _, x := range r.reasons {
		if failCodes[x.Code] {
			status = StatusFail
			break
		}
		status = StatusError
	}
	criteriaRun := 0
	for _, e := range r.evidence {
		if e.Value["kind"] == "criterion" {
			criteriaRun++
		}
	}
	if status == StatusPass && (criteriaRun == 0 || criteriaRun != len(r.spec.Criteria)) {
		status = StatusError
		r.reason(CodeInfraError, "", "not every criterion ran")
	}
	reasons := make([]any, len(r.reasons))
	for i, x := range r.reasons {
		reasons[i] = map[string]any{"code": x.Code, "detail": x.Detail, "evidence_id": x.EvidenceID}
	}
	evidence := make([]any, len(r.evidence))
	for i, e := range r.evidence {
		evidence[i] = map[string]any{"id": e.ID, "hash": e.Hash}
	}
	v := map[string]any{
		"schema":         VerdictSchema,
		"id":             r.id,
		"task_id":        r.opt.TaskID,
		"task_hash":      r.taskHash,
		"task_sources":   nonNilAny(r.sources),
		"submitted_sha":  r.opt.SHA,
		"tree":           r.tree,
		"base_sha":       r.spec.BaseSHA,
		"status":         status,
		"reasons":        reasons,
		"evidence":       evidence,
		"verifier":       Verifier,
		"started_at":     r.started.Format(task.TimeLayout),
		"ended_at":       r.now(),
		"secret_ruleset": SecretRulesetHash(),
		"isolation":      r.isolation,
	}
	verdict, err := makeRecord(task.KindVerdict, r.id, v)
	if err != nil {
		return Result{}, err
	}
	res := Result{Status: status, Reasons: r.reasons, Evidence: r.evidence, Verdict: verdict, Warnings: r.warnings}
	if r.isolation == IsolationNone && len(r.spec.Criteria) > 0 && r.opt.AllowUnisolated {
		res.Warnings = append(res.Warnings, "checks ran without isolation (--allow-unisolated): a process a check left behind could have changed files the verifier read; this verdict says isolation \"none\"")
	}
	if l := r.opt.Ledger; l != nil {
		recs := append(append([]Record(nil), r.evidence...), verdict)
		if b, ok := l.(task.BatchLedger); ok {
			// One transaction: the evidence and the verdict are chained
			// together or not at all.
			items := make([]task.Item, len(recs))
			for i, rec := range recs {
				items[i] = task.Item{Kind: rec.Kind, ID: rec.ID, Canonical: rec.Canonical, Hash: rec.Hash}
			}
			if _, err := b.AppendAll(r.ctx, items); err != nil {
				return res, fmt.Errorf("ledger: nothing appended: %w", err)
			}
			return res, nil
		}
		for _, rec := range recs {
			if _, err := l.Append(r.ctx, rec.Kind, rec.ID, rec.Canonical, rec.Hash); err != nil {
				return res, fmt.Errorf("ledger: %s %s: %w", rec.Kind, rec.ID, err)
			}
		}
	}
	return res, nil
}

// goEnvGOROOT asks the criterion's go command for its GOROOT, with the
// check's environment, outside the checkout.
func goEnvGOROOT(ctx context.Context, goCmd string, env []string, dir string) (string, error) {
	cmd := exec.CommandContext(ctx, goCmd, "env", "GOROOT")
	cmd.Env, cmd.Dir = env, dir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	g := strings.TrimSpace(string(out))
	if !filepath.IsAbs(g) {
		return "", fmt.Errorf("GOROOT %q is not absolute", g)
	}
	return g, nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
