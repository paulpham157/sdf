package live

import (
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultModel       = "deepseek/deepseek-v4.1-flash-1017-free"
	defaultOpenAI      = "https://api.tokenrouter.com/v1"
	defaultAnthropic   = "https://api.tokenrouter.com"
	defaultK           = 5
	defaultMaxTurns    = 12
	defaultMaxTokens   = 20000
	defaultRetries     = 4
	defaultHTTPTimeout = 90 * time.Second
	defaultRunTimeout  = 3 * time.Minute

	schemaLive = "sdf.decision.recall.live/v1"
)

// Options is one live measurement. The zero Key skips the run. Bases are
// the provider URLs the local proxy forwards to, not URLs written into a
// committed file.
type Options struct {
	Agents []string
	// FixtureNames limits the scenarios. Empty runs every embedded fixture.
	// The sdf-recall command does not set this; it always runs the full set.
	FixtureNames  []string
	K             int
	Model         string
	OpenAIBase    string
	AnthropicBase string
	Key           string
	Install       bool
	MaxTurns      int
	MaxTokens     int
	Retries       int
	HTTPTimeout   time.Duration
	RunTimeout    time.Duration
	SkillDir      string
	SDFBin        string
	// LookPath resolves a CLI name. Nil uses exec.LookPath.
	LookPath func(string) (string, error)
	// ExtraEnv is added to the agent process. Values that contain Key are
	// dropped. It cannot override the key variables.
	ExtraEnv map[string]string
	// Backoff is the wait before retry attempt n, starting at 0. Nil uses
	// exponential backoff from 200ms, capped at 5s.
	Backoff func(attempt int) time.Duration
	// HTTPClient is the proxy's upstream client. Nil uses a client that
	// does not follow redirects.
	HTTPClient *http.Client

	// Local proxy bases, set by Run after the proxy listens. Agent CLIs
	// are pointed here. The report keeps the upstream bases.
	proxyOpenAI    string
	proxyAnthropic string
}

// ConfigFromEnv reads the measurement settings. An empty key means skip.
// SDF_RECALL_OPENAI_BASE_URL wins over SDF_RECALL_BASE_URL. The Anthropic
// base does not fall back to the OpenAI base, because that URL already
// ends in /v1 and Claude appends /v1/messages itself.
func ConfigFromEnv() Options {
	o := Options{
		Key:           firstNonEmpty(os.Getenv("SDF_RECALL_API_KEY"), os.Getenv("TOKENROUTER_API_KEY")),
		Model:         os.Getenv("SDF_RECALL_MODEL"),
		OpenAIBase:    firstNonEmpty(os.Getenv("SDF_RECALL_OPENAI_BASE_URL"), os.Getenv("SDF_RECALL_BASE_URL")),
		AnthropicBase: os.Getenv("SDF_RECALL_ANTHROPIC_BASE_URL"),
		SDFBin:        os.Getenv("SDF_BIN"),
		SkillDir:      os.Getenv("SDF_RECALL_SKILL"),
	}
	if n, err := strconv.Atoi(os.Getenv("SDF_RECALL_K")); err == nil && n > 0 {
		o.K = n
	}
	if n, err := strconv.Atoi(os.Getenv("SDF_RECALL_MAX_TURNS")); err == nil && n > 0 {
		o.MaxTurns = n
	}
	if n, err := strconv.Atoi(os.Getenv("SDF_RECALL_MAX_TOKENS")); err == nil && n > 0 {
		o.MaxTokens = n
	}
	if n, err := strconv.Atoi(os.Getenv("SDF_RECALL_RETRIES")); err == nil && n > 0 {
		o.Retries = n
	}
	if d, err := time.ParseDuration(os.Getenv("SDF_RECALL_HTTP_TIMEOUT")); err == nil && d > 0 {
		o.HTTPTimeout = d
	}
	if d, err := time.ParseDuration(os.Getenv("SDF_RECALL_RUN_TIMEOUT")); err == nil && d > 0 {
		o.RunTimeout = d
	}
	if os.Getenv("SDF_RECALL_NO_INSTALL") == "1" {
		o.Install = false
	}
	return o
}

func (o Options) withDefaults() Options {
	if o.Model == "" {
		o.Model = defaultModel
	}
	if o.OpenAIBase == "" {
		o.OpenAIBase = defaultOpenAI
	}
	if o.AnthropicBase == "" {
		o.AnthropicBase = defaultAnthropic
	}
	if o.K == 0 {
		o.K = defaultK
	}
	if o.MaxTurns == 0 {
		o.MaxTurns = defaultMaxTurns
	}
	if o.MaxTokens == 0 {
		o.MaxTokens = defaultMaxTokens
	}
	if o.Retries == 0 {
		o.Retries = defaultRetries
	}
	if o.HTTPTimeout == 0 {
		o.HTTPTimeout = defaultHTTPTimeout
	}
	if o.RunTimeout == 0 {
		o.RunTimeout = defaultRunTimeout
	}
	if o.Backoff == nil {
		o.Backoff = defaultBackoff
	}
	o.OpenAIBase = strings.TrimRight(o.OpenAIBase, "/")
	o.AnthropicBase = strings.TrimRight(o.AnthropicBase, "/")
	o.Key = strings.TrimSpace(o.Key)
	return o
}

func defaultBackoff(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	if attempt > 5 {
		attempt = 5
	}
	d := 200 * time.Millisecond << attempt
	if d > 5*time.Second {
		return 5 * time.Second
	}
	return d
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
