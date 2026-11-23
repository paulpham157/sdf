package pricing

import (
	"fmt"
	"math/big"
	"sort"
	"strings"
)

// pricesFor returns the per-token rates to use for one token map.
// singleRequest is true when tokens are one request, so a count at or
// above min_prompt_tokens may use the override rates. A session aggregate
// may only use the base rates when the prompt total is below every
// threshold; at or above the lowest threshold the cost stays unknown.
func pricesFor(model *Model, source string, tokens map[string]float64, singleRequest bool) (map[string]string, string, string) {
	if !model.HasOverrides && len(model.Overrides) == 0 {
		return model.Prices, "", ""
	}
	if len(model.Overrides) == 0 {
		return nil, "", "model " + model.ID + " has conditional OpenRouter price overrides, and the conditions were not loaded, so no rate was applied"
	}
	var unknown []string
	seenUnknown := map[string]bool{}
	byThreshold := map[int64]map[string]string{}
	var thresholds []int64
	for _, ov := range model.Overrides {
		for _, key := range ov.UnknownConditions {
			if !seenUnknown[key] {
				unknown = append(unknown, key)
				seenUnknown[key] = true
			}
		}
		if ov.MinPromptTokens == nil {
			if len(ov.UnknownConditions) == 0 {
				return nil, "", "model " + model.ID + " has a pricing.overrides entry without a min_prompt_tokens condition, so no rate was applied"
			}
			continue
		}
		if prev, ok := byThreshold[*ov.MinPromptTokens]; ok {
			if !sameRates(prev, ov.Prices) {
				return nil, "", fmt.Sprintf("model %s has two pricing.overrides entries for min_prompt_tokens %d at different rates, so no rate was applied", model.ID, *ov.MinPromptTokens)
			}
			continue
		}
		byThreshold[*ov.MinPromptTokens] = ov.Prices
		thresholds = append(thresholds, *ov.MinPromptTokens)
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, "", "model " + model.ID + " has conditional OpenRouter price overrides with unrecognized condition keys (" + strings.Join(unknown, ", ") + "), so no rate was applied"
	}
	if len(thresholds) == 0 {
		return nil, "", "model " + model.ID + " has conditional OpenRouter price overrides, and no min_prompt_tokens threshold could be used, so no rate was applied"
	}
	sort.Slice(thresholds, func(i, j int) bool { return thresholds[i] < thresholds[j] })
	prompt, reason := promptTokens(source, tokens)
	if reason != "" {
		return nil, "", reason
	}
	lowest := thresholds[0]
	if !singleRequest && prompt.Cmp(big.NewRat(lowest, 1)) >= 0 {
		return nil, "", fmt.Sprintf("model %s has conditional OpenRouter price overrides; the token total is not one request and the prompt token count is at or above min_prompt_tokens %d, so a request could have crossed the threshold and the base rate was not applied", model.ID, lowest)
	}
	var chosen int64
	found := false
	for _, threshold := range thresholds {
		if prompt.Cmp(big.NewRat(threshold, 1)) >= 0 {
			chosen = threshold
			found = true
		}
	}
	if !found {
		return model.Prices, fmt.Sprintf("base rates applied because the prompt token count is below min_prompt_tokens %d", lowest), ""
	}
	prices := make(map[string]string, len(model.Prices)+len(byThreshold[chosen]))
	for key, value := range model.Prices {
		prices[key] = value
	}
	for key, value := range byThreshold[chosen] {
		prices[key] = value
	}
	return prices, fmt.Sprintf("override rates applied for min_prompt_tokens %d", chosen), ""
}

func sameRates(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		if b[key] != value {
			return false
		}
	}
	return true
}

// promptTokens is the request (or aggregate) input size compared to
// min_prompt_tokens. For overlap sources, that is the input total, which
// already includes cache read and cache write. For disjoint sources, it is
// input plus cache read plus cache write.
func promptTokens(source string, tokens map[string]float64) (*big.Rat, string) {
	values, overlap, reason := canonicalPromptValues(source, tokens)
	if reason != "" {
		return nil, reason
	}
	if overlap {
		input := values["input"]
		if input == nil {
			return nil, "no input token count, so the override threshold was not compared"
		}
		return new(big.Rat).Set(input), ""
	}
	prompt := new(big.Rat)
	found := false
	for _, key := range []string{"input", "cache_read", "cache_write"} {
		if value := values[key]; value != nil {
			prompt.Add(prompt, value)
			found = true
		}
	}
	if !found {
		return nil, "no prompt token count, so the override threshold was not compared"
	}
	return prompt, ""
}

// canonicalPromptValues maps raw token keys the same way bucketsFor does,
// but keeps the pre-overlap totals so the prompt size is not reduced by
// cache or reasoning.
func canonicalPromptValues(source string, tokens map[string]float64) (map[string]*big.Rat, bool, string) {
	names, ignore, overlap, reason := promptRule(source)
	if reason != "" {
		return nil, false, reason
	}
	values := map[string]*big.Rat{}
	for key, value := range tokens {
		if key == ignore {
			continue
		}
		label, ok := names[key]
		if !ok {
			return nil, false, fmt.Sprintf("token key %q is not part of this pricing rule", key)
		}
		amount, err := parseTokens(value)
		if err != nil {
			return nil, false, err.Error()
		}
		if prev, ok := values[label]; ok {
			if prev.Cmp(amount) != 0 {
				return nil, false, fmt.Sprintf("token key %q would price %s twice", key, label)
			}
			continue
		}
		values[label] = amount
	}
	return values, overlap, ""
}

func promptRule(source string) (names map[string]string, ignore string, overlap bool, reason string) {
	switch source {
	case "metrics claude_code.token.usage":
		return map[string]string{
			"input": "input", "output": "output",
			"cacheRead": "cache_read", "cacheCreation": "cache_write",
		}, "", false, ""
	case "logs claude_code.api_request":
		return map[string]string{
			"input_tokens": "input", "output_tokens": "output",
			"cache_read_tokens": "cache_read", "cache_creation_tokens": "cache_write",
		}, "", false, ""
	case "metrics codex.turn.token_usage":
		return map[string]string{
			"input": "input", "cached_input": "cache_read", "cache_write_input": "cache_write",
			"output": "output", "reasoning_output": "reasoning",
		}, "total", true, ""
	case "logs codex.sse_event response.completed":
		return map[string]string{
			"input_token_count": "input", "cached_token_count": "cache_read",
			"cache_write_token_count": "cache_write", "output_token_count": "output",
			"reasoning_token_count": "reasoning",
		}, "", true, ""
	case "traces SessionStep.attempt":
		return map[string]string{
			"input_tokens": "input", "output_tokens": "output",
			"cache_read.input_tokens": "cache_read", "cache_read_input_tokens": "cache_read",
			"cache_write.input_tokens": "cache_write", "cache_write_input_tokens": "cache_write",
			"reasoning_tokens": "reasoning",
		}, "", true, ""
	case "traces chat":
		return map[string]string{"input_tokens": "input", "output_tokens": "output"}, "", false, ""
	case "metrics gen_ai.client.token.usage":
		return map[string]string{"input": "input", "output": "output"}, "", false, ""
	case "metrics grok_code.token.usage":
		return map[string]string{
			"input": "input", "output": "output",
			"cache_read": "cache_read", "cache_write": "cache_write", "reasoning": "reasoning",
		}, "", true, ""
	case "logs grok_code.api_request":
		return map[string]string{
			"input_tokens": "input", "output_tokens": "output",
			"cache_read_tokens": "cache_read", "cache_creation_tokens": "cache_write",
			"reasoning_tokens": "reasoning",
		}, "", true, ""
	default:
		if source == "" {
			return nil, "", false, "token source is missing, so the counts were not priced"
		}
		return nil, "", false, fmt.Sprintf("no pricing rule for token source %q", source)
	}
}

func joinNotes(existing string, notes []string) string {
	out := existing
	for _, note := range notes {
		if note == "" {
			continue
		}
		if out != "" {
			out = out + "; " + note
		} else {
			out = note
		}
	}
	return out
}

func uniqueNotes(notes []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, note := range notes {
		if note == "" || seen[note] {
			continue
		}
		seen[note] = true
		out = append(out, note)
	}
	return out
}
