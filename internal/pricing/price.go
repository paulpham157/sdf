package pricing

import (
	"fmt"
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"
)

// Usage is the token total a summary already reduced.
// Source is the summary's tokens_source string. The counts must already
// follow that agent's de-dup rule (Codex and OpenCode take a max for the
// prefix; Pi sums per-turn input and output and drops cache and reasoning).
type Usage struct {
	Models []string
	Tokens map[string]float64
	Source string
}

// Cost is the computed price for one session.
// USD is omitted when Status is "unknown", so a missing price is not zero.
type Cost struct {
	Status      string      `json:"status"`
	USD         string      `json:"usd,omitempty"`
	Reason      string      `json:"reason,omitempty"`
	Model       string      `json:"model,omitempty"`
	CatalogID   string      `json:"catalog_id,omitempty"`
	MatchedBy   string      `json:"matched_by,omitempty"`
	PriceSource string      `json:"price_source,omitempty"`
	PriceDate   string      `json:"price_date,omitempty"`
	PriceTable  string      `json:"price_table,omitempty"`
	Method      string      `json:"method,omitempty"`
	Note        string      `json:"note,omitempty"`
	Components  []Component `json:"components,omitempty"`
}

// Component is one priced bucket after the agent's overlap rule.
type Component struct {
	Name        string `json:"name"`
	Tokens      string `json:"tokens"`
	USDPerToken string `json:"usd_per_token"`
	USD         string `json:"usd"`
}

type bucket struct {
	name   string
	tokens *big.Rat
	// priceKey is the OpenRouter field for this bucket.
	priceKey string
	// fallbackKey is used when priceKey is absent. Reasoning tokens are part
	// of output, so a missing internal_reasoning rate uses completion.
	fallbackKey string
	// conflictKey is a second published rate for the same undifferentiated
	// count (cache write, 5-minute versus 1-hour). If both exist and differ,
	// the cost is unknown.
	conflictKey string
}

// Price multiplies usage by the catalog. It never returns a zero cost for
// an unknown model or a missing component price.
func Price(cat *Catalog, usage Usage) Cost {
	if cat == nil {
		return Cost{Status: "unknown", Reason: "no price catalog"}
	}
	out := Cost{
		PriceSource: cat.Source,
		PriceDate:   cat.RetrievedAtRaw,
		PriceTable:  cat.Table,
	}
	if cat.Warning != "" {
		out.Note = cat.Warning
	}
	if len(usage.Tokens) == 0 {
		out.Status = "unknown"
		out.Reason = "no token counts"
		return out
	}
	for key, value := range usage.Tokens {
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			out.Status = "unknown"
			out.Reason = fmt.Sprintf("token count %s is not a non-negative number", key)
			return out
		}
	}
	models := uniqueModels(usage.Models)
	if len(models) == 0 {
		out.Status = "unknown"
		out.Reason = "no model id on the session"
		return out
	}
	var (
		match Match
		by    string
	)
	for _, reported := range models {
		one, reason := cat.Resolve(reported)
		if reason != "" {
			out.Status = "unknown"
			out.Reason = reason
			out.Model = reported
			return out
		}
		if match.Model == nil {
			match = one
			by = one.By
			continue
		}
		if match.Model.ID != one.Model.ID {
			out.Status = "unknown"
			out.Reason = "session models price as different catalog ids: " + match.Model.ID + ", " + one.Model.ID
			out.Model = strings.Join(models, ", ")
			return out
		}
		if by != one.By {
			by = "mixed"
		}
	}
	out.Model = strings.Join(models, ", ")
	out.CatalogID = match.Model.ID
	out.MatchedBy = by
	if match.Model.HasOverrides {
		out.Status = "unknown"
		out.Reason = "model " + match.Model.ID + " has conditional OpenRouter price overrides, and the session total is not one request, so the base rate was not applied"
		return out
	}
	buckets, method, reason := bucketsFor(usage.Source, usage.Tokens)
	if reason != "" {
		out.Status = "unknown"
		out.Reason = reason
		return out
	}
	out.Method = method
	var total big.Rat
	var notes []string
	for _, item := range buckets {
		if item.tokens.Sign() == 0 {
			continue
		}
		perToken, priceText, note, reason := rateFor(match.Model, item)
		if reason != "" {
			out.Status = "unknown"
			out.Reason = reason
			out.Method = method
			out.Components = nil
			return out
		}
		if note != "" {
			notes = append(notes, note)
		}
		usd := new(big.Rat).Mul(item.tokens, perToken)
		total.Add(&total, usd)
		tokenText, ok := decimalString(item.tokens)
		usdText, usdOK := decimalString(usd)
		if !ok || !usdOK {
			out.Status = "unknown"
			out.Reason = "computed amount is not a terminating decimal"
			out.Components = nil
			return out
		}
		out.Components = append(out.Components, Component{
			Name:        item.name,
			Tokens:      tokenText,
			USDPerToken: priceText,
			USD:         usdText,
		})
	}
	text, ok := decimalString(&total)
	if !ok {
		out.Status = "unknown"
		out.Reason = "computed amount is not a terminating decimal"
		out.Components = nil
		return out
	}
	out.Status = "known"
	out.USD = text
	notes = append(notes, feeNote(match.Model))
	for _, note := range notes {
		if note == "" {
			continue
		}
		if out.Note != "" {
			out.Note = out.Note + "; " + note
		} else {
			out.Note = note
		}
	}
	return out
}

func rateFor(model *Model, item bucket) (rate *big.Rat, priceText, note, reason string) {
	key := item.priceKey
	if item.conflictKey != "" {
		primary, primaryOK := model.Prices[item.priceKey]
		alt, altOK := model.Prices[item.conflictKey]
		switch {
		case primaryOK && altOK && primary != alt:
			return nil, "", "", "model " + model.ID + " publishes both " + item.priceKey + " and " + item.conflictKey + " at different rates, and the token count does not say which rate applied"
		case primaryOK:
			key = item.priceKey
		case altOK:
			return nil, "", "", "model " + model.ID + " has " + item.conflictKey + " but no " + item.priceKey + ", and the token count does not say which cache lifetime was written"
		default:
			return nil, "", "", "catalog has no " + item.priceKey + " price for model " + model.ID
		}
	}
	text, ok := model.Prices[key]
	if !ok || text == "" {
		if item.fallbackKey != "" {
			text, ok = model.Prices[item.fallbackKey]
			key = item.fallbackKey
			note = item.name + " tokens are priced at " + item.fallbackKey + " because the catalog has no " + item.priceKey + " price"
		}
	}
	if !ok || text == "" {
		return nil, "", "", "catalog has no " + item.priceKey + " price for model " + model.ID
	}
	parsed, err := parseDecimal(text)
	if err != nil {
		return nil, "", "", "catalog price " + key + " for model " + model.ID + " is not a decimal"
	}
	if parsed.Sign() < 0 {
		return nil, "", "", "catalog price " + key + " for model " + model.ID + " is negative"
	}
	return parsed, text, note, ""
}

func feeNote(model *Model) string {
	var names []string
	for _, key := range []string{"request", "image", "image_output", "image_token", "web_search", "audio", "audio_output", "input_audio_cache"} {
		text, ok := model.Prices[key]
		if !ok || text == "" || text == "0" {
			continue
		}
		rate, err := parseDecimal(text)
		if err != nil || rate.Sign() == 0 {
			continue
		}
		names = append(names, key)
	}
	if len(names) == 0 {
		return ""
	}
	return "token prices only; catalog also lists " + strings.Join(names, ", ") + ", which were not added because those units are not in the token totals"
}

func bucketsFor(source string, tokens map[string]float64) ([]bucket, string, string) {
	switch source {
	case "metrics claude_code.token.usage":
		return disjoint(tokens, map[string]bucketSpec{
			"input":         {name: "input", priceKey: "prompt"},
			"output":        {name: "output", priceKey: "completion"},
			"cacheRead":     {name: "cache_read", priceKey: "input_cache_read"},
			"cacheCreation": {name: "cache_write", priceKey: "input_cache_write", conflictKey: "input_cache_write_1h"},
		}, "Claude Code token types are disjoint: input excludes cache read and cache creation (monitoring-usage)")
	case "logs claude_code.api_request":
		return disjoint(tokens, map[string]bucketSpec{
			"input_tokens":          {name: "input", priceKey: "prompt"},
			"output_tokens":         {name: "output", priceKey: "completion"},
			"cache_read_tokens":     {name: "cache_read", priceKey: "input_cache_read"},
			"cache_creation_tokens": {name: "cache_write", priceKey: "input_cache_write", conflictKey: "input_cache_write_1h"},
		}, "Claude Code api_request token fields are disjoint: input_tokens excludes cache read and cache creation")
	case "metrics codex.turn.token_usage":
		return codexBuckets(tokens, map[string]string{
			"input":             "input",
			"cached_input":      "cache_read",
			"cache_write_input": "cache_write",
			"output":            "output",
			"reasoning_output":  "reasoning",
		}, "total", "Codex turn.token_usage reports input, cached_input, cache_write_input, output, and reasoning_output; total is not priced. cached_input is part of input (TokenUsage.non_cached_input). cache_write_input is subtracted from input as well; a negative remainder is not priced")
	case "logs codex.sse_event response.completed":
		return codexBuckets(tokens, map[string]string{
			"input_token_count":       "input",
			"cached_token_count":      "cache_read",
			"cache_write_token_count": "cache_write",
			"output_token_count":      "output",
			"reasoning_token_count":   "reasoning",
		}, "", "Codex response.completed counts use the summary's max for input, cached, and cache_write, and the sum for output and reasoning. cached and cache_write are subtracted from input; reasoning is subtracted from output")
	case "traces SessionStep.attempt":
		return openCodeBuckets(tokens)
	case "traces chat", "metrics gen_ai.client.token.usage":
		return piBuckets(source, tokens)
	default:
		if source == "" {
			return nil, "", "token source is missing, so the counts were not priced"
		}
		return nil, "", fmt.Sprintf("no pricing rule for token source %q", source)
	}
}

type bucketSpec struct {
	name        string
	priceKey    string
	conflictKey string
}

func disjoint(tokens map[string]float64, spec map[string]bucketSpec, method string) ([]bucket, string, string) {
	var keys []string
	for key := range tokens {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var out []bucket
	for _, key := range keys {
		item, ok := spec[key]
		if !ok {
			return nil, "", fmt.Sprintf("token key %q is not part of this pricing rule", key)
		}
		amount, err := parseTokens(tokens[key])
		if err != nil {
			return nil, "", err.Error()
		}
		out = append(out, bucket{name: item.name, tokens: amount, priceKey: item.priceKey, conflictKey: item.conflictKey})
	}
	return out, method, ""
}

func codexBuckets(tokens map[string]float64, names map[string]string, ignore string, method string) ([]bucket, string, string) {
	values := map[string]*big.Rat{}
	for key, value := range tokens {
		if key == ignore {
			continue
		}
		label, ok := names[key]
		if !ok {
			return nil, "", fmt.Sprintf("token key %q is not part of this pricing rule", key)
		}
		amount, err := parseTokens(value)
		if err != nil {
			return nil, "", err.Error()
		}
		values[label] = amount
	}
	return overlapBuckets(values, method)
}

func openCodeBuckets(tokens map[string]float64) ([]bucket, string, string) {
	// gen_ai.usage.cache_read.input_tokens is the OpenTelemetry name for cache
	// reads included in input tokens. The summary has already replaced repeated
	// prefix counts with the max.
	aliases := map[string]string{
		"input_tokens":             "input",
		"output_tokens":            "output",
		"cache_read.input_tokens":  "cache_read",
		"cache_read_input_tokens":  "cache_read",
		"cache_write.input_tokens": "cache_write",
		"cache_write_input_tokens": "cache_write",
		"reasoning_tokens":         "reasoning",
	}
	seen := map[string]string{}
	values := map[string]*big.Rat{}
	var keys []string
	for key := range tokens {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		label, ok := aliases[key]
		if !ok {
			return nil, "", fmt.Sprintf("token key %q is not part of the OpenCode pricing rule", key)
		}
		if prev, ok := seen[label]; ok {
			return nil, "", fmt.Sprintf("token keys %q and %q would price %s twice", prev, key, label)
		}
		seen[label] = key
		amount, err := parseTokens(tokens[key])
		if err != nil {
			return nil, "", err.Error()
		}
		values[label] = amount
	}
	return overlapBuckets(values, "OpenCode input and cache totals already use the max across requests; cache read and cache write are priced as subsets of input, and reasoning as a subset of output")
}

func piBuckets(source string, tokens map[string]float64) ([]bucket, string, string) {
	allowed := map[string]bucketSpec{}
	method := "Pi summarized tokens are input and output only; cache and reasoning attributes are excluded by the token rule and are not priced"
	if source == "metrics gen_ai.client.token.usage" {
		allowed["input"] = bucketSpec{name: "input", priceKey: "prompt"}
		allowed["output"] = bucketSpec{name: "output", priceKey: "completion"}
		method = "Pi gen_ai.client.token.usage records input and output only; cache and reasoning are not on this metric and are not priced"
	} else {
		allowed["input_tokens"] = bucketSpec{name: "input", priceKey: "prompt"}
		allowed["output_tokens"] = bucketSpec{name: "output", priceKey: "completion"}
	}
	return disjoint(tokens, allowed, method)
}

func overlapBuckets(values map[string]*big.Rat, method string) ([]bucket, string, string) {
	input := values["input"]
	output := values["output"]
	cacheRead := values["cache_read"]
	cacheWrite := values["cache_write"]
	reasoning := values["reasoning"]
	if input == nil && (cacheRead != nil || cacheWrite != nil) {
		return nil, "", "cache tokens are present without an input total, so they were not priced"
	}
	if output == nil && reasoning != nil {
		return nil, "", "reasoning tokens are present without an output total, so they were not priced"
	}
	zero := new(big.Rat)
	if input == nil {
		input = zero
	}
	if output == nil {
		output = zero
	}
	if cacheRead == nil {
		cacheRead = zero
	}
	if cacheWrite == nil {
		cacheWrite = zero
	}
	if reasoning == nil {
		reasoning = zero
	}
	billableInput := new(big.Rat).Sub(input, cacheRead)
	billableInput.Sub(billableInput, cacheWrite)
	if billableInput.Sign() < 0 {
		return nil, "", "cache read plus cache write exceeds input, so the counts were not priced"
	}
	billableOutput := new(big.Rat).Sub(output, reasoning)
	if billableOutput.Sign() < 0 {
		return nil, "", "reasoning exceeds output, so the counts were not priced"
	}
	return []bucket{
		{name: "input", tokens: billableInput, priceKey: "prompt"},
		{name: "output", tokens: billableOutput, priceKey: "completion"},
		{name: "cache_read", tokens: cacheRead, priceKey: "input_cache_read"},
		{name: "cache_write", tokens: cacheWrite, priceKey: "input_cache_write", conflictKey: "input_cache_write_1h"},
		{name: "reasoning", tokens: reasoning, priceKey: "internal_reasoning", fallbackKey: "completion"},
	}, method, ""
}

func uniqueModels(models []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" || seen[model] {
			continue
		}
		seen[model] = true
		out = append(out, model)
	}
	return out
}

func parseTokens(value float64) (*big.Rat, error) {
	text := strconv.FormatFloat(value, 'f', -1, 64)
	return parseDecimal(text)
}

func parseDecimal(text string) (*big.Rat, error) {
	rat := new(big.Rat)
	if _, ok := rat.SetString(text); !ok {
		return nil, fmt.Errorf("not a decimal: %s", text)
	}
	return rat, nil
}

// decimalString renders rat as a plain decimal. The bool is false when the
// value is not a terminating decimal, which Price reports as unknown rather
// than rounding to a made-up place.
func decimalString(rat *big.Rat) (string, bool) {
	if rat.Sign() == 0 {
		return "0", true
	}
	neg := rat.Sign() < 0
	num := new(big.Int).Abs(rat.Num())
	den := new(big.Int).Set(rat.Denom())
	two := factor(den, 2)
	five := factor(den, 5)
	if den.Cmp(big.NewInt(1)) != 0 {
		return "", false
	}
	scale := two
	if five > scale {
		scale = five
	}
	if scale > 24 {
		return "", false
	}
	scaled := new(big.Int).Set(num)
	if scale > two {
		scaled.Mul(scaled, new(big.Int).Exp(big.NewInt(2), big.NewInt(int64(scale-two)), nil))
	}
	if scale > five {
		scaled.Mul(scaled, new(big.Int).Exp(big.NewInt(5), big.NewInt(int64(scale-five)), nil))
	}
	digits := scaled.String()
	var text string
	if scale == 0 {
		text = digits
	} else if len(digits) <= scale {
		digits = strings.Repeat("0", scale-len(digits)+1) + digits
		point := len(digits) - scale
		text = digits[:point] + "." + strings.TrimRight(digits[point:], "0")
	} else {
		point := len(digits) - scale
		frac := strings.TrimRight(digits[point:], "0")
		text = digits[:point]
		if frac != "" {
			text += "." + frac
		}
	}
	if neg {
		text = "-" + text
	}
	return text, true
}

func factor(n *big.Int, p int64) int {
	base := big.NewInt(p)
	count := 0
	mod := new(big.Int)
	for {
		mod.Mod(n, base)
		if mod.Sign() != 0 {
			return count
		}
		n.Quo(n, base)
		count++
	}
}
