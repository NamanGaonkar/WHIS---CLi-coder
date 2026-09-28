package provider

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// ModelInfo is one model available on a provider key (or local daemon).
type ModelInfo struct {
	ID      string  // provider model id
	Slug    string  // full whis slug (e.g. "or:vendor/name" or "ollama:x")
	Context int     // context window in tokens (0 = unknown)
	In      float64 // USD per million input tokens (0 = unknown)
	Out     float64 // USD per million output tokens (0 = unknown)
}

// fetchTimeout bounds every catalog request.
var fetchTimeout = 8 * time.Second

// modelsListJSON covers OpenAI / DeepSeek / OpenRouter /list responses.
type modelsListJSON struct {
	Data []struct {
		ID            string `json:"id"`
		ContextLength int    `json:"context_length"`
		Topology      struct {
			ContextLength int `json:"context_length"`
		} `json:"topology"`
		Pricing struct {
			Prompt     string `json:"prompt"`
			Completion string `json:"completion"`
		} `json:"pricing"`
	} `json:"data"`
}

// anthropicListJSON covers the Anthropic /v1/models response.
type anthropicListJSON struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}

// ollamaTagsJSON covers the local daemon /api/tags response with sizes.
type ollamaTagsJSON struct {
	Models []struct {
		Name    string `json:"name"`
		Size    uint64 `json:"size"`
		Digest  string `json:"digest"`
		Details struct {
			ParameterSize string `json:"parameter_size"`
			Quantization  string `json:"quantization_level"`
		} `json:"details"`
	} `json:"models"`
}

// FetchModels returns the live model catalog for a provider using its key.
// Local Ollama is detected from the daemon; everything else hits the vendor
// /models endpoint. Metrics (context, pricing) are captured when published.
func FetchModels(prov, key string) ([]ModelInfo, error) {
	switch prov {
	case "ollama":
		return fetchOllamaModels(OllamaHost())
	case "deepseek":
		return fetchOpenAIStyle("https://api.deepseek.com/models", key, "", false)
	case "openai":
		return fetchOpenAIStyle("https://api.openai.com/v1/models", key, "gpt", false)
	case "anthropic":
		return fetchAnthropicModels(key)
	case "openrouter":
		return fetchOpenAIStyle("https://openrouter.ai/api/v1/models", key, "", true)
	case "ollama-cloud":
		return fetchOllamaCloudModels(key)
	case "gemini":
		return fetchGeminiModels(key)
	case "xai":
		return fetchOpenAIStyle("https://api.x.ai/v1/models", key, "grok", false)
	case "mistral":
		return fetchOpenAIStyle("https://api.mistral.ai/v1/models", key, "", false)
	case "moonshot":
		return fetchOpenAIStyle("https://api.moonshot.ai/v1/models", key, "kimi", false)
	case "qwen":
		return fetchOpenAIStyle("https://dashscope-intl.aliyuncs.com/compatible-mode/v1/models", key, "qwen", false)
	case "zai":
		return fetchOpenAIStyle("https://api.z.ai/api/paas/v4/models", key, "glm", false)
	case "minimax":
		return fetchOpenAIStyle("https://api.minimax.io/v1/models", key, "", false)
	case "groq":
		return fetchOpenAIStyle("https://api.groq.com/openai/v1/models", key, "", false)
	}
	return nil, fmt.Errorf("unknown provider %q", prov)
}

// geminiModelsURL is a var so tests can point the fetcher at a stub.
var geminiModelsURL = "https://generativelanguage.googleapis.com/v1beta/models"

// fetchGeminiModels lists models from Google's NATIVE v1beta endpoint.
// The OpenAI-compat /openai/models path 404s for keyless/blocked callers,
// which surfaced to users as a stuck "fetching live models…" row. The
// native endpoint also wants the x-goog-api-key header, not Bearer.
func fetchGeminiModels(key string) ([]ModelInfo, error) {
	var raw struct {
		Models []struct {
			Name                       string   `json:"name"`
			SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
			InputTokenLimit            int      `json:"inputTokenLimit"`
		} `json:"models"`
	}
	if err := httpGetJSONHeader(geminiModelsURL, "x-goog-api-key", key, &raw); err != nil {
		return nil, err
	}
	var models []ModelInfo
	for _, m := range raw.Models {
		name := strings.TrimPrefix(m.Name, "models/")
		if name == "" || !strings.HasPrefix(name, "gemini-") {
			continue
		}
		chat := false
		for _, meth := range m.SupportedGenerationMethods {
			if meth == "generateContent" {
				chat = true
				break
			}
		}
		if !chat { // embeddings/aiera-style rows are useless in whis
			continue
		}
		// tts/image/video rows also expose generateContent but cannot code
		if strings.Contains(name, "-tts") || strings.Contains(name, "imagen") ||
			strings.Contains(name, "-image") || strings.Contains(name, "-native-audio") {
			continue
		}
		models = append(models, ModelInfo{
			ID:      name,
			Slug:    "gemini:" + name,
			Context: m.InputTokenLimit,
		})
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("no chat-capable gemini models returned")
	}
	return models, nil
}

// httpGetJSONHeader is httpGetJSON with an explicit auth header name
// (Google-style native APIs use x-goog-api-key, not Bearer).
func httpGetJSONHeader(url, header, key string, out any) error {
	client := &http.Client{Timeout: fetchTimeout}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if key != "" {
		req.Header.Set(header, key)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(out)
}

func httpGetJSON(url, key string, out any) error {
	client := &http.Client{Timeout: fetchTimeout}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(out)
}

// fetchOpenAIStyle parses the OpenAI-family /models shape. parsePricing
// enables OpenRouter per-model USD pricing strings.
func fetchOpenAIStyle(url, key, prefix string, parsePricing bool) ([]ModelInfo, error) {
	var out modelsListJSON
	if err := httpGetJSON(url, key, &out); err != nil {
		return nil, err
	}
	var models []ModelInfo
	for _, d := range out.Data {
		id := d.ID
		if id == "" {
			continue
		}
		if prefix != "" && !strings.HasPrefix(id, prefix) {
			continue
		}
		mi := ModelInfo{ID: id, Context: max(d.ContextLength, d.Topology.ContextLength)}
		if parsePricing && d.Pricing.Prompt != "" {
			mi.In = parseUSD(d.Pricing.Prompt)
			mi.Out = parseUSD(d.Pricing.Completion)
		}
		models = append(models, mi)
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("no models visible for this key")
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models, nil
}

// parseUSD converts OpenRouter pricing strings ("0.0000015") to USD/MTok.
func parseUSD(s string) float64 {
	v, err := strconvParseFloat(strings.TrimSpace(s))
	if err != nil || v <= 0 {
		return 0
	}
	return v * 1e6
}

func strconvParseFloat(s string) (float64, error) {
	var f float64
	err := json.Unmarshal([]byte(s), &f)
	return f, err
}

// fetchAnthropicModels lists Anthropic model ids (metrics not published here).
func fetchAnthropicModels(key string) ([]ModelInfo, error) {
	var out anthropicListJSON
	if err := httpGetJSON("https://api.anthropic.com/v1/models", key, &out); err != nil {
		// header-based auth for anthropic
		return nil, err
	}
	var models []ModelInfo
	for _, d := range out.Data {
		if d.ID != "" {
			models = append(models, ModelInfo{ID: d.ID})
		}
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("no models visible for this key")
	}
	return models, nil
}

// fetchOllamaCloudModels lists cloud-hosted models from ollama.com.
func fetchOllamaCloudModels(key string) ([]ModelInfo, error) {
	var out ollamaTagsJSON
	client := &http.Client{Timeout: fetchTimeout}
	req, _ := http.NewRequest(http.MethodGet, "https://ollama.com/api/tags", nil)
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&out); err != nil {
		return nil, err
	}
	var models []ModelInfo
	for _, m := range out.Models {
		if m.Name == "" {
			continue
		}
		models = append(models, ModelInfo{ID: m.Name, Slug: "ollama-cloud:" + m.Name})
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("no cloud models visible for this key")
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models, nil
}

// fetchOllamaModels lists installed local models with size + param metrics.
func fetchOllamaModels(base string) ([]ModelInfo, error) {
	var out ollamaTagsJSON
	client := &http.Client{Timeout: fetchTimeout}
	resp, err := client.Get(base + "/api/tags")
	if err != nil {
		return nil, fmt.Errorf("ollama not reachable at %s", base)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama HTTP %d", resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&out); err != nil {
		return nil, err
	}
	var models []ModelInfo
	for _, m := range out.Models {
		if m.Name == "" {
			continue
		}
		mi := ModelInfo{ID: m.Name, Slug: "ollama:" + m.Name}
		mi.In = float64(m.Size) / (1 << 30) // repurpose: size in GiB shown in UI
		models = append(models, mi)
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("no models installed (pull one: ollama pull <name>)")
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models, nil
}

// FetchModelsConcurrent fetches for many providers in parallel; failures are
// reported per provider. Used to pre-warm menus.
func FetchModelsConcurrent(reqs map[string]string) (map[string][]ModelInfo, map[string]error) {
	var mu sync.Mutex
	type pair struct {
		prov   string
		models []ModelInfo
		err    error
	}
	done := make(chan pair, len(reqs))
	for prov, key := range reqs {
		go func(p, k string) {
			ms, err := FetchModels(p, k)
			done <- pair{p, ms, err}
		}(prov, key)
	}
	out := map[string][]ModelInfo{}
	errs := map[string]error{}
	for range reqs {
		p := <-done
		mu.Lock()
		if p.err != nil {
			errs[p.prov] = p.err
		} else {
			out[p.prov] = p.models
		}
		mu.Unlock()
	}
	return out, errs
}

// SyncCatalogWithPricing merges live catalog entries into the static price
// table for known slugs, so cost math stays accurate after hot-swap.
func SyncCatalogWithPricing(prov string, models []ModelInfo) {
	mu.Lock()
	defer mu.Unlock()
	for _, m := range models {
		slug := m.Slug
		if slug == "" {
			continue
		}
		if e, ok := catalog[slug]; ok && (m.In > 0 || m.Out > 0) {
			if m.In > 0 {
				e.prices.In = m.In
			}
			if m.Out > 0 {
				e.prices.Out = m.Out
			}
			if m.Context > 0 {
				e.prices.Context = m.Context
			}
			catalog[slug] = e
		}
	}
	_ = prov
}

var catalogMu sync.Mutex

// ContextWindow returns the best-known context window for a wire model.
func ContextWindow(prov, wire string) int {
	mu.Lock()
	defer mu.Unlock()
	for _, e := range catalog {
		if e.model == wire {
			if e.prices.Context > 0 {
				return e.prices.Context
			}
		}
	}
	switch prov {
	case "anthropic":
		return 200 * 1024
	case "deepseek", "openai":
		return 128 * 1024
	case "openrouter":
		return 128 * 1024
	default:
		return 32 * 1024
	}
}

var mu = &sync.Mutex{}
