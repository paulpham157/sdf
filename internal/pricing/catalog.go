// Package pricing prices a session from token counts and a catalog copied
// from OpenRouter's public models API.
//
// The bundled file is internal/pricing/openrouter.json. Each pricing string
// is the value OpenRouter published for that model at retrieved_at. SDF does
// not fill in a missing price. https://openrouter.ai/api/v1/models and
// https://openrouter.ai/docs/guides/overview/models (USD per token; "0"
// means that component is free; pricing.overrides are conditional rates).
package pricing

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// SourceURL is the OpenRouter models list the catalog is copied from.
const SourceURL = "https://openrouter.ai/api/v1/models"

//go:embed openrouter.json
var bundledJSON []byte

// Catalog is one snapshot of per-token prices.
type Catalog struct {
	Source         string
	RetrievedAt    time.Time
	RetrievedAtRaw string
	Table          string // bundled, cache, or file
	Warning        string
	Models         map[string]*Model
	suffix         map[string][]string
}

// Model is one catalog row. Prices holds the string fields from the API.
// HasOverrides is true when the row included a non-empty pricing.overrides
// array; those conditional rates are not applied.
type Model struct {
	ID           string
	Prices       map[string]string
	HasOverrides bool
}

type fileModel struct {
	ID      string          `json:"id"`
	Pricing json.RawMessage `json:"pricing"`
}

type fileDoc struct {
	Source      string      `json:"source"`
	RetrievedAt string      `json:"retrieved_at"`
	Models      []fileModel `json:"models"`
	ModelCount  int         `json:"model_count"`
	Skipped     int         `json:"skipped"`
}

var (
	bundledOnce sync.Once
	bundled     *Catalog
	bundledErr  error
)

// Bundled returns the catalog compiled into the binary.
func Bundled() *Catalog {
	bundledOnce.Do(func() {
		bundled, bundledErr = Parse(bundledJSON, "bundled")
	})
	if bundledErr != nil {
		panic(bundledErr)
	}
	return bundled
}

// Parse decodes a catalog file. table is "bundled", "cache", or "file".
func Parse(data []byte, table string) (*Catalog, error) {
	var doc fileDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("price catalog: %w", err)
	}
	return build(doc, table)
}

func build(doc fileDoc, table string) (*Catalog, error) {
	if doc.Source == "" {
		return nil, fmt.Errorf("price catalog: missing source")
	}
	if doc.Source != SourceURL && doc.Source != testSource {
		return nil, fmt.Errorf("price catalog: source %q is not %s", doc.Source, SourceURL)
	}
	when, err := time.Parse(time.RFC3339, doc.RetrievedAt)
	if err != nil {
		return nil, fmt.Errorf("price catalog: retrieved_at: %w", err)
	}
	cat := &Catalog{
		Source:         doc.Source,
		RetrievedAt:    when.UTC(),
		RetrievedAtRaw: doc.RetrievedAt,
		Table:          table,
		Models:         make(map[string]*Model, len(doc.Models)),
		suffix:         map[string][]string{},
	}
	for _, row := range doc.Models {
		model, err := decodeModel(row)
		if err != nil {
			return nil, err
		}
		if _, ok := cat.Models[model.ID]; ok {
			return nil, fmt.Errorf("price catalog: duplicate model %q", model.ID)
		}
		cat.Models[model.ID] = model
		if slash := strings.IndexByte(model.ID, '/'); slash > 0 && strings.IndexByte(model.ID[slash+1:], '/') < 0 {
			suffix := model.ID[slash+1:]
			cat.suffix[suffix] = append(cat.suffix[suffix], model.ID)
		}
	}
	if doc.ModelCount != 0 && doc.ModelCount != len(cat.Models) {
		return nil, fmt.Errorf("price catalog: model_count is %d, parsed %d", doc.ModelCount, len(cat.Models))
	}
	return cat, nil
}

func decodeModel(row fileModel) (*Model, error) {
	if row.ID == "" {
		return nil, fmt.Errorf("price catalog: model with empty id")
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(row.Pricing, &raw); err != nil {
		return nil, fmt.Errorf("price catalog: %s: %w", row.ID, err)
	}
	prices := make(map[string]string, len(raw))
	hasOverrides := false
	for key, value := range raw {
		if key == "overrides" {
			var items []json.RawMessage
			if err := json.Unmarshal(value, &items); err != nil {
				return nil, fmt.Errorf("price catalog: %s overrides: %w", row.ID, err)
			}
			hasOverrides = len(items) > 0
			continue
		}
		var text string
		if err := json.Unmarshal(value, &text); err != nil {
			return nil, fmt.Errorf("price catalog: %s %s is not a string", row.ID, key)
		}
		prices[key] = text
	}
	if _, ok := prices["prompt"]; !ok {
		return nil, fmt.Errorf("price catalog: %s has no prompt price", row.ID)
	}
	if _, ok := prices["completion"]; !ok {
		return nil, fmt.Errorf("price catalog: %s has no completion price", row.ID)
	}
	return &Model{ID: row.ID, Prices: prices, HasOverrides: hasOverrides}, nil
}

// Match is one reported model id resolved to a catalog row.
type Match struct {
	Model    *Model
	By       string // exact, openrouter_prefix, or unique_suffix
	Reported string
}

// Resolve maps one reported model id onto a catalog row.
// An ambiguous or missing id returns a reason and a nil model.
func (c *Catalog) Resolve(reported string) (Match, string) {
	reported = strings.TrimSpace(reported)
	if reported == "" {
		return Match{}, "empty model id"
	}
	if model, ok := c.Models[reported]; ok {
		return Match{Model: model, By: "exact", Reported: reported}, ""
	}
	if rest, ok := strings.CutPrefix(reported, "openrouter/"); ok {
		if model, ok := c.Models[rest]; ok {
			return Match{Model: model, By: "openrouter_prefix", Reported: reported}, ""
		}
	}
	if !strings.Contains(reported, "/") {
		hits := c.suffix[reported]
		switch len(hits) {
		case 1:
			return Match{Model: c.Models[hits[0]], By: "unique_suffix", Reported: reported}, ""
		case 0:
		default:
			shown := hits
			extra := ""
			if len(shown) > 5 {
				extra = fmt.Sprintf(" (%d more)", len(shown)-5)
				shown = shown[:5]
			}
			return Match{}, fmt.Sprintf("model %q matches more than one catalog id: %s%s", reported, strings.Join(shown, ", "), extra)
		}
	}
	return Match{}, fmt.Sprintf("no price for model %q in the OpenRouter catalog retrieved %s", reported, c.RetrievedAtRaw)
}

// CachePath is the optional snapshot written by `sdf prices refresh`.
// SDF_PRICE_CACHE overrides the directory. An empty SDF_PRICE_CACHE uses
// the user cache directory.
func CachePath() (string, error) {
	dir := os.Getenv("SDF_PRICE_CACHE")
	if dir == "" {
		base, err := os.UserCacheDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(base, "sdf")
	}
	return filepath.Join(dir, "openrouter-models.json"), nil
}

// NewFixture builds a catalog for tests. source is test-fixture, not OpenRouter.
func NewFixture(retrievedAt string, models []*Model) (*Catalog, error) {
	when, err := time.Parse(time.RFC3339, retrievedAt)
	if err != nil {
		return nil, err
	}
	cat := &Catalog{
		Source:         testSource,
		RetrievedAt:    when.UTC(),
		RetrievedAtRaw: retrievedAt,
		Table:          "file",
		Models:         make(map[string]*Model, len(models)),
		suffix:         map[string][]string{},
	}
	for _, model := range models {
		if model == nil || model.ID == "" {
			return nil, fmt.Errorf("fixture model is missing an id")
		}
		if _, ok := model.Prices["prompt"]; !ok {
			return nil, fmt.Errorf("fixture %s has no prompt price", model.ID)
		}
		if _, ok := model.Prices["completion"]; !ok {
			return nil, fmt.Errorf("fixture %s has no completion price", model.ID)
		}
		if _, ok := cat.Models[model.ID]; ok {
			return nil, fmt.Errorf("fixture duplicate model %q", model.ID)
		}
		cat.Models[model.ID] = model
		if slash := strings.IndexByte(model.ID, '/'); slash > 0 && strings.IndexByte(model.ID[slash+1:], '/') < 0 {
			suffix := model.ID[slash+1:]
			cat.suffix[suffix] = append(cat.suffix[suffix], model.ID)
		}
	}
	return cat, nil
}

// ResolveCatalog picks the table for a summary.
// spec "" uses the cache file when it exists and otherwise the bundled table.
// spec "bundled" forces the embedded table. Any other spec is a file path.
func ResolveCatalog(spec string) (*Catalog, error) {
	switch spec {
	case "bundled":
		return Bundled(), nil
	case "":
		path, err := CachePath()
		if err != nil {
			cat := Bundled()
			clone := *cat
			clone.Warning = "price cache path: " + err.Error()
			return &clone, nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				return Bundled(), nil
			}
			return nil, fmt.Errorf("price cache: %w", err)
		}
		cat, err := Parse(data, "cache")
		if err != nil {
			return nil, fmt.Errorf("price cache %s: %w", path, err)
		}
		return cat, nil
	default:
		data, err := os.ReadFile(spec)
		if err != nil {
			return nil, fmt.Errorf("price catalog: %w", err)
		}
		return Parse(data, "file")
	}
}
