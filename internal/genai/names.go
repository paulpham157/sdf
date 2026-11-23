package genai

// Attribute keys defined by semantic-conventions-genai v0.1.0.
const (
	AttrAgentID        = "gen_ai.agent.id"
	AttrConversationID = "gen_ai.conversation.id"
	AttrOperationName  = "gen_ai.operation.name"
	AttrRequestModel   = "gen_ai.request.model"
	AttrResponseModel  = "gen_ai.response.model"
	AttrToolName       = "gen_ai.tool.name"
	AttrToolCallID     = "gen_ai.tool.call.id"
)

// UsagePrefix starts every gen_ai.usage.* token attribute. SDF strips it
// and works with the remaining label, such as input_tokens.
const UsagePrefix = "gen_ai.usage."

// gen_ai.usage.* labels (the key without UsagePrefix) defined by v0.1.0.
const (
	UsageLabelInputTokens           = "input_tokens"
	UsageLabelOutputTokens          = "output_tokens"
	UsageLabelCacheReadInputTokens  = "cache_read.input_tokens"
	UsageLabelCacheWriteInputTokens = "cache_write.input_tokens"
	UsageLabelReasoningOutputTokens = "reasoning.output_tokens"
)

// Full gen_ai.usage.* attribute keys defined by v0.1.0.
const (
	AttrUsageInputTokens           = UsagePrefix + UsageLabelInputTokens
	AttrUsageOutputTokens          = UsagePrefix + UsageLabelOutputTokens
	AttrUsageCacheReadInputTokens  = UsagePrefix + UsageLabelCacheReadInputTokens
	AttrUsageCacheWriteInputTokens = UsagePrefix + UsageLabelCacheWriteInputTokens
	AttrUsageReasoningOutputTokens = UsagePrefix + UsageLabelReasoningOutputTokens
)

// gen_ai.operation.name values. SDF also matches them as the first word of a
// span name ("chat <model>", "invoke_agent <name>", "execute_tool <tool>").
const (
	OperationChat           = "chat"
	OperationInvokeAgent    = "invoke_agent"
	OperationInvokeWorkflow = "invoke_workflow"
	OperationExecuteTool    = "execute_tool"
)

// MetricInferenceUsagePrefix starts the v0.1.0 per-token-type usage counters,
// gen_ai.client.inference.usage.<label> (for example
// gen_ai.client.inference.usage.input_tokens).
const MetricInferenceUsagePrefix = "gen_ai.client.inference.usage."

// gen_ai.token.modality values defined by v0.1.0. They also prefix the
// per-modality usage labels, such as text.input_tokens.
const (
	ModalityText  = "text"
	ModalityImage = "image"
	ModalityAudio = "audio"
)
