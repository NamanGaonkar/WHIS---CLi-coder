package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// OllamaHost returns the local daemon base URL (OLLAMA_HOST or default).
func OllamaHost() string { return osOllamaHost() }

// DetectOllamaModels probes the local daemon and returns raw model names
// (no prefix); exported for CLI default-model picking.
func DetectOllamaModels(base string) []string {
	models := detectOllama(base)
	out := make([]string, 0, len(models))
	for _, m := range models {
		out = append(out, strings.TrimPrefix(m, "ollama:"))
	}
	return out
}

// modelsJSON mirrors `GET {base}/v1/models` / `GET {base}/api/tags` minimal shape.
type modelsJSON struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
	Models []struct {
		Name string `json:"name"`
	} `json:"models"`
}

// detectOllama probes a local Ollama daemon and returns its models.
// Returns nil if the daemon is not reachable.
func detectOllama(base string) []string {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(base + "/api/tags")
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	var out modelsJSON
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil || json.Unmarshal(b, &out) != nil {
		return nil
	}
	var names []string
	seen := map[string]bool{}
	for _, m := range out.Models {
		if m.Name != "" && !seen[m.Name] {
			seen[m.Name] = true
			names = append(names, "ollama:"+m.Name)
		}
	}
	return names
}

// getEnv is a small helper shared by provider files.
func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// Resolve builds the concrete provider for a slug using resolved keys.
// cfg keys come from config.Load().
func Resolve(slug string, keys map[string]string) (Provider, string, Prices, error) {
	prov, wire, pr, err := Get(slug)
	if err != nil {
		return nil, slug, pr, err
	}
	switch prov {
	case "deepseek":
		k := keys["deepseek"]
		if k == "" {
			return nil, wire, pr, fmt.Errorf("no DeepSeek key (run `whis init` or set WHIS_DEEPSEEK_KEY)")
		}
		return NewDeepSeek(k, wire), wire, pr, nil
	case "anthropic":
		k := keys["anthropic"]
		if k == "" {
			return nil, wire, pr, fmt.Errorf("no Anthropic key (run `whis init` or set WHIS_ANTHROPIC_KEY)")
		}
		return NewAnthropic(k, wire), wire, pr, nil
	case "openai":
		k := keys["openai"]
		if k == "" {
			return nil, wire, pr, fmt.Errorf("no OpenAI key (run `whis init` or set WHIS_OPENAI_KEY)")
		}
		return NewOpenAI(k, wire), wire, pr, nil
	case "openrouter":
		k := keys["openrouter"]
		if k == "" {
			return nil, wire, pr, fmt.Errorf("no OpenRouter key (run `whis init` or set WHIS_OPENROUTER_KEY)")
		}
		return NewOpenRouter(k, wire), wire, pr, nil
	case "ollama":
		return NewOllama(wire), wire, pr, nil
	case "ollama-cloud":
		k := keys["ollama-cloud"]
		if k == "" {
			return nil, wire, pr, fmt.Errorf("no Ollama cloud key (run `whis init`)")
		}
		return NewOllamaCloud(k, wire), wire, pr, nil
	}
	return nil, wire, pr, fmt.Errorf("unsupported provider %q", prov)
}

// KeysMap flattens a resolved key set for Resolve().
func KeysMap(anthropic, openai, deepseek, openrouter, ollamaCloud string) map[string]string {
	return map[string]string{
		"anthropic": anthropic, "openai": openai, "deepseek": deepseek,
		"openrouter": openrouter, "ollama-cloud": ollamaCloud,
	}
}

// PipingContext is a context for headless runs (exported so main can pass one).
func PipingContext() context.Context { return context.Background() }

// bufio/bytes/time are used by the clients above; keep imports honest here.
var (
	_ = bufio.NewScanner
	_ = bytes.NewReader
	_ = context.Background
	_ = fmt.Sprintf
	_ = io.EOF
	_ = http.MethodGet
	_ = strings.TrimSpace
	_ = time.Second
)
