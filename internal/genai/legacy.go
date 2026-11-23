package genai

// Names that semantic-conventions-genai v0.1.0 does not define but that SDF
// must still read because agents in the field send them (and, for
// AttrSystem, that the Pi git hook emits to match Pi's own spans). Each one
// is listed with its reason in notInRegistry in registry_test.go.

// AttrSystem is gen_ai.system, replaced by gen_ai.provider.name before
// v0.1.0. Pi still sets it.
const AttrSystem = "gen_ai.system"

// AttrTokenType is gen_ai.token.type, removed in v0.1.0 together with
// MetricClientTokenUsage.
const AttrTokenType = "gen_ai.token.type"

// MetricClientTokenUsage is gen_ai.client.token.usage, removed in v0.1.0 and
// replaced by gen_ai.client.inference.operation.{input,output}_tokens and the
// gen_ai.client.inference.usage.* counters. Pi still emits it.
const MetricClientTokenUsage = "gen_ai.client.token.usage"

// Legacy gen_ai.usage.* labels.
const (
	// UsageLabelCacheCreationInputTokens was renamed to
	// UsageLabelCacheWriteInputTokens in v0.1.0. OpenCode and Qwen Code
	// still send it.
	UsageLabelCacheCreationInputTokens = "cache_creation.input_tokens"
	// Underscore spellings some OpenCode builds send. They were never in
	// the semantic conventions.
	UsageLabelCacheCreationInputTokensUnderscore = "cache_creation_input_tokens"
	UsageLabelCacheReadInputTokensUnderscore     = "cache_read_input_tokens"
	UsageLabelCacheWriteInputTokensUnderscore    = "cache_write_input_tokens"
	// UsageLabelReasoningOutputTokensUnderscore is the underscore spelling
	// Copilot CLI builds before 1.0.64 sent.
	UsageLabelReasoningOutputTokensUnderscore = "reasoning_output_tokens"
	// UsageLabelReasoningTokens is the pre-v0.1.0 spelling of
	// UsageLabelReasoningOutputTokens.
	UsageLabelReasoningTokens = "reasoning_tokens"
)

// AttrUsageCacheCreationInputTokens is the full pre-v0.1.0 key for
// UsageLabelCacheCreationInputTokens.
const AttrUsageCacheCreationInputTokens = UsagePrefix + UsageLabelCacheCreationInputTokens

// ModalityVideo is not a gen_ai.token.modality member in v0.1.0. Per-modality
// usage labels with it are accepted ahead of the spec.
const ModalityVideo = "video"
