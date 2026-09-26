// Package provider defines the model provider interface and shared streaming types.
package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Message is a conversation message in provider-neutral form.
type Message struct {
	Role       string // system | user | assistant | tool
	Content    string
	ToolCallID string     // for role==tool
	ToolCalls  []ToolCall // for role==assistant
	Cacheable  bool       // hint: mark with cache_control (Anthropic) / stable prefix (DeepSeek)
	Reasoning  string     // plan/thinking trace (DeepSeek reasoning_content)
}

// ToolCall is an assistant tool invocation.
type ToolCall struct {
	ID   string
	Name string
	Args json.RawMessage
}

// Tool is a tool definition exposed to the model.
type Tool struct {
	Name        string
	Description string
	Schema      map[string]any
}

// Usage is token accounting for one turn.
type Usage struct {
	In, Cached, Out int
}

// Delta is one streamed chunk.
type Delta struct {
	// Text carries assistant prose.
	Text string
	// Reasoning carries plan/thinking traces.
	Reasoning string
	// Call is a fully-assembled tool call (emitted once, at chunk end).
	Call  *ToolCall
	Usage *Usage
}

// Stream is the event stream for one completion.
type Stream interface {
	Next() (Delta, error) // returns io.EOF when done
	Close() error
}

// Provider is a chat-completion backend.
type Provider interface {
	Name() string  // e.g. "anthropic"
	Label() string // e.g. "claude-sonnet-4"
	Stream(ctx context.Context, model string, msgs []Message, tools []Tool) (Stream, error)
}

// Prices are USD per million tokens (in, cached-in, out). CacheWrite is an
// additive surcharge on input tokens for Anthropic-style ephemeral caching.
// Context is the known context window in tokens (0 = default guess).
type Prices struct {
	In, Cached, Out, CacheWrite float64
	Context                     int
}

// Cost computes USD for a turn.
func (p Prices) Cost(u Usage) float64 {
	uncached := u.In - u.Cached
	if uncached < 0 {
		uncached = 0
	}
	return (float64(uncached)*p.In + float64(u.Cached)*p.Cached + float64(u.Out)*p.Out) / 1e6
}

// Registry maps user-facing model slugs to (provider, wire-model, prices).
type Registry struct {
	Keys map[string]string // provider -> api key (already resolved)
}

type entry struct {
	provider string
	model    string
	prices   Prices
}

// Static catalog: curated fallbacks used when live /models fetch is
// unavailable. Live fetch always wins at runtime (see catalog.go).
var catalog = map[string]entry{
	// DeepSeek — v4 generation; the old deepseek-chat / deepseek-reasoner
	// aliases were retired from the API on 2026-07-24 and must not be used.
	"deepseek-v4-pro":   {"deepseek", "deepseek-v4-pro", Prices{In: 0.55, Cached: 0.14, Out: 2.19, Context: 1_000_000}},
	"deepseek-v4-flash": {"deepseek", "deepseek-v4-flash", Prices{In: 0.28, Cached: 0.028, Out: 0.42, Context: 128 * 1024}},

	// Anthropic — Claude generation.
	"claude-opus-5-5":  {"anthropic", "claude-opus-5-5", Prices{In: 5, Cached: 0.50, Out: 25, CacheWrite: 6.25, Context: 200_000}},
	"claude-sonnet-5":  {"anthropic", "claude-sonnet-5", Prices{In: 3, Cached: 0.30, Out: 15, CacheWrite: 3.75, Context: 200_000}},
	"claude-haiku-4-5": {"anthropic", "claude-haiku-4-5-20251001", Prices{In: 1, Cached: 0.10, Out: 5, CacheWrite: 1.25, Context: 200_000}},
	"claude-fable-5-1": {"anthropic", "claude-fable-5-1", Prices{In: 12, Cached: 1.20, Out: 60, CacheWrite: 15, Context: 200_000}},

	// OpenAI — GPT-6 generation; gpt-4o/3.5 are legacy, never default to them.
	"gpt-6-astra": {"openai", "gpt-6-astra", Prices{In: 2.50, Cached: 0.63, Out: 10, Context: 400_000}},
	"gpt-6-sol":   {"openai", "gpt-6-sol", Prices{In: 1.25, Cached: 0.31, Out: 5, Context: 400_000}},
	"gpt-6-luna":  {"openai", "gpt-6-luna", Prices{In: 0.25, Cached: 0.06, Out: 1, Context: 400_000}},
}

// SlugFor maps a wire model name back to the catalog slug when known
// (used when resuming sessions started with -m wire-name).
func SlugFor(wire string) string {
	for slug, e := range catalog {
		if e.model == wire && slug == wire {
			return wire
		}
	}
	for _, prefix := range []string{"or:", "ollama:", "ollama-cloud:"} {
		if strings.HasPrefix(wire, prefix) {
			return wire
		}
	}
	return wire
}

// Get resolves a slug to provider + wire model + prices.
func Get(slug string) (prov, wire string, pr Prices, err error) {
	if e, ok := catalog[slug]; ok {
		return e.provider, e.model, e.prices, nil
	}
	switch {
	case strings.HasPrefix(slug, "or:"):
		return "openrouter", strings.TrimPrefix(slug, "or:"), Prices{Context: 128 * 1024}, nil
	case strings.HasPrefix(slug, "ollama-cloud:"):
		return "ollama-cloud", strings.TrimPrefix(slug, "ollama-cloud:"), Prices{}, nil
	case strings.HasPrefix(slug, "ollama:"):
		return "ollama", strings.TrimPrefix(slug, "ollama:"), Prices{}, nil
	case strings.HasPrefix(slug, "deepseek:"), strings.HasPrefix(slug, "anthropic:"), strings.HasPrefix(slug, "openai:"):
		// live-fetched wire id: provider:name
		i := strings.IndexByte(slug, ':')
		return slug[:i], slug[i+1:], Prices{}, nil
	}
	return "", "", Prices{}, fmt.Errorf("unknown model %q (try /model <slug>)", slug)
}

// Exists reports whether a slug is known or has a recognised prefix.
func Exists(slug string) bool {
	if _, ok := catalog[slug]; ok {
		return true
	}
	for _, p := range []string{"or:", "ollama:", "ollama-cloud:", "deepseek:", "anthropic:", "openai:"} {
		if strings.HasPrefix(slug, p) {
			return true
		}
	}
	return false
}

// NeedsKey reports whether a provider requires an API key.
func NeedsKey(prov string) bool {
	return prov != "ollama"
}

// KnownSlugs lists catalog slugs for pickers (sorted).
func KnownSlugs() []string {
	out := make([]string, 0, len(catalog))
	for s := range catalog {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// Suggestions returns common slugs for the /model picker and error hints.
func Suggestions() []string {
	return []string{"deepseek-v4-pro", "deepseek-v4-flash", "claude-opus-5-5", "claude-sonnet-5",
		"gpt-6-astra", "gpt-6-luna", "or:<vendor/model>", "ollama:<name>", "ollama-cloud:<name>"}
}

// ProviderInfo describes one selectable provider in the TUI menus.
type ProviderInfo struct {
	Name     string // "ollama", "ollama-cloud", "deepseek", "anthropic", "openai", "openrouter"
	Label    string // display name
	NeedsKey bool
}

// Providers lists providers in picker order (local first).
func Providers() []ProviderInfo {
	return []ProviderInfo{
		{"ollama", "Ollama (local)", false},
		{"ollama-cloud", "Ollama Cloud", true},
		{"deepseek", "DeepSeek", true},
		{"anthropic", "Anthropic", true},
		{"openai", "OpenAI", true},
		{"openrouter", "OpenRouter", true},
	}
}

// ModelsForProvider returns catalog slugs for a cloud provider.
// These are fallbacks; the live /models fetch (catalog.go) always wins when
// a key is available, so fresh model ids appear without shipping a release.
func ModelsForProvider(prov string) []string {
	var out []string
	for slug, e := range catalog {
		if e.provider == prov {
			out = append(out, slug)
		}
	}
	sort.Strings(out)
	return out
}
