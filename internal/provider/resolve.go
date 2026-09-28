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
	case "gemini":
		k := keys["gemini"]
		if k == "" {
			return nil, wire, pr, fmt.Errorf("no Gemini key (run `whis init` or set WHIS_GEMINI_KEY)")
		}
		return NewGemini(k, wire), wire, pr, nil
	case "xai":
		k := keys["xai"]
		if k == "" {
			return nil, wire, pr, fmt.Errorf("no xAI key (get one at console.x.ai)")
		}
		return NewXAI(k, wire), wire, pr, nil
	case "mistral":
		k := keys["mistral"]
		if k == "" {
			return nil, wire, pr, fmt.Errorf("no Mistral key (run `whis init`)")
		}
		return NewMistral(k, wire), wire, pr, nil
	case "moonshot":
		k := keys["moonshot"]
		if k == "" {
			return nil, wire, pr, fmt.Errorf("no Moonshot key (platform.kimi.ai; Coding-plan keys differ)")
		}
		return NewMoonshot(k, wire), wire, pr, nil
	case "qwen":
		k := keys["qwen"]
		if k == "" {
			return nil, wire, pr, fmt.Errorf("no Qwen key (international Model Studio console)")
		}
		return NewQwen(k, wire), wire, pr, nil
	case "zai":
		k := keys["zai"]
		if k == "" {
			return nil, wire, pr, fmt.Errorf("no Z.ai key (z.ai Model API)")
		}
		return NewZai(k, wire), wire, pr, nil
	case "minimax":
		k := keys["minimax"]
		if k == "" {
			return nil, wire, pr, fmt.Errorf("no MiniMax key (platform.minimax.io)")
		}
		return NewMiniMax(k, wire), wire, pr, nil
	case "groq":
		k := keys["groq"]
		if k == "" {
			return nil, wire, pr, fmt.Errorf("no Groq key (console.groq.com)")
		}
		return NewGroq(k, wire), wire, pr, nil
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

// SetKeysMap returns the full 13-provider key map for Resolve().
func SetKeysMap(keys map[string]string) map[string]string {
	for _, p := range []string{"gemini", "xai", "mistral", "moonshot", "qwen", "zai", "minimax", "groq"} {
		if _, ok := keys[p]; !ok {
			keys[p] = ""
		}
	}
	return keys
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
