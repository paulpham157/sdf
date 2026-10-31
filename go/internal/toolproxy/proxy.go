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
// binding → credential scope → allowlist → idempotency fence → executor.
type Proxy struct {
	exec Executor

	mu     sync.Mutex
	scopes map[string]SandboxScope // attempt_id → scope
	// durable outcomes: attempt_id\0action_id → status (executed|failed)
	outcomes map[string]string
	results  map[string]Result // cached value for replay
}

// NewProxy builds a Proxy. exec may be nil (returns nil value on allow).
func NewProxy(exec Executor) *Proxy {
	if exec == nil {
		exec = func(context.Context, ActionRequest) (any, error) { return map[string]any{"ok": true}, nil }
	}
	return &Proxy{
		exec:     exec,
		scopes:   make(map[string]SandboxScope),
		outcomes: make(map[string]string),
		results:  make(map[string]Result),
	}
}

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

	fenceKey := req.AttemptID + "\x00" + actionID
	p.mu.Lock()
	if st, ok := p.outcomes[fenceKey]; ok {
		cached := p.results[fenceKey]
		cached.Replay = true
		p.mu.Unlock()
		if st == StatusExecuted {
			return cached, nil
		}
		return cached, ErrFailed
	}
	p.mu.Unlock()

	val, err := p.exec(ctx, req)
	if err != nil {
		if ctx.Err() != nil {
			res := Result{
				AttemptID: req.AttemptID,
				ActionID:  actionID,
				Actor:     derived,
				Status:    StatusFailed,
				Effect:    "allow",
				Reason:    "sandbox cancelled or timed out",
				Policy:    PolicySandboxTimeout,
				Error:     ctx.Err().Error(),
			}
			p.storeOutcome(fenceKey, res)
			return res, fmt.Errorf("%w: %v", ErrFailed, ctx.Err())
		}
		res := Result{
			AttemptID: req.AttemptID,
			ActionID:  actionID,
			Actor:     derived,
			Status:    StatusFailed,
			Effect:    "allow",
			Reason:    "executor error",
			Error:     err.Error(),
		}
		p.storeOutcome(fenceKey, res)
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
	p.storeOutcome(fenceKey, res)
	return res, nil
}

func (p *Proxy) storeOutcome(key string, res Result) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.outcomes[key] = res.Status
	p.results[key] = res
}

// CallTimeout is a helper deadline for sandbox tool calls.
func CallTimeout(parent context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if d <= 0 {
		d = 30 * time.Second
	}
	return context.WithTimeout(parent, d)
}
