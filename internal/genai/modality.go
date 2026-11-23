package genai

import "strings"

// usageModalities are the modality names accepted in
// gen_ai.usage.<modality>.* breakdown labels. v0.1.0 defines text, image, and
// audio; video is accepted ahead of the spec. Any other modality is not
// recognized, so its key is not treated as a breakdown.
var usageModalities = map[string]bool{
	ModalityText:  true,
	ModalityImage: true,
	ModalityAudio: true,
	ModalityVideo: true,
}

// modalityTotals are the totals a per-modality label can break down. v0.1.0
// defines input_tokens, output_tokens, and cache_read.input_tokens per
// modality. cache_write.input_tokens per modality is not in the v0.1.0
// registry; it is accepted for forward compatibility only.
var modalityTotals = map[string]bool{
	UsageLabelInputTokens:           true,
	UsageLabelOutputTokens:          true,
	UsageLabelCacheReadInputTokens:  true,
	UsageLabelCacheWriteInputTokens: true,
}

// SplitModalityUsage splits a per-modality usage label (a gen_ai.usage.* key
// without UsagePrefix) such as text.input_tokens into its modality and the
// total label it is part of. ok is false for any other label.
func SplitModalityUsage(label string) (modality, total string, ok bool) {
	modality, total, ok = strings.Cut(label, ".")
	if !ok || !usageModalities[modality] || !modalityTotals[total] {
		return "", "", false
	}
	return modality, total, true
}

// IsModalityUsage reports whether label (a gen_ai.usage.* key without the
// prefix) is a per-modality breakdown such as text.input_tokens. Such a count
// is already part of the matching total (input_tokens, output_tokens, or a
// cache total), so it is informational and never priced again.
func IsModalityUsage(label string) bool {
	_, _, ok := SplitModalityUsage(label)
	return ok
}
