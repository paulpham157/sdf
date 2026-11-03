package toolproxy

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/paulpham157/sdf/go/internal/metrics"
)

// Executor runs one already-authorized action. Must honour ctx cancellation.
type Executor func(ctx context.Context, req ActionRequest) (any, error)

// Proxy is an in-process Tool Proxy matching sdf_core.tools.ToolProxy:
// binding → credential scope → allowlist → policy_decided audit →
// durable idempotency fence → executor.
// Every decision appends policy_decided, including denials and replays.
// action_claimed / action_executed / action_failed stay one per action.
type Proxy struct {
	exec    Executor
	store   OutcomeStore
	reg     *metrics.Registry
	modeSrc EvidenceModeSource

	mu     sync.Mutex
	scopes map[string]SandboxScope // attempt_id → scope
}

// NewProxy builds a Proxy. exec may be nil (returns a trivial ok value on allow).
// store may be nil (defaults to MemoryStore). Prefer OpenSQLiteStore for durability.
func NewProxy(exec Executor, store OutcomeStore) *Proxy {
	if exec == nil {
		exec = func(context.Context, ActionRequest) (any, error) { return map[string]any{"ok": true}, nil }
	}
	if store == nil {
		store = NewMemoryStore()
	}
	return &Proxy{
		exec:   exec,
		store:  store,
		reg:    metrics.Default,
		scopes: make(map[string]SandboxScope),
	}
}

// SetMetrics selects where Tool Proxy counters are recorded.
// Nil selects metrics.Default. Call it before Execute.
func (p *Proxy) SetMetrics(reg *metrics.Registry) {
	if reg == nil {
		reg = metrics.Default
	}
	p.reg = reg
}

// Store returns the durable outcome fence.
func (p *Proxy) Store() OutcomeStore { return p.store }

// SetEvidenceModeSource selects how each observation learns the Attempt's
// mode (ADR 0038). Nil stamps synthetic, the unknown-Attempt rule. Call it
// before Execute.
func (p *Proxy) SetEvidenceModeSource(src EvidenceModeSource) {
	p.mu.Lock()
	p.modeSrc = src
	p.mu.Unlock()
}

// RegisterScope binds an Attempt to its sandbox credential fence.
func (p *Proxy) RegisterScope(scope SandboxScope) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.scopes[scope.AttemptID] = scope
}

// DropScope removes a sandbox scope registered for attemptID.
func (p *Proxy) DropScope(attemptID string) {
	p.mu.Lock()
	delete(p.scopes, attemptID)
	p.mu.Unlock()
}

func (p *Proxy) stamp(attemptID string) (string, error) {
	p.mu.Lock()
	src := p.modeSrc
	p.mu.Unlock()
	if src == nil {
		return EvidenceModeSynthetic, nil
	}
	mode, ok, err := src(attemptID)
	if err != nil {
		return "", err
	}
	if !ok {
		return EvidenceModeSynthetic, nil
	}
	return StampEvidenceMode(mode), nil
}

// Execute runs the Tool Proxy path. claimedActor is optional (ADR 0026);
// empty means no claim. principal is the authenticated caller.
func (p *Proxy) Execute(ctx context.Context, req ActionRequest, claimedActor, principal string) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(req.AttemptID) == "" || strings.TrimSpace(req.Tool) == "" ||
		strings.TrimSpace(req.Action) == "" || strings.TrimSpace(req.Resource) == "" {
		return Result{}, fmt.Errorf("toolproxy: incomplete ActionRequest")
	}
	// Refuse reserved sdf_* context keys (ADR 0026).
	for k := range req.Context {
		if strings.HasPrefix(strings.ToLower(k), "sdf_") {
			reason := fmt.Sprintf("reserved context key %q", k)
			mode, err := p.appendDecision(req, req.ActionID(), req.Actor, principal, "deny", reason, "context-reserved")
			if err != nil {
				return Result{}, err
			}
			p.noteDeny()
			return Result{
				AttemptID:    req.AttemptID,
				ActionID:     req.ActionID(),
				Status:       StatusDenied,
				Effect:       "deny",
				Reason:       reason,
				Policy:       "context-reserved",
				EvidenceMode: mode,
			}, ErrDenied
		}
	}

	p.mu.Lock()
	scope, ok := p.scopes[req.AttemptID]
	p.mu.Unlock()
	if !ok {
		reason := "attempt has no registered sandbox scope"
		mode, err := p.appendDecision(req, req.ActionID(), req.Actor, principal, "deny", reason, PolicyAttemptBinding)
		if err != nil {
			return Result{}, err
		}
		p.noteDeny()
		return Result{
			AttemptID:    req.AttemptID,
			ActionID:     req.ActionID(),
			Status:       StatusDenied,
			Effect:       "deny",
			Reason:       reason,
			Policy:       PolicyAttemptBinding,
			EvidenceMode: mode,
		}, ErrDenied
	}

	// Server-derived actor always wins.
	derived := scope.DerivedActor()
	req.Actor = derived
	actionID := req.ActionID()

	deny := func(reason, policy string) (Result, error) {
		mode, err := p.appendDecision(req, actionID, derived, principal, "deny", reason, policy)
		if err != nil {
			return Result{}, err
		}
		p.noteDeny()
		return Result{
			AttemptID:    req.AttemptID,
			ActionID:     actionID,
			Actor:        derived,
			Status:       StatusDenied,
			Effect:       "deny",
			Reason:       reason,
			Policy:       policy,
			EvidenceMode: mode,
		}, ErrDenied
	}

	if claimedActor != "" && claimedActor != derived {
		return deny(fmt.Sprintf("claimed actor %q does not match %q", claimedActor, derived), PolicyActorBinding)
	}
	if principal != "" && scope.OwnerPrincipal != "" && principal != scope.OwnerPrincipal {
		return deny(fmt.Sprintf("principal %q did not start this attempt", principal), PolicyAttemptBinding)
	}

	// Credential scope: agents only get credentials listed for their sandbox.
	if cred := CredentialNameFromRequest(req); cred != "" {
		if !scope.AllowsCredential(cred) {
			return deny(fmt.Sprintf("credential %q is outside sandbox %q scope", cred, scope.SandboxID), PolicyCredentialScope)
		}
	}

	if len(scope.AllowedTools) > 0 {
		allowed := false
		for _, t := range scope.AllowedTools {
			if t == req.Tool {
				allowed = true
				break
			}
		}
		if !allowed {
			return deny(fmt.Sprintf("tool %q not in allowlist", req.Tool), PolicyAllowlist)
		}
	}

	// ADR 0026: record this delivery's decision before the fence, including
	// when the terminal outcome is replayed and the executor does not run.
	// The mode on that row is what Result reports (ADR 0031 §9).
	const allowReason = "action passed tool proxy checks"
	decisionMode, err := p.appendDecision(req, actionID, derived, principal, "allow", allowReason, PolicyAllowlist)
	if err != nil {
		return Result{}, err
	}

	// Durable fence (ADR 0026): look up terminal outcome before claiming.
	if stored, found, err := p.store.Lookup(req.AttemptID, actionID); err != nil {
		return Result{}, err
	} else if found {
		res, rerr := p.replay(stored)
		res.EvidenceMode = decisionMode
		return res, rerr
	}

	claimMode, err := p.stamp(req.AttemptID)
	if err != nil {
		return Result{}, err
	}
	okClaim, err := p.store.Claim(Observation{
		AttemptID:    req.AttemptID,
		ActionID:     actionID,
		Actor:        derived,
		Principal:    principal,
		EvidenceMode: claimMode,
		Tool:         req.Tool,
		Action:       req.Action,
		Resource:     req.Resource,
		Decision:     "allow",
		Reason:       "executor claim acquired",
		Policy:       PolicyAllowlist,
	})
	if err != nil {
		return Result{}, err
	}
	if !okClaim {
		// Lost the race: another delivery claimed or completed. Re-lookup.
		if stored, found, err := p.store.Lookup(req.AttemptID, actionID); err != nil {
			return Result{}, err
		} else if found {
			res, rerr := p.replay(stored)
			res.EvidenceMode = decisionMode
			return res, rerr
		}
		if p.reg != nil {
			p.reg.IncToolClaimStuck()
		}
		return Result{
			AttemptID:    req.AttemptID,
			ActionID:     actionID,
			Actor:        derived,
			Status:       StatusFailed,
			Effect:       "allow",
			Reason:       "action is already claimed by another delivery; issue a new action identity to retry",
			Policy:       PolicyAllowlist,
			Error:        "action is already claimed by another delivery; issue a new action identity to retry",
			EvidenceMode: decisionMode,
		}, ErrFailed
	}

	val, err := p.exec(ctx, req)
	if err != nil {
		failStatus := StatusFailed
		policy := ""
		reason := "executor error"
		errMsg := err.Error()
		if ctx.Err() != nil {
			policy = PolicySandboxTimeout
			reason = "sandbox cancelled or timed out"
			errMsg = ctx.Err().Error()
		}
		failMode, merr := p.stamp(req.AttemptID)
		if merr != nil {
			return Result{}, merr
		}
		res := Result{
			AttemptID:    req.AttemptID,
			ActionID:     actionID,
			Actor:        derived,
			Status:       failStatus,
			Effect:       "allow",
			Reason:       reason,
			Policy:       policy,
			Error:        errMsg,
			EvidenceMode: decisionMode,
		}
		_ = p.store.Complete(StoredOutcome{
			AttemptID:    req.AttemptID,
			ActionID:     actionID,
			Status:       StatusFailed,
			Actor:        derived,
			Principal:    principal,
			Tool:         req.Tool,
			Action:       req.Action,
			Resource:     req.Resource,
			Reason:       reason,
			Policy:       policy,
			Error:        errMsg,
			EvidenceMode: failMode,
			CreatedAt:    time.Now().UTC(),
		})
		return res, fmt.Errorf("%w: %v", ErrFailed, err)
	}
	execMode, merr := p.stamp(req.AttemptID)
	if merr != nil {
		return Result{}, merr
	}
	res := Result{
		AttemptID:    req.AttemptID,
		ActionID:     actionID,
		Actor:        derived,
		Status:       StatusExecuted,
		Effect:       "allow",
		Reason:       "executor completed",
		Policy:       PolicyAllowlist,
		Value:        val,
		EvidenceMode: decisionMode,
	}
	if err := p.store.Complete(StoredOutcome{
		AttemptID:    req.AttemptID,
		ActionID:     actionID,
		Status:       StatusExecuted,
		Actor:        derived,
		Principal:    principal,
		Tool:         req.Tool,
		Action:       req.Action,
		Resource:     req.Resource,
		Reason:       res.Reason,
		Policy:       res.Policy,
		Value:        val,
		EvidenceMode: execMode,
		CreatedAt:    time.Now().UTC(),
	}); err != nil {
		return res, err
	}
	if p.reg != nil {
		p.reg.IncToolExecuted()
	}
	return res, nil
}

func (p *Proxy) appendDecision(req ActionRequest, actionID, actor, principal, effect, reason, policy string) (string, error) {
	mode, err := p.stamp(req.AttemptID)
	if err != nil {
		return "", err
	}
	err = p.store.AppendPolicy(Observation{
		AttemptID:    req.AttemptID,
		ActionID:     actionID,
		Actor:        actor,
		Principal:    principal,
		EvidenceMode: mode,
		Tool:         req.Tool,
		Action:       req.Action,
		Resource:     req.Resource,
		Decision:     effect,
		Reason:       reason,
		Policy:       policy,
		CreatedAt:    time.Now().UTC(),
	})
	if err != nil {
		return "", err
	}
	return mode, nil
}

func (p *Proxy) noteDeny() {
	if p.reg != nil {
		p.reg.IncToolPolicyDeny()
	}
}

func (p *Proxy) replay(stored StoredOutcome) (Result, error) {
	if p.reg != nil {
		p.reg.IncToolReplay()
	}
	res := resultFromStored(stored, true)
	if stored.Status == StatusFailed {
		return res, ErrFailed
	}
	return res, nil
}

func resultFromStored(o StoredOutcome, replay bool) Result {
	return Result{
		AttemptID: o.AttemptID,
		ActionID:  o.ActionID,
		Actor:     o.Actor,
		Status:    o.Status,
		Effect:    "allow",
		Reason:    o.Reason,
		Policy:    o.Policy,
		Value:     o.Value,
		Error:     o.Error,
		Replay:    replay,
	}
}

// CallTimeout is a helper deadline for sandbox tool calls.
func CallTimeout(parent context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if d <= 0 {
		d = 30 * time.Second
	}
	return context.WithTimeout(parent, d)
}
