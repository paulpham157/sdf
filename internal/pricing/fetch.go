package pricing

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// testSource is accepted by Parse so unit tests can build a tiny catalog
// without pretending it came from OpenRouter.
const testSource = "test-fixture"

const maxCatalogBytes = 20 << 20

// priceFields are copied as strings. overrides is copied separately.
// Anything else in the API object is dropped so the file stays a price table.
var priceFields = []string{
	"prompt",
	"completion",
	"request",
	"image",
	"image_output",
	"image_token",
	"web_search",
	"internal_reasoning",
	"input_cache_read",
	"input_cache_write",
	"input_cache_write_1h",
	"audio",
	"audio_output",
	"input_audio_cache",
}

type apiDoc struct {
	Data []apiModel `json:"data"`
}

type apiModel struct {
	ID      string                     `json:"id"`
	Pricing map[string]json.RawMessage `json:"pricing"`
}

// Snapshot is a catalog and the JSON to write for it.
type Snapshot struct {
	Catalog *Catalog
	Body    []byte
}

// Fetch downloads SourceURL, or pageURL when that is not empty, and returns
// a catalog of the same shape as the bundled file. It does not send a key.
func Fetch(ctx context.Context, client *http.Client, pageURL string) (*Snapshot, error) {
	if pageURL == "" {
		pageURL = SourceURL
	}
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "sdf")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxCatalogBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxCatalogBytes {
		return nil, fmt.Errorf("openrouter models response exceeds %d bytes", maxCatalogBytes)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("openrouter models: HTTP %d", resp.StatusCode)
	}
	var doc apiDoc
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("openrouter models: %w", err)
	}
	retrieved := time.Now().UTC().Format(time.RFC3339)
	file := fileDoc{
		Source:      SourceURL,
		RetrievedAt: retrieved,
		Models:      make([]fileModel, 0, len(doc.Data)),
	}
	skipped := 0
	for _, row := range doc.Data {
		encoded, ok, err := encodePricing(row)
		if err != nil {
			return nil, err
		}
		if !ok {
			skipped++
			continue
		}
		file.Models = append(file.Models, fileModel{ID: row.ID, Pricing: encoded})
	}
	sort.Slice(file.Models, func(i, j int) bool { return file.Models[i].ID < file.Models[j].ID })
	file.ModelCount = len(file.Models)
	file.Skipped = skipped
	raw, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return nil, err
	}
	raw = append(raw, '\n')
	cat, err := Parse(raw, "cache")
	if err != nil {
		return nil, err
	}
	return &Snapshot{Catalog: cat, Body: raw}, nil
}

func encodePricing(row apiModel) (json.RawMessage, bool, error) {
	if strings.TrimSpace(row.ID) == "" || row.Pricing == nil {
		return nil, false, nil
	}
	prompt, promptOK := rawString(row.Pricing["prompt"])
	completion, completionOK := rawString(row.Pricing["completion"])
	if !promptOK || !completionOK || prompt == "" || completion == "" {
		return nil, false, nil
	}
	out := map[string]any{}
	for _, key := range priceFields {
		text, ok := rawString(row.Pricing[key])
		if ok {
			out[key] = text
		}
	}
	if raw, ok := row.Pricing["overrides"]; ok {
		var items []json.RawMessage
		if err := json.Unmarshal(raw, &items); err != nil {
			return nil, false, fmt.Errorf("%s overrides: %w", row.ID, err)
		}
		if len(items) > 0 {
			out["overrides"] = json.RawMessage(raw)
		}
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return nil, false, err
	}
	return encoded, true, nil
}

func rawString(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return "", false
	}
	return text, true
}
