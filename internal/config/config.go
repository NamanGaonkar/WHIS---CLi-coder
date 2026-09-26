// Package config loads and persists the WHIS config (~/.whis/config.json).
package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// Keys holds API keys for remote providers.
type Keys struct {
	Anthropic   string `json:"anthropic,omitempty"`
	OpenAI      string `json:"openai,omitempty"`
	DeepSeek    string `json:"deepseek,omitempty"`
	OpenRouter  string `json:"openrouter,omitempty"`
	OllamaCloud string `json:"ollama_cloud,omitempty"`
}

// Config is the on-disk WHIS configuration.
type Config struct {
	Keys Keys `json:"keys"`
	// Model is the last used model, e.g. "deepseek-flash", "claude-sonnet-4", "ollama:qwen2.5-coder:7b".
	Model string `json:"model,omitempty"`
	// AutoApprove mirrors the -y flag for convenience.
	AutoApprove bool `json:"auto_approve,omitempty"`
}

// Dir returns ~/.whis (created if missing).
func Dir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	d := filepath.Join(home, ".whis")
	_ = os.MkdirAll(d, 0o755)
	return d
}

// Path is the config file path.
func Path() string { return filepath.Join(Dir(), "config.json") }

// Load reads the config file; a missing file yields a zero Config.
func Load() *Config {
	var c Config
	b, err := os.ReadFile(Path())
	if err == nil {
		_ = json.Unmarshal(b, &c)
	}
	c.ApplyEnv()
	return &c
}

// ApplyEnv overlays WHIS_* / standard provider env vars (env wins over file).
func (c *Config) ApplyEnv() {
	envOr := func(a, b string) string {
		if v := os.Getenv(b); v != "" {
			return v
		}
		return a
	}
	c.Keys.Anthropic = envOr(c.Keys.Anthropic, "WHIS_ANTHROPIC_KEY")
	c.Keys.OpenAI = envOr(c.Keys.OpenAI, "WHIS_OPENAI_KEY")
	c.Keys.DeepSeek = envOr(c.Keys.DeepSeek, "WHIS_DEEPSEEK_KEY")
	c.Keys.OpenRouter = envOr(c.Keys.OpenRouter, "WHIS_OPENROUTER_KEY")
	c.Keys.OllamaCloud = envOr(c.Keys.OllamaCloud, "WHIS_OLLAMA_KEY")
	if v := os.Getenv("WHIS_MODEL"); v != "" && c.Model == "" {
		c.Model = v
	}
}

// Save atomically persists the config.
func (c *Config) Save() error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	p := Path()
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// ErrNoKey is returned when a provider has no usable key.
var ErrNoKey = errors.New("no API key for provider (run `whis init`)")

// EnvKey fetches a key for a named provider, preferring env over file.
func EnvKey(provider string) string {
	switch provider {
	case "anthropic":
		return os.Getenv("WHIS_ANTHROPIC_KEY")
	case "openai":
		return os.Getenv("WHIS_OPENAI_KEY")
	case "deepseek":
		return os.Getenv("WHIS_DEEPSEEK_KEY")
	case "openrouter":
		return os.Getenv("WHIS_OPENROUTER_KEY")
	}
	return ""
}

// ResolveKey returns the API key for a provider (env first, then stored).
func (c *Config) ResolveKey(provider string) string {
	if k := EnvKey(provider); k != "" {
		return k
	}
	switch provider {
	case "anthropic":
		return c.Keys.Anthropic
	case "openai":
		return c.Keys.OpenAI
	case "deepseek":
		return c.Keys.DeepSeek
	case "openrouter":
		return c.Keys.OpenRouter
	}
	return ""
}

// AvailableModels lists stored models for UI hints, sorted by name.
func (c *Config) AvailableModels() []string {
	out := []string{"deepseek-flash", "deepseek-reasoner", "deepseek-chat"}
	if c.Keys.Anthropic != "" {
		out = append(out, "claude-sonnet-4", "claude-haiku-4")
	}
	if c.Keys.OpenAI != "" {
		out = append(out, "gpt-4.1-mini", "gpt-4.1")
	}
	if c.Keys.OpenRouter != "" {
		out = append(out, "or:anthropic/claude-sonnet-4", "or:deepseek/deepseek-chat")
	}
	sort.Strings(out)
	return out
}

// Mask returns a masked representation of a secret for UI display.
func Mask(s string) string {
	if s == "" {
		return ""
	}
	if len(s) <= 8 {
		return strings.Repeat("*", len(s))
	}
	return s[:4] + strings.Repeat("*", 8) + s[len(s)-4:]
}

// Platform is a short OS/arch label for the header ribbon.
func Platform() string { return runtime.GOOS + "/" + runtime.GOARCH }
