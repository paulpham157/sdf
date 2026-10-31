package toolproxy

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Executor runs one already-authorized action. Must honour ctx cancellation.
type Executor func(ctx context.Context, req ActionRequest) (any, error)

// Proxy is an in-process Tool Proxy matching sdf_core.tools.ToolProxy:
// binding → credential scope → allowlist → durable idempotency fence → executor.
type Proxy struct {
	exec  Executor
	store OutcomeStore

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
		scopes: make(map[string]SandboxScope),
	}
}

// Store returns the durable outcome fence.
func (p *Proxy) Store() OutcomeStore { return p.store }

// RegisterScope binds an Attempt to its sandbox credential fence.
func (p *Proxy) RegisterScope(scope SandboxScope) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.scopes[scope.AttemptID] = scope
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
			return Result{
				AttemptID: req.AttemptID,
				ActionID:  req.ActionID(),
				Status:    StatusDenied,
				Effect:    "deny",
				Reason:    fmt.Sprintf("reserved context key %q", k),
				Policy:    "context-reserved",
			}, ErrDenied
		}
	}

	p.mu.Lock()
	scope, ok := p.scopes[req.AttemptID]
	p.mu.Unlock()
	if !ok {
		return Result{
			AttemptID: req.AttemptID,
			ActionID:  req.ActionID(),
			Status:    StatusDenied,
			Effect:    "deny",
			Reason:    "attempt has no registered sandbox scope",
			Policy:    PolicyAttemptBinding,
		}, ErrDenied
	}

	// Server-derived actor always wins.
	derived := scope.DerivedActor()
	req.Actor = derived
	actionID := req.ActionID()

	if claimedActor != "" && claimedActor != derived {
		return Result{
			AttemptID: req.AttemptID,
			ActionID:  actionID,
			Actor:     derived,
			Status:    StatusDenied,
			Effect:    "deny",
			Reason:    fmt.Sprintf("claimed actor %q does not match %q", claimedActor, derived),
			Policy:    PolicyActorBinding,
		}, ErrDenied
	}
	if principal != "" && scope.OwnerPrincipal != "" && principal != scope.OwnerPrincipal {
		return Result{
			AttemptID: req.AttemptID,
			ActionID:  actionID,
			Actor:     derived,
			Status:    StatusDenied,
			Effect:    "deny",
			Reason:    fmt.Sprintf("principal %q did not start this attempt", principal),
			Policy:    PolicyAttemptBinding,
		}, ErrDenied
	}

	// Credential scope: agents only get credentials listed for their sandbox.
	if cred := CredentialNameFromRequest(req); cred != "" {
		if !scope.AllowsCredential(cred) {
			return Result{
				AttemptID: req.AttemptID,
				ActionID:  actionID,
				Actor:     derived,
				Status:    StatusDenied,
				Effect:    "deny",
				Reason:    fmt.Sprintf("credential %q is outside sandbox %q scope", cred, scope.SandboxID),
				Policy:    PolicyCredentialScope,
			}, ErrDenied
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
			return Result{
				AttemptID: req.AttemptID,
				ActionID:  actionID,
				Actor:     derived,
				Status:    StatusDenied,
				Effect:    "deny",
				Reason:    fmt.Sprintf("tool %q not in allowlist", req.Tool),
				Policy:    PolicyAllowlist,
			}, ErrDenied
		}
	}

	// Durable fence (ADR 0026): look up terminal outcome before claiming.
	if stored, found, err := p.store.Lookup(req.AttemptID, actionID); err != nil {
		return Result{}, err
	} else if found {
		res := resultFromStored(stored, true)
		if stored.Status == StatusFailed {
			return res, ErrFailed
		}
		return res, nil
	}

	okClaim, err := p.store.Claim(req.AttemptID, actionID)
	if err != nil {
		return Result{}, err
	}
	if !okClaim {
		// Lost the race: another delivery claimed or completed. Re-lookup.
		if stored, found, err := p.store.Lookup(req.AttemptID, actionID); err != nil {
			return Result{}, err
		} else if found {
			res := resultFromStored(stored, true)
			if stored.Status == StatusFailed {
				return res, ErrFailed
			}
			return res, nil
		}
		return Result{
			AttemptID: req.AttemptID,
			ActionID:  actionID,
			Actor:     derived,
			Status:    StatusFailed,
			Effect:    "allow",
			Reason:    "action is already claimed by another delivery; issue a new action identity to retry",
			Policy:    PolicyAllowlist,
			Error:     "action is already claimed by another delivery; issue a new action identity to retry",
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
		res := Result{
			AttemptID: req.AttemptID,
			ActionID:  actionID,
			Actor:     derived,
			Status:    failStatus,
			Effect:    "allow",
			Reason:    reason,
			Policy:    policy,
			Error:     errMsg,
		}
		_ = p.store.Complete(StoredOutcome{
			AttemptID: req.AttemptID,
			ActionID:  actionID,
			Status:    StatusFailed,
			Actor:     derived,
			Reason:    reason,
			Policy:    policy,
			Error:     errMsg,
			CreatedAt: time.Now().UTC(),
		})
		return res, fmt.Errorf("%w: %v", ErrFailed, err)
	}
	res := Result{
		AttemptID: req.AttemptID,
		ActionID:  actionID,
		Actor:     derived,
		Status:    StatusExecuted,
		Effect:    "allow",
		Reason:    "executor completed",
		Policy:    PolicyAllowlist,
		Value:     val,
	}
	if err := p.store.Complete(StoredOutcome{
		AttemptID: req.AttemptID,
		ActionID:  actionID,
		Status:    StatusExecuted,
		Actor:     derived,
		Reason:    res.Reason,
		Policy:    res.Policy,
		Value:     val,
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		return res, err
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
