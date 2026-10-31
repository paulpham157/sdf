// Package toolproxy is the Go adapter for SDF's Tool Proxy security boundary
// (ADR 0026, ADR 0035). Agents must not call tools or read credentials
// directly; structured ActionRequests go through this package.
package toolproxy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Status values mirror sdf_core.tools.ToolExecutionStatus.
const (
	StatusExecuted = "executed"
	StatusDenied   = "denied"
	StatusFailed   = "failed"
)

// Policy names used in denials.
const (
	PolicyCredentialScope = "credential-scope"
	PolicyActorBinding    = "actor-binding"
	PolicyAttemptBinding  = "attempt-binding"
	PolicyAllowlist       = "allowlist"
	PolicySandboxTimeout  = "sandbox-timeout"
)

var (
	// ErrDenied is returned when policy rejects the action.
	ErrDenied = errors.New("toolproxy: denied")
	// ErrFailed is returned when the executor fails after allow.
	ErrFailed = errors.New("toolproxy: failed")
)

// ActionRequest is the structured, Attempt-bound tool call (Python ActionRequest).
// Terminal text is never accepted. ActionID is a SHA-256 over the identity fields.
type ActionRequest struct {
	AttemptID string
	Actor     string // server-derived "agent:<id>"; claimed mismatches are denied
	Tool      string
	Action    string
	Resource  string
	Context   map[string]any
}

// ActionID matches Python: ACTION-<sha256(canonical JSON of identity fields)>.
func (r ActionRequest) ActionID() string {
	ctx := map[string]any{}
	if r.Context != nil {
		for k, v := range r.Context {
			ctx[k] = v
		}
	}
	payload := map[string]any{
		"attempt_id": r.AttemptID,
		"actor":      r.Actor,
		"tool":       r.Tool,
		"action":     r.Action,
		"resource":   r.Resource,
		"context":    ctx,
	}
	enc := mustCanonicalJSON(payload)
	sum := sha256.Sum256([]byte(enc))
	return "ACTION-" + hex.EncodeToString(sum[:])
}

func mustCanonicalJSON(v any) string {
	b, err := json.Marshal(canonicalize(v))
	if err != nil {
		return "{}"
	}
	return string(b)
}

func canonicalize(v any) any {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		return orderedMap{m: t, keys: keys}
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = canonicalize(e)
		}
		return out
	default:
		return v
	}
}

// orderedMap marshals with sorted keys (Python sort_keys=True).
type orderedMap struct {
	m    map[string]any
	keys []string
}

func (s orderedMap) MarshalJSON() ([]byte, error) {
	var b strings.Builder
	b.WriteByte('{')
	for i, k := range s.keys {
		if i > 0 {
			b.WriteByte(',')
		}
		kb, err := json.Marshal(k)
		if err != nil {
			return nil, err
		}
		b.Write(kb)
		b.WriteByte(':')
		vb, err := json.Marshal(canonicalize(s.m[k]))
		if err != nil {
			return nil, err
		}
		b.Write(vb)
	}
	b.WriteByte('}')
	return []byte(b.String()), nil
}

// Result is one Tool Proxy outcome.
type Result struct {
	AttemptID string
	ActionID  string
	Actor     string
	Status    string
	Effect    string // allow | deny
	Reason    string
	Policy    string
	Value     any
	Error     string
	Replay    bool // true if durable outcome was replayed (idempotent)
	// EvidenceMode is the mode of the policy_decided row this delivery
	// wrote (ADR 0031 §9). It is not the terminal execution row's mode.
	EvidenceMode string
}

// SandboxScope is the credential + identity fence for one Attempt's sandbox.
type SandboxScope struct {
	SandboxID          string
	AttemptID          string
	AgentID            string
	OwnerPrincipal     string
	AllowedCredentials []string // e.g. ANTHROPIC_API_KEY — empty means no credential tools
	AllowedTools       []string // empty = allow any tool name after credential checks
}

// AllowsCredential reports whether name is in the sandbox allow-list.
func (s SandboxScope) AllowsCredential(name string) bool {
	name = strings.TrimSpace(name)
	for _, a := range s.AllowedCredentials {
		if a == name {
			return true
		}
	}
	return false
}

// DerivedActor is agent:<AgentID>, matching the Python API.
func (s SandboxScope) DerivedActor() string {
	return fmt.Sprintf("agent:%s", s.AgentID)
}

// CredentialNameFromRequest extracts a credential env name when the action is
// a credentials tool or context carries "credential" / "env".
func CredentialNameFromRequest(r ActionRequest) string {
	if strings.EqualFold(r.Tool, "credentials") || strings.EqualFold(r.Tool, "credential") {
		if r.Resource != "" {
			return r.Resource
		}
	}
	if r.Context != nil {
		for _, k := range []string{"credential", "env", "env_name", "credential_name"} {
			if v, ok := r.Context[k]; ok {
				if s, ok := v.(string); ok && s != "" {
					return s
				}
			}
		}
	}
	return ""
}
