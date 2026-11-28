package live

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

func writeJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return os.WriteFile(path, b, 0o600)
}

func writePrivate(path, body string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(body), 0o600)
}

func tomlString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// OpenCode speaks Chat Completions through a project opencode.json.
// The provider schema is the one documented at opencode.ai/docs/providers:
// package @opencode/ai/providers/openai-compatible and settings.baseURL.
type opencodeAgent struct{}

func (opencodeAgent) ID() string     { return "opencode" }
func (opencodeAgent) Name() string   { return "OpenCode" }
func (opencodeAgent) Binary() string { return "opencode" }
func (opencodeAgent) Support() support {
	return support{OK: true, Protocol: protocolChat}
}
func (opencodeAgent) InstallCommands() [][]string {
	return [][]string{{"npm", "install", "-g", "@opencode/cli"}}
}
func (opencodeAgent) InstallHint() string {
	return "Install with npm install -g @opencode/cli, or pass --install."
}
func (opencodeAgent) Prepare(_ context.Context, spec spec) (invocation, error) {
	if err := installSkills(spec,
		filepath.Join(spec.Repo, ".opencode", "skills", "sdf-decisions"),
		filepath.Join(spec.Repo, ".agents", "skills", "sdf-decisions"),
		filepath.Join(spec.Home, ".config", "opencode", "skills", "sdf-decisions"),
	); err != nil {
		return invocation{}, err
	}
	cfg := map[string]any{
		"$schema": "https://opencode.ai/config.json",
		"model":   "tokenrouter/recall",
		"permission": map[string]string{
			"bash": "allow",
			"edit": "allow",
		},
		"providers": map[string]any{
			"tokenrouter": map[string]any{
				"name":    "TokenRouter",
				"env":     []string{"SDF_RECALL_API_KEY"},
				"package": "@opencode/ai/providers/openai-compatible",
				"settings": map[string]any{
					"baseURL": spec.OpenAIBase,
				},
				"models": map[string]any{
					"recall": map[string]any{
						"name":    "recall",
						"modelID": spec.Model,
						"capabilities": map[string]any{
							"tools":  true,
							"input":  []string{"text"},
							"output": []string{"text"},
						},
					},
				},
			},
		},
	}
	if err := writeJSON(filepath.Join(spec.Repo, "opencode.json"), cfg); err != nil {
		return invocation{}, err
	}
	return invocation{
		Argv: []string{"opencode", "run", "--standalone", "--model", "tokenrouter/recall", spec.Prompt},
		Env: map[string]string{
			"OTEL_EXPORTER_OTLP_ENDPOINT": spec.OTLP,
			"OPENCODE_DISABLE_AUTOUPDATE": "1",
		},
	}, nil
}

// Pi reads ~/.pi/agent/models.json. api is openai-completions. The key is
// the env interpolation $SDF_RECALL_API_KEY, which Pi expands and which is
// not the key itself.
type piAgent struct{}

func (piAgent) ID() string     { return "pi" }
func (piAgent) Name() string   { return "Pi" }
func (piAgent) Binary() string { return "pi" }
func (piAgent) Support() support {
	return support{OK: true, Protocol: protocolChat}
}
func (piAgent) InstallCommands() [][]string {
	return [][]string{
		{"npm", "install", "-g", "--ignore-scripts", "@earendil-works/pi-coding-agent"},
		{"pi", "install", "npm:pi-otel"},
	}
}
func (piAgent) InstallHint() string {
	return "Install with npm install -g --ignore-scripts @earendil-works/pi-coding-agent and pi install npm:pi-otel, or pass --install."
}
func (piAgent) Prepare(_ context.Context, spec spec) (invocation, error) {
	if err := installSkills(spec,
		filepath.Join(spec.Home, ".pi", "agent", "skills", "sdf-decisions"),
		filepath.Join(spec.Repo, ".pi", "skills", "sdf-decisions"),
		filepath.Join(spec.Repo, ".agents", "skills", "sdf-decisions"),
	); err != nil {
		return invocation{}, err
	}
	cfg := map[string]any{
		"$schema": "https://pi.dev/schemas/models.schema.json",
		"providers": map[string]any{
			"tokenrouter": map[string]any{
				"name":       "TokenRouter",
				"baseUrl":    spec.OpenAIBase,
				"api":        "openai-completions",
				"apiKey":     "$SDF_RECALL_API_KEY",
				"authHeader": true,
				"compat": map[string]any{
					"supportsDeveloperRole": false,
					"supportsStore":         false,
				},
				"models": []any{
					map[string]any{
						"id":            spec.Model,
						"name":          "recall",
						"reasoning":     false,
						"input":         []string{"text"},
						"contextWindow": 128000,
						"maxTokens":     8192,
						"cost": map[string]any{
							"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0,
						},
					},
				},
			},
		},
	}
	if err := writeJSON(filepath.Join(spec.Home, ".pi", "agent", "models.json"), cfg); err != nil {
		return invocation{}, err
	}
	return invocation{
		Argv: []string{"pi", "--print", "--provider", "tokenrouter", "--model", spec.Model, spec.Prompt},
		Env: map[string]string{
			"OTEL_EXPORTER_OTLP_ENDPOINT": spec.OTLP,
			"OTEL_EXPORTER_OTLP_PROTOCOL": "http/json",
			"PI_OTEL_METRICS":             "1",
			"PI_OTEL_LOGS":                "1",
			"PI_OTEL_SPAN_NAMING":         "genai",
		},
	}, nil
}

// Qwen Code uses the OpenAI-compatible auth type. wireApi defaults to
// Chat Completions; it is set explicitly. envKey names OPENAI_API_KEY.
type qwenAgent struct{}

func (qwenAgent) ID() string     { return "qwen" }
func (qwenAgent) Name() string   { return "Qwen Code" }
func (qwenAgent) Binary() string { return "qwen" }
func (qwenAgent) Support() support {
	return support{OK: true, Protocol: protocolChat}
}
func (qwenAgent) InstallCommands() [][]string {
	return [][]string{{"npm", "install", "-g", "@qwen-code/qwen-code"}}
}
func (qwenAgent) InstallHint() string {
	return "Install with npm install -g @qwen-code/qwen-code, or pass --install."
}
func (qwenAgent) Prepare(_ context.Context, spec spec) (invocation, error) {
	if err := installSkills(spec,
		filepath.Join(spec.Home, ".qwen", "skills", "sdf-decisions"),
		filepath.Join(spec.Repo, ".qwen", "skills", "sdf-decisions"),
		filepath.Join(spec.Repo, ".agents", "skills", "sdf-decisions"),
	); err != nil {
		return invocation{}, err
	}
	qwenHome := filepath.Join(spec.Home, ".qwen")
	cfg := map[string]any{
		"modelProviders": map[string]any{
			"openai": []any{
				map[string]any{
					"id":      spec.Model,
					"envKey":  "OPENAI_API_KEY",
					"baseUrl": spec.OpenAIBase,
					"wireApi": "chat-completions",
				},
			},
		},
		"security": map[string]any{
			"auth":        map[string]any{"selectedType": "openai"},
			"folderTrust": map[string]any{"enabled": true},
		},
		"model": map[string]any{"name": spec.Model},
		"telemetry": map[string]any{
			"enabled":      true,
			"otlpEndpoint": spec.OTLP,
			"otlpProtocol": "http",
			"logPrompts":   false,
		},
	}
	if err := writeJSON(filepath.Join(qwenHome, "settings.json"), cfg); err != nil {
		return invocation{}, err
	}
	// System settings override the user file. Point them at empty files so a
	// machine-wide settings.json cannot force gRPC or drop the model. Trust
	// the temp repo so folder trust does not block tools in a headless run.
	if err := writeJSON(filepath.Join(qwenHome, "system-settings.json"), map[string]any{}); err != nil {
		return invocation{}, err
	}
	if err := writeJSON(filepath.Join(qwenHome, "system-defaults.json"), map[string]any{}); err != nil {
		return invocation{}, err
	}
	if err := writeJSON(filepath.Join(qwenHome, "trustedFolders.json"), map[string]string{
		spec.Repo: "TRUST_FOLDER",
	}); err != nil {
		return invocation{}, err
	}
	return invocation{
		Argv: []string{"qwen", "--yolo", "-p", spec.Prompt},
		Env: map[string]string{
			"QWEN_HOME":                            qwenHome,
			"OPENAI_BASE_URL":                      spec.OpenAIBase,
			"OPENAI_MODEL":                         spec.Model,
			"QWEN_TELEMETRY_ENABLED":               "true",
			"QWEN_TELEMETRY_OTLP_ENDPOINT":         spec.OTLP,
			"QWEN_TELEMETRY_OTLP_PROTOCOL":         "http",
			"QWEN_TELEMETRY_LOG_PROMPTS":           "false",
			"QWEN_TELEMETRY_OTLP_TRACES_ENDPOINT":  signalURL(spec.OTLP, "traces"),
			"QWEN_TELEMETRY_OTLP_LOGS_ENDPOINT":    signalURL(spec.OTLP, "logs"),
			"QWEN_TELEMETRY_OTLP_METRICS_ENDPOINT": signalURL(spec.OTLP, "metrics"),
			"QWEN_CODE_SYSTEM_SETTINGS_PATH":       filepath.Join(qwenHome, "system-settings.json"),
			"QWEN_CODE_SYSTEM_DEFAULTS_PATH":       filepath.Join(qwenHome, "system-defaults.json"),
			"QWEN_CODE_SUPPRESS_YOLO_WARNING":      "1",
		},
		SecretEnv: []string{"OPENAI_API_KEY"},
	}, nil
}

// Grok CLI's custom model defaults to Chat Completions. env_key names the
// variable; api_key is not set, so the key is not written into config.toml.
type grokAgent struct{}

func (grokAgent) ID() string     { return "grok" }
func (grokAgent) Name() string   { return "Grok CLI" }
func (grokAgent) Binary() string { return "grok" }
func (grokAgent) Support() support {
	return support{OK: true, Protocol: protocolChat}
}
func (grokAgent) InstallCommands() [][]string { return nil }
func (grokAgent) InstallHint() string {
	return "Install the Grok CLI so grok is on PATH. This runner does not install it."
}
func (grokAgent) Prepare(_ context.Context, spec spec) (invocation, error) {
	if err := installSkills(spec,
		filepath.Join(spec.Home, ".grok", "skills", "sdf-decisions"),
		filepath.Join(spec.Repo, ".agents", "skills", "sdf-decisions"),
	); err != nil {
		return invocation{}, err
	}
	home := filepath.Join(spec.Home, ".grok")
	body := strings.Join([]string{
		"[model.recall]",
		"model = " + tomlString(spec.Model),
		"base_url = " + tomlString(spec.OpenAIBase),
		`name = "recall"`,
		`env_key = "SDF_RECALL_API_KEY"`,
		`api_backend = "chat_completions"`,
		"",
	}, "\n")
	if err := writePrivate(filepath.Join(home, "config.toml"), body); err != nil {
		return invocation{}, err
	}
	return invocation{
		Argv: []string{"grok", "--always-approve", "--model", "recall", "-p", spec.Prompt},
		Env: map[string]string{
			"GROK_HOME":                    home,
			"GROK_EXTERNAL_OTEL":           "1",
			"OTEL_METRICS_EXPORTER":        "otlp",
			"OTEL_LOGS_EXPORTER":           "otlp",
			"OTEL_EXPORTER_OTLP_PROTOCOL":  "http/protobuf",
			"OTEL_EXPORTER_OTLP_ENDPOINT":  spec.OTLP,
			"OTEL_LOG_USER_PROMPTS":        "0",
			"OTEL_LOG_ASSISTANT_RESPONSES": "0",
			"OTEL_LOG_TOOL_DETAILS":        "0",
			"OTEL_LOG_TOOL_CONTENT":        "0",
		},
	}, nil
}

// Copilot CLI takes an OpenAI-compatible provider. providers.json stores
// apiKeyCommand, not the key. $VAR in that file would be literal text.
type copilotAgent struct{}

func (copilotAgent) ID() string     { return "copilot" }
func (copilotAgent) Name() string   { return "Copilot CLI" }
func (copilotAgent) Binary() string { return "copilot" }
func (copilotAgent) Support() support {
	return support{OK: true, Protocol: protocolChat}
}
func (copilotAgent) InstallCommands() [][]string {
	return [][]string{{"npm", "install", "-g", "@github/copilot"}}
}
func (copilotAgent) InstallHint() string {
	return "Install with npm install -g @github/copilot, or pass --install."
}
func (copilotAgent) Prepare(_ context.Context, spec spec) (invocation, error) {
	if err := installSkills(spec,
		filepath.Join(spec.Home, ".copilot", "skills", "sdf-decisions"),
		filepath.Join(spec.Repo, ".github", "skills", "sdf-decisions"),
		filepath.Join(spec.Repo, ".agents", "skills", "sdf-decisions"),
	); err != nil {
		return invocation{}, err
	}
	cfg := map[string]any{
		"providers": []any{
			map[string]any{
				"name":          "tokenrouter",
				"type":          "openai",
				"baseUrl":       spec.OpenAIBase,
				"wireApi":       "completions",
				"apiKeyCommand": "printenv SDF_RECALL_API_KEY",
			},
		},
		"models": []any{
			map[string]any{
				"id":        "recall",
				"provider":  "tokenrouter",
				"wireModel": spec.Model,
			},
		},
	}
	if err := writeJSON(filepath.Join(spec.Home, ".copilot", "providers.json"), cfg); err != nil {
		return invocation{}, err
	}
	return invocation{
		Argv: []string{"copilot", "--allow-all-tools", "--model", "tokenrouter/recall", "-p", spec.Prompt},
		Env: map[string]string{
			"COPILOT_PROVIDER_BASE_URL":                  spec.OpenAIBase,
			"COPILOT_MODEL":                              "recall",
			"OTEL_EXPORTER_OTLP_ENDPOINT":                spec.OTLP,
			"OTEL_EXPORTER_OTLP_PROTOCOL":                "http/json",
			"OTEL_INSTRUMENTATION_GENAI_CAPTURE_CONTENT": "NO_CONTENT",
		},
		SecretEnv: []string{"COPILOT_PROVIDER_API_KEY"},
	}, nil
}

// Codex speaks the Responses API. TokenRouter returns HTTP 400 when that
// request includes tool_choice or reasoning. Leaving model_reasoning_effort
// unset does not stop Codex from sending those fields, and no provider flag
// omits tool_choice. The runner does not rewrite the Responses body.
type codexAgent struct{}

func (codexAgent) ID() string     { return "codex" }
func (codexAgent) Name() string   { return "Codex CLI" }
func (codexAgent) Binary() string { return "codex" }
func (codexAgent) Support() support {
	return support{
		OK: false,
		Reason: "Codex speaks the OpenAI Responses API. TokenRouter returns HTTP 400 when the request includes tool_choice or reasoning. " +
			"Leaving model_reasoning_effort unset does not stop Codex from sending those fields, and there is no provider flag that omits them.",
	}
}
func (codexAgent) InstallCommands() [][]string { return nil }
func (codexAgent) InstallHint() string         { return "" }
func (codexAgent) Prepare(context.Context, spec) (invocation, error) {
	return invocation{}, errUnsupported
}

// Claude Code speaks the Anthropic Messages API. ANTHROPIC_BASE_URL is the
// origin; Claude appends /v1/messages. The key is ANTHROPIC_AUTH_TOKEN.
type claudeAgent struct{}

func (claudeAgent) ID() string     { return "claude-code" }
func (claudeAgent) Name() string   { return "Claude Code" }
func (claudeAgent) Binary() string { return "claude" }
func (claudeAgent) Support() support {
	return support{OK: true, Protocol: protocolAnthropic}
}
func (claudeAgent) InstallCommands() [][]string { return nil }
func (claudeAgent) InstallHint() string {
	return "Install Claude Code so claude is on PATH. This runner does not install it."
}
func (claudeAgent) Prepare(_ context.Context, spec spec) (invocation, error) {
	if err := installSkills(spec,
		filepath.Join(spec.Home, ".claude", "skills", "sdf-decisions"),
		filepath.Join(spec.Repo, ".claude", "skills", "sdf-decisions"),
	); err != nil {
		return invocation{}, err
	}
	return invocation{
		Argv: []string{"claude", "--dangerously-skip-permissions", "--model", spec.Model, "-p", spec.Prompt},
		Env: map[string]string{
			"ANTHROPIC_BASE_URL":                  spec.AnthropicBase,
			"ANTHROPIC_API_KEY":                   "",
			"ANTHROPIC_MODEL":                     spec.Model,
			"ANTHROPIC_DEFAULT_SONNET_MODEL":      spec.Model,
			"ANTHROPIC_DEFAULT_OPUS_MODEL":        spec.Model,
			"ANTHROPIC_DEFAULT_HAIKU_MODEL":       spec.Model,
			"ANTHROPIC_DEFAULT_FABLE_MODEL":       spec.Model,
			"CLAUDE_CODE_SUBAGENT_MODEL":          spec.Model,
			"CLAUDE_CODE_ENABLE_TELEMETRY":        "1",
			"CLAUDE_CODE_ENHANCED_TELEMETRY_BETA": "1",
			"OTEL_TRACES_EXPORTER":                "otlp",
			"OTEL_METRICS_EXPORTER":               "otlp",
			"OTEL_LOGS_EXPORTER":                  "otlp",
			"OTEL_EXPORTER_OTLP_PROTOCOL":         "http/protobuf",
			"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT":  signalURL(spec.OTLP, "traces"),
			"OTEL_EXPORTER_OTLP_METRICS_ENDPOINT": signalURL(spec.OTLP, "metrics"),
			"OTEL_EXPORTER_OTLP_LOGS_ENDPOINT":    signalURL(spec.OTLP, "logs"),
		},
		SecretEnv: []string{"ANTHROPIC_AUTH_TOKEN"},
	}, nil
}

// Gemini CLI is unsupported. TokenRouter's generateContent paths do not
// succeed, and this runner does not retarget Gemini at Chat Completions.
type geminiAgent struct{}

func (geminiAgent) ID() string     { return "gemini" }
func (geminiAgent) Name() string   { return "Gemini CLI" }
func (geminiAgent) Binary() string { return "gemini" }
func (geminiAgent) Support() support {
	return support{
		OK: false,
		Reason: "Gemini CLI speaks the native Gemini generateContent protocol. " +
			"TokenRouter has no working generateContent endpoint (the v1beta path returns HTTP 500 and the v1 path returns HTTP 404). " +
			"This runner does not point Gemini CLI at the OpenAI Chat Completions endpoint.",
	}
}
func (geminiAgent) InstallCommands() [][]string { return nil }
func (geminiAgent) InstallHint() string         { return "" }
func (geminiAgent) Prepare(context.Context, spec) (invocation, error) {
	return invocation{}, errUnsupported
}

var errUnsupported = errors.New("agent is unsupported with this provider")
