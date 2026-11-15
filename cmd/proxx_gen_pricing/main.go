// Command proxx_gen_pricing regenerates the embedded proxx price snapshot
// (internal/proxx/pricing/snapshot.json) from the models.dev catalog.
//
// The snapshot keeps the models.dev format but only cost data, and only for:
//   - every model of a small set of popular providers, and
//   - models listed in internal/registry/models/models.json found under any provider.
//
// Usage:
//
//	go run ./cmd/proxx_gen_pricing                       # fetch https://models.dev/api.json
//	go run ./cmd/proxx_gen_pricing -in /path/api.json    # use a downloaded copy
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/proxx/pricing"
)

var popularProviders = []string{
	"anthropic", "openai", "google", "xai", "deepseek", "mistral", "moonshotai", "groq", "openrouter",
}

type cost struct {
	Input      *float64 `json:"input,omitempty"`
	Output     *float64 `json:"output,omitempty"`
	CacheRead  *float64 `json:"cache_read,omitempty"`
	CacheWrite *float64 `json:"cache_write,omitempty"`
	Reasoning  *float64 `json:"reasoning,omitempty"`
}

type model struct {
	Cost cost `json:"cost"`
}

type provider struct {
	ID     string           `json:"id"`
	Models map[string]model `json:"models"`
}

func main() {
	in := flag.String("in", pricing.DefaultCatalogURL, "models.dev api.json path or URL")
	registryPath := flag.String("models", "internal/registry/models/models.json", "registry models.json")
	out := flag.String("out", "internal/proxx/pricing/snapshot.json", "output snapshot path")
	flag.Parse()
	if err := run(*in, *registryPath, *out); err != nil {
		fmt.Fprintln(os.Stderr, "proxx_gen_pricing:", err)
		os.Exit(1)
	}
}

func run(in, registryPath, out string) error {
	data, err := read(in)
	if err != nil {
		return err
	}
	if err = pricing.ValidateCatalog(data); err != nil {
		return err
	}
	registryData, err := os.ReadFile(registryPath)
	if err != nil {
		return err
	}
	wanted := map[string]bool{}
	ojson.ParseBytes(registryData).ForEach(func(_, list ojson.Result) bool {
		for _, m := range list.Array() {
			if id := strings.ToLower(m.Get("id").String()); id != "" {
				wanted[id] = true
				wanted[pricing.NormalizeModelID(id)] = true
			}
		}
		return true
	})

	popular := map[string]bool{}
	for _, p := range popularProviders {
		popular[p] = true
	}
	result := map[string]*provider{}
	covered := map[string]bool{}
	root := ojson.ParseBytes(data)
	// Pass 1: popular providers in full. Pass 2: registry models from other providers.
	for pass := 1; pass <= 2; pass++ {
		var ids []string
		root.ForEach(func(k, _ ojson.Result) bool {
			ids = append(ids, k.String())
			return true
		})
		sort.Strings(ids)
		for _, pid := range ids {
			if (pass == 1) != popular[pid] {
				continue
			}
			root.Get(ojson.Escape(pid)).Get("models").ForEach(func(mk, mv ojson.Result) bool {
				id := strings.ToLower(mk.String())
				norm := pricing.NormalizeModelID(id)
				if pass == 2 && (!(wanted[id] || wanted[norm]) || covered[id] || covered[norm]) {
					return true
				}
				c := mv.Get("cost")
				entry := cost{
					Input:      num(c.Get("input")),
					Output:     num(c.Get("output")),
					CacheRead:  num(c.Get("cache_read")),
					CacheWrite: num(c.Get("cache_write")),
					Reasoning:  num(c.Get("reasoning")),
				}
				if entry.Input == nil && entry.Output == nil {
					return true
				}
				p := result[pid]
				if p == nil {
					p = &provider{ID: pid, Models: map[string]model{}}
					result[pid] = p
				}
				p.Models[id] = model{Cost: entry}
				covered[id], covered[norm] = true, true
				return true
			})
		}
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return err
	}
	if err = pricing.ValidateCatalog(encoded); err != nil {
		return err
	}
	if err = os.WriteFile(out, append(encoded, '\n'), 0o644); err != nil {
		return err
	}
	total := 0
	for _, p := range result {
		total += len(p.Models)
	}
	fmt.Printf("wrote %s: %d providers, %d models, %d bytes\n", out, len(result), total, len(encoded))
	return nil
}

func num(r ojson.Result) *float64 {
	if r.Type != ojson.Number {
		return nil
	}
	v := r.Float()
	return &v
}

func read(in string) ([]byte, error) {
	if !strings.HasPrefix(in, "http://") && !strings.HasPrefix(in, "https://") {
		return os.ReadFile(in)
	}
	resp, err := http.Get(in)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: HTTP %d", in, resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}
