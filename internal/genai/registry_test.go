package genai

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"go/ast"
	"go/constant"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const vendorDir = "testdata/semconv-genai-v0.1.0"

// vendoredSHA256 pins the vendored release assets. The values are the
// sha256 digests GitHub reports for the v0.1.0 release assets. A pin move
// changes them together with SHA256SUMS and the files themselves.
var vendoredSHA256 = map[string]string{
	"manifest-dev.yaml": "36615b10ad45605b8c32df05e4e6fc4fed7caa4e4feccc2f247eaf4c35930e15",
	"registry-dev.yaml": "2fb23ed0d6bdee11ddfe13a2364f249f751e2c384e3ce2bb33bb066a568fffe9",
}

// notInRegistry is the explicit list of constants that are deliberately not
// in the v0.1.0 registry. Each one is a name SDF still has to read (or, for
// the git hook, emit) because agents in the field send it. The test fails if
// one of them shows up in the registry, so the list cannot go stale silently.
var notInRegistry = map[string]string{
	"AttrSystem":                                   "gen_ai.system was replaced by gen_ai.provider.name before v0.1.0; Pi still sends it and the git hook emits it for Pi",
	"AttrTokenType":                                "gen_ai.token.type was removed with gen_ai.client.token.usage; Pi's token metric still carries it",
	"MetricClientTokenUsage":                       "gen_ai.client.token.usage was removed in v0.1.0 (replaced by gen_ai.client.inference.operation.{input,output}_tokens); Pi still emits it",
	"UsageLabelCacheCreationInputTokens":           "renamed to cache_write.input_tokens in v0.1.0; OpenCode and Qwen Code still send it",
	"AttrUsageCacheCreationInputTokens":            "renamed to gen_ai.usage.cache_write.input_tokens in v0.1.0; OpenCode and Qwen Code still send it",
	"UsageLabelCacheCreationInputTokensUnderscore": "underscore spelling of cache_creation.input_tokens sent by some OpenCode builds; never in semconv",
	"UsageLabelCacheReadInputTokensUnderscore":     "underscore spelling of cache_read.input_tokens sent by some OpenCode builds; never in semconv",
	"UsageLabelCacheWriteInputTokensUnderscore":    "underscore spelling of cache_write.input_tokens; never in semconv",
	"UsageLabelReasoningTokens":                    "pre-v0.1.0 spelling of reasoning.output_tokens",
	"UsageLabelReasoningOutputTokensUnderscore":    "underscore spelling of reasoning.output_tokens sent by Copilot CLI before 1.0.64; never in semconv",
	"ModalityVideo":                                "not a gen_ai.token.modality member in v0.1.0; accepted ahead of the spec",
}

// pinConstants are checked by TestPin rather than against registry names.
var pinConstants = map[string]bool{
	"SemconvVersion":     true,
	"SemconvCommit":      true,
	"SchemaURL":          true,
	"CoreSemconvVersion": true,
	"CoreSchemaURL":      true,
	"WeaverVersion":      true,
}

type registry struct {
	schemaURL    string
	dependencies []string
	attributes   map[string]bool
	members      map[string]map[string]bool // attribute key -> enum member values
	metrics      map[string]bool
	events       map[string]bool
	spans        map[string]bool
}

func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && (s[0] == '\'' || s[0] == '"') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	return s
}

// loadRegistry reads the parts of the resolved registry the tests need. The
// file is Weaver output with a fixed layout, so a line scanner is enough and
// SDF keeps its dependency list free of a YAML library.
func loadRegistry(t *testing.T) registry {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(vendorDir, "registry-dev.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	r := registry{
		attributes: map[string]bool{},
		members:    map[string]map[string]bool{},
		metrics:    map[string]bool{},
		events:     map[string]bool{},
		spans:      map[string]bool{},
	}
	section, sub, key := "", "", ""
	sc := bufio.NewScanner(bytes.NewReader(body))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if line != "" && line[0] != ' ' && line[0] != '-' {
			name, value, _ := strings.Cut(line, ":")
			section, sub, key = name, "", ""
			if name == "schema_url" {
				r.schemaURL = unquote(value)
			}
			continue
		}
		switch section {
		case "attribute_catalog":
			if rest, ok := strings.CutPrefix(line, "- key: "); ok {
				key = unquote(rest)
				r.attributes[key] = true
				if r.members[key] == nil {
					r.members[key] = map[string]bool{}
				}
			} else if rest, ok := strings.CutPrefix(line, "      value: "); ok && key != "" {
				r.members[key][unquote(rest)] = true
			}
		case "registry":
			if strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "   ") && !strings.HasPrefix(line, "  -") {
				sub, _, _ = strings.Cut(strings.TrimSpace(line), ":")
				continue
			}
			switch sub {
			case "metrics":
				if rest, ok := strings.CutPrefix(line, "  - name: "); ok {
					r.metrics[unquote(rest)] = true
				}
			case "events":
				if rest, ok := strings.CutPrefix(line, "  - name: "); ok {
					r.events[unquote(rest)] = true
				}
			case "spans":
				if rest, ok := strings.CutPrefix(line, "  - type: "); ok {
					r.spans[unquote(rest)] = true
				}
			}
		case "dependencies":
			if rest, ok := strings.CutPrefix(line, "- "); ok {
				r.dependencies = append(r.dependencies, unquote(rest))
			}
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	// Guard the scanner itself: these are well-known v0.1.0 names.
	for _, want := range []string{"gen_ai.request.model", "gen_ai.usage.input_tokens", "gen_ai.token.modality"} {
		if !r.attributes[want] {
			t.Fatalf("registry scan missed attribute %s", want)
		}
	}
	if !r.metrics["gen_ai.client.inference.operation.input_tokens"] || !r.spans["gen_ai.client.inference"] || !r.events["gen_ai.client.inference.operation.details"] {
		t.Fatal("registry scan missed metrics, spans, or events")
	}
	if !r.members["gen_ai.operation.name"]["chat"] {
		t.Fatal("registry scan missed gen_ai.operation.name members")
	}
	return r
}

// packageConstants type-checks the non-test files of this package and
// returns every exported string constant with its value. Reading the source
// means a new constant is covered by the conformance test without anyone
// remembering to list it.
func packageConstants(t *testing.T) map[string]string {
	t.Helper()
	fset := token.NewFileSet()
	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var files []*ast.File
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
	}
	if len(files) == 0 {
		t.Fatal("package genai has no non-test Go files; the pinned names do not exist yet")
	}
	conf := types.Config{Importer: importer.Default()}
	checked, err := conf.Check("github.com/paulpham157/sdf/internal/genai", fset, files, nil)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	scope := checked.Scope()
	for _, name := range scope.Names() {
		c, ok := scope.Lookup(name).(*types.Const)
		if !ok || !c.Exported() || c.Val().Kind() != constant.String {
			continue
		}
		out[name] = constant.StringVal(c.Val())
	}
	if len(out) == 0 {
		t.Fatal("package genai exports no string constants")
	}
	return out
}

func TestVendoredRegistryChecksum(t *testing.T) {
	sums, err := os.ReadFile(filepath.Join(vendorDir, "SHA256SUMS"))
	if err != nil {
		t.Fatal(err)
	}
	listed := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(sums)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			t.Fatalf("SHA256SUMS line %q", line)
		}
		listed[fields[1]] = fields[0]
	}
	if len(listed) != len(vendoredSHA256) {
		t.Fatalf("SHA256SUMS lists %d files, want %d", len(listed), len(vendoredSHA256))
	}
	for name, want := range vendoredSHA256 {
		if listed[name] != want {
			t.Errorf("SHA256SUMS %s = %q, want %q", name, listed[name], want)
		}
		body, err := os.ReadFile(filepath.Join(vendorDir, name))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(body)
		if got := hex.EncodeToString(sum[:]); got != want {
			t.Errorf("%s sha256 = %s, want %s", name, got, want)
		}
	}
}

func TestPin(t *testing.T) {
	r := loadRegistry(t)
	if SchemaURL != r.schemaURL {
		t.Errorf("SchemaURL = %q, registry schema_url = %q", SchemaURL, r.schemaURL)
	}
	if !strings.HasSuffix(SchemaURL, "/"+SemconvVersion+"-dev") {
		t.Errorf("SchemaURL %q does not end in /%s-dev", SchemaURL, SemconvVersion)
	}
	if len(r.dependencies) != 1 || r.dependencies[0] != CoreSchemaURL {
		t.Errorf("registry dependencies = %q, want [%s]", r.dependencies, CoreSchemaURL)
	}
	if CoreSchemaURL != "https://opentelemetry.io/schemas/"+CoreSemconvVersion {
		t.Errorf("CoreSchemaURL %q does not match CoreSemconvVersion %q", CoreSchemaURL, CoreSemconvVersion)
	}
	manifest, err := os.ReadFile(filepath.Join(vendorDir, "manifest-dev.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"schema_url: " + SchemaURL + "\n",
		"- schema_url: " + CoreSchemaURL + "\n",
		"releases/download/v" + SemconvVersion + "/registry-dev.yaml\n",
	} {
		if !strings.Contains(string(manifest), want) {
			t.Errorf("manifest-dev.yaml lacks %q", want)
		}
	}
	// The commit and the Weaver version are not in the release assets; they
	// are recorded from the tag (git rev-parse v0.1.0, versions.env).
	if len(SemconvCommit) != 40 || strings.Trim(SemconvCommit, "0123456789abcdef") != "" {
		t.Errorf("SemconvCommit %q is not a full commit SHA", SemconvCommit)
	}
	if !strings.HasPrefix(WeaverVersion, "v") {
		t.Errorf("WeaverVersion %q", WeaverVersion)
	}
}

// TestConstantsExistInRegistry asserts that every exported string constant in
// the package is a name defined by the pinned v0.1.0 registry, unless it is on
// the documented notInRegistry list. Constants are classified by prefix:
//
//	Attr*        attribute key
//	UsageLabel*  gen_ai.usage.<label> attribute key
//	UsagePrefix  prefix of at least one attribute key
//	Metric*      metric name (Metric*Prefix: prefix of at least one metric)
//	Operation*   member of gen_ai.operation.name (also SDF's span-name head)
//	Modality*    member of gen_ai.token.modality
func TestConstantsExistInRegistry(t *testing.T) {
	r := loadRegistry(t)
	consts := packageConstants(t)
	var names []string
	for name := range consts {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		value := consts[name]
		if pinConstants[name] {
			continue
		}
		var found bool
		switch {
		case name == "UsagePrefix":
			for key := range r.attributes {
				found = found || strings.HasPrefix(key, value)
			}
		case strings.HasPrefix(name, "Metric") && strings.HasSuffix(name, "Prefix"):
			for metric := range r.metrics {
				found = found || strings.HasPrefix(metric, value)
			}
		case strings.HasPrefix(name, "UsageLabel"):
			found = r.attributes[UsagePrefix+value]
		case strings.HasPrefix(name, "Attr"):
			found = r.attributes[value]
		case strings.HasPrefix(name, "Metric"):
			found = r.metrics[value]
		case strings.HasPrefix(name, "Operation"):
			found = r.members["gen_ai.operation.name"][value]
		case strings.HasPrefix(name, "Modality"):
			found = r.members["gen_ai.token.modality"][value]
		default:
			t.Errorf("%s = %q: no classification rule; name it Attr*, UsageLabel*, Metric*, Operation*, or Modality*, or add a rule", name, value)
			continue
		}
		reason, legacy := notInRegistry[name]
		switch {
		case legacy && found:
			t.Errorf("%s = %q is listed as not in the registry (%s), but v0.1.0 defines it; drop it from notInRegistry", name, value, reason)
		case !legacy && !found:
			t.Errorf("%s = %q is not defined by semantic-conventions-genai v%s; fix the name or add it to notInRegistry with a reason", name, value, SemconvVersion)
		}
	}
	for name := range notInRegistry {
		if _, ok := consts[name]; !ok {
			t.Errorf("notInRegistry lists %s, which the package does not export", name)
		}
	}
}

// Spot-check the values SDF depends on, so a rename of a constant's value is
// caught even if the new value also happens to be in the registry.
func TestWellKnownValues(t *testing.T) {
	for got, want := range map[string]string{
		AttrConversationID:                "gen_ai.conversation.id",
		AttrOperationName:                 "gen_ai.operation.name",
		AttrRequestModel:                  "gen_ai.request.model",
		AttrResponseModel:                 "gen_ai.response.model",
		AttrToolName:                      "gen_ai.tool.name",
		AttrToolCallID:                    "gen_ai.tool.call.id",
		AttrUsageInputTokens:              "gen_ai.usage.input_tokens",
		AttrUsageOutputTokens:             "gen_ai.usage.output_tokens",
		AttrUsageCacheReadInputTokens:     "gen_ai.usage.cache_read.input_tokens",
		AttrUsageCacheCreationInputTokens: "gen_ai.usage.cache_creation.input_tokens",
		AttrSystem:                        "gen_ai.system",
		AttrTokenType:                     "gen_ai.token.type",
		MetricClientTokenUsage:            "gen_ai.client.token.usage",
		OperationChat:                     "chat",
		OperationInvokeAgent:              "invoke_agent",
		OperationExecuteTool:              "execute_tool",
		OperationInvokeWorkflow:           "invoke_workflow",
		AttrAgentID:                       "gen_ai.agent.id",
		MetricInferenceUsagePrefix:        "gen_ai.client.inference.usage.",
	} {
		if got != want {
			t.Errorf("got %q want %q", got, want)
		}
	}
}
