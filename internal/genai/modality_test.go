package genai

import "testing"

func TestIsModalityUsage(t *testing.T) {
	for label, want := range map[string]bool{
		"text.input_tokens":             true,
		"image.output_tokens":           true,
		"audio.cache_read.input_tokens": true,
		"video.input_tokens":            true,
		"text.cache_write.input_tokens": true,
		"hologram.input_tokens":         false,
		"cache_read.input_tokens":       false,
		"cache_write.input_tokens":      false,
		"cache_creation.input_tokens":   false,
		"reasoning.output_tokens":       false,
		"input_tokens":                  false,
		"text.total_tokens":             false,
		"Text.input_tokens":             false,
	} {
		if got := IsModalityUsage(label); got != want {
			t.Errorf("%s: got %v want %v", label, got, want)
		}
	}
}

func TestSplitModalityUsage(t *testing.T) {
	for label, want := range map[string][2]string{
		"text.input_tokens":              {ModalityText, UsageLabelInputTokens},
		"image.output_tokens":            {ModalityImage, UsageLabelOutputTokens},
		"audio.cache_read.input_tokens":  {ModalityAudio, UsageLabelCacheReadInputTokens},
		"video.cache_write.input_tokens": {ModalityVideo, UsageLabelCacheWriteInputTokens},
	} {
		modality, total, ok := SplitModalityUsage(label)
		if !ok || modality != want[0] || total != want[1] {
			t.Errorf("%s: got %q %q %v", label, modality, total, ok)
		}
	}
	if _, _, ok := SplitModalityUsage("text.reasoning.output_tokens"); ok {
		t.Error("text.reasoning.output_tokens is not a v0.1.0 modality breakdown")
	}
}

// Every per-modality key IsModalityUsage accepts is either in the v0.1.0
// registry or a documented forward-compatible name.
func TestModalityUsageAgainstRegistry(t *testing.T) {
	r := loadRegistry(t)
	forward := map[string]bool{
		// cache_write per modality is not in the v0.1.0 registry; accepted
		// for forward compatibility only.
		"text.cache_write.input_tokens":  true,
		"image.cache_write.input_tokens": true,
		"audio.cache_write.input_tokens": true,
	}
	for _, modality := range []string{ModalityText, ModalityImage, ModalityAudio, ModalityVideo} {
		for _, total := range []string{UsageLabelInputTokens, UsageLabelOutputTokens, UsageLabelCacheReadInputTokens, UsageLabelCacheWriteInputTokens} {
			label := modality + "." + total
			if !IsModalityUsage(label) {
				t.Errorf("%s not accepted", label)
				continue
			}
			inRegistry := r.attributes[UsagePrefix+label]
			expected := modality != ModalityVideo && !forward[label]
			if inRegistry != expected {
				t.Errorf("%s: in v0.1.0 registry = %v, want %v; update the forward-compatible list", label, inRegistry, expected)
			}
		}
	}
}
