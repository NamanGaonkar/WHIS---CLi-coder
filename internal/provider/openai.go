package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"whis/internal/config"
)

// openaiCompatible covers OpenAI, DeepSeek and OpenRouter chat-completions APIs
// (all implement the /chat/completions SSE dialect).
type openaiCompatible struct {
	name   string
	apiKey string
	base   string // e.g. https://api.deepseek.com
	model  string
	http   *http.Client
}

// NewOpenAI builds a plain OpenAI client.
func NewOpenAI(key, model string) *openaiCompatible {
	return &openaiCompatible{name: "openai", apiKey: key, base: "https://api.openai.com/v1", model: model, http: &http.Client{}}
}

// NewDeepSeek builds a DeepSeek client (prefix caching: keep system+tools stable).
func NewDeepSeek(key, model string) *openaiCompatible {
	return &openaiCompatible{name: "deepseek", apiKey: key, base: "https://api.deepseek.com", model: model, http: &http.Client{}}
}

// NewOpenRouter builds an OpenRouter client.
func NewOpenRouter(key, model string) *openaiCompatible {
	return &openaiCompatible{name: "openrouter", apiKey: key, base: "https://openrouter.ai/api/v1", model: model, http: &http.Client{}}
}

// sanitizeKey neutralizes paste artifacts that make providers reject a
// perfectly good key: surrounding quotes, trailing newlines/spaces and
// zero-width characters dragged along by terminal/clipboard copies.
// Valid keys pass through byte-identical.
func sanitizeKey(k string) string {
	k = strings.TrimSpace(k)
	k = strings.Trim(k, "\"'“”‘’")
	k = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7F || (r >= 0x200B && r <= 0x200F) || r == 0xFEFF {
			return -1
		}
		return r
	}, k)
	return strings.TrimSpace(k)
}

// OpenAI-compatible vendors (2026-09 verified endpoints). All speak the
// standard /chat/completions SSE dialect; key form differs per vendor:
//   - gemini: API key works as a bare bearer token on the OpenAI-compat
//     endpoint (generativelanguage.googleapis.com/v1beta/openai)
//   - moonshot: international keys -> api.moonshot.ai, mainland -> .cn
//     (mirror constructor below; keys are NOT interchangeable)
func NewGemini(key, model string) *openaiCompatible {
	return &openaiCompatible{name: "gemini", apiKey: key, base: "https://generativelanguage.googleapis.com/v1beta/openai", model: model, http: &http.Client{}}
}

func NewXAI(key, model string) *openaiCompatible {
	return &openaiCompatible{name: "xai", apiKey: key, base: "https://api.x.ai/v1", model: model, http: &http.Client{}}
}

func NewMistral(key, model string) *openaiCompatible {
	return &openaiCompatible{name: "mistral", apiKey: key, base: "https://api.mistral.ai/v1", model: model, http: &http.Client{}}
}

func NewMoonshot(key, model string) *openaiCompatible {
	return &openaiCompatible{name: "moonshot", apiKey: key, base: "https://api.moonshot.ai/v1", model: model, http: &http.Client{}}
}

func NewQwen(key, model string) *openaiCompatible {
	return &openaiCompatible{name: "qwen", apiKey: key, base: "https://dashscope-intl.aliyuncs.com/compatible-mode/v1", model: model, http: &http.Client{}}
}

func NewZai(key, model string) *openaiCompatible {
	return &openaiCompatible{name: "zai", apiKey: key, base: "https://api.z.ai/api/paas/v4", model: model, http: &http.Client{}}
}

func NewMiniMax(key, model string) *openaiCompatible {
	return &openaiCompatible{name: "minimax", apiKey: key, base: "https://api.minimax.io/v1", model: model, http: &http.Client{}}
}

func NewGroq(key, model string) *openaiCompatible {
	return &openaiCompatible{name: "groq", apiKey: key, base: "https://api.groq.com/openai/v1", model: model, http: &http.Client{}}
}

func (o *openaiCompatible) Name() string  { return o.name }
func (o *openaiCompatible) Label() string { return o.model }

type oaMessage struct {
	Role       string   `json:"role"`
	Content    any      `json:"content,omitempty"`
	ToolCallID string   `json:"tool_call_id,omitempty"`
	ToolCalls  []oaCall `json:"tool_calls,omitempty"`
}

type oaCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type oaTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Parameters  map[string]any `json:"parameters"`
	} `json:"function"`
}

func toOAMessages(msgs []Message) []oaMessage {
	out := make([]oaMessage, 0, len(msgs))
	for _, m := range msgs {
		om := oaMessage{Role: m.Role, ToolCallID: m.ToolCallID}
		if m.Content != "" {
			om.Content = m.Content
		}
		if m.Role == "assistant" && len(m.ToolCalls) > 0 {
			for _, tc := range m.ToolCalls {
				var c oaCall
				c.ID = tc.ID
				c.Type = "function"
				c.Function.Name = tc.Name
				c.Function.Arguments = string(tc.Args)
				om.ToolCalls = append(om.ToolCalls, c)
			}
		}
		out = append(out, om)
	}
	return out
}

func toOATools(tools []Tool) []oaTool {
	out := make([]oaTool, 0, len(tools))
	for _, t := range tools {
		var ot oaTool
		ot.Type = "function"
		ot.Function.Name = t.Name
		ot.Function.Description = t.Description
		ot.Function.Parameters = t.Schema
		out = append(out, ot)
	}
	return out
}

type oaStreamRequest struct {
	Model     string      `json:"model"`
	Messages  []oaMessage `json:"messages"`
	Tools     []oaTool    `json:"tools,omitempty"`
	Stream    bool        `json:"stream"`
	StreamOpt *struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options,omitempty"`
	// DeepSeek V4 reasoning/effort control: keeps billed output lean by
	// capping thinking budget per run kind (reasoning tokens bill as output).
	Thinking *oaThinking `json:"thinking,omitempty"`
	Effort   string      `json:"reasoning_effort,omitempty"`
	// OpenAI-style output cap. `max_tokens` is the universally-known field
	// across OpenAI-compatible APIs; Google's Gemini compat layer REJECTS
	// unknown root fields (max_output_tokens → "Cannot find field" 400).
	MaxOutputTokens int `json:"max_tokens,omitempty"`
}

// oaThinking toggles DeepSeek reasoning; Effort picks the level.
type oaThinking struct {
	Type string `json:"type"`
}

func (o *openaiCompatible) Stream(ctx context.Context, model string, msgs []Message, tools []Tool) (Stream, error) {
	if o.apiKey == "" {
		return nil, errors.New("missing API key for " + o.name + " (run `whis init`)")
	}
	wire := model
	if wire == "" {
		wire = o.model
	}
	req := oaStreamRequest{Model: wire, Messages: toOAMessages(msgs), Stream: true}
	if len(tools) > 0 {
		req.Tools = toOATools(tools)
	}
	req.StreamOpt = &struct {
		IncludeUsage bool `json:"include_usage"`
	}{IncludeUsage: true}
	// Token-consumption control: cap billed output and reasoning effort.
	// Tools-only turns (the model is just picking the next action) do not
	// need deep thinking; final-answer turns may still think when the model
	// supports it. Unknown extra fields are ignored by lenient servers.
	req.MaxOutputTokens = 4096
	if strings.Contains(wire, "deepseek") {
		req.Thinking = &oaThinking{Type: "enabled"}
		req.Effort = "low"
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, o.base+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+sanitizeKey(o.apiKey))
	if o.name == "gemini" {
		// Google honors x-goog-api-key natively; sending both keeps auth
		// working whichever layer handles the request.
		httpReq.Header.Set("x-goog-api-key", sanitizeKey(o.apiKey))
	}
	if o.name == "openrouter" {
		httpReq.Header.Set("HTTP-Referer", "https://github.com/whis-cli/whis")
		httpReq.Header.Set("X-Title", "WHIS")
	}
	resp, err := o.http.Do(httpReq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		msg := strings.TrimSpace(string(b))
		// turn auth failures into actionable, evidence-based hints for every
		// key-based provider (openrouter/openai/mistral/groq/... share this
		// client): show WHAT was actually attached so "Missing Authentication
		// header" from a garbage/empty stored key is instantly diagnosable.
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			sent := sanitizeKey(o.apiKey)
			switch {
			case sent == "":
				msg = "whis attached NO usable key (the stored key is empty) — set it via / → provider → " + o.name
			case len(sent) < 16:
				msg = fmt.Sprintf("whis attached key %s which looks truncated/corrupt — re-enter the full key via / → provider → edit key", config.Mask(sent))
			default:
				msg = msg + " — whis attached key " + config.Mask(sent) + " and the provider rejected it: re-enter it via / → provider → edit key (or generate a new one)"
			}
		}
		return nil, fmt.Errorf("%s: HTTP %d: %s", o.name, resp.StatusCode, msg)
	}
	return &oaStream{resp: resp, sc: bufio.NewScanner(resp.Body)}, nil
}

type oaStream struct {
	resp   *http.Response
	sc     *bufio.Scanner
	pend   map[int]*ToolCall // index -> assembling tool call
	usage  *Usage
	closed bool
}

type oaChunk struct {
	Choices []struct {
		Delta struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			ToolCalls        []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		// DeepSeek style: cache hits reported at top level; hit+miss = prompt
		PromptCacheHitTokens  int `json:"prompt_cache_hit_tokens"`
		PromptCacheMissTokens int `json:"prompt_cache_miss_tokens"`
		// OpenAI style: nested details
		PromptTokensDetails *struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
}

func (s *oaStream) Next() (Delta, error) {
	for s.sc.Scan() {
		line := strings.TrimSpace(s.sc.Text())
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			_ = s.Close()
			return Delta{}, io.EOF
		}
		var ch oaChunk
		if err := json.Unmarshal([]byte(payload), &ch); err != nil {
			continue
		}
		if ch.Usage != nil {
			u := Usage{In: ch.Usage.PromptTokens, Out: ch.Usage.CompletionTokens}
			// DeepSeek reports cache hits at top level (hit + miss = prompt);
			// OpenAI reports them nested in prompt_tokens_details.
			if ch.Usage.PromptCacheHitTokens > 0 {
				u.Cached = ch.Usage.PromptCacheHitTokens
				if ch.Usage.PromptCacheMissTokens > 0 &&
					ch.Usage.PromptCacheHitTokens+ch.Usage.PromptCacheMissTokens != u.In {
					u.In = ch.Usage.PromptCacheHitTokens + ch.Usage.PromptCacheMissTokens
				}
			} else if ch.Usage.PromptTokensDetails != nil {
				u.Cached = ch.Usage.PromptTokensDetails.CachedTokens
			}
			s.usage = &u
		}
		for _, c := range ch.Choices {
			d := Delta{}
			if c.Delta.Content != "" {
				d.Text = c.Delta.Content
			}
			if c.Delta.ReasoningContent != "" {
				d.Reasoning = c.Delta.ReasoningContent
			}
			for _, tc := range c.Delta.ToolCalls {
				if s.pend == nil {
					s.pend = map[int]*ToolCall{}
				}
				p := s.pend[tc.Index]
				if p == nil {
					p = &ToolCall{}
					s.pend[tc.Index] = p
				}
				if tc.ID != "" {
					p.ID = tc.ID
				}
				if tc.Function.Name != "" {
					p.Name = tc.Function.Name
				}
				if tc.Function.Arguments != "" {
					p.Args = append(p.Args, tc.Function.Arguments...)
				}
			}
			if c.FinishReason != nil && len(s.pend) > 0 {
				// flush assembled tool calls one at a time
				for idx, p := range s.pend {
					cp := *p
					delete(s.pend, idx)
					d.Call = &cp
					return d, nil
				}
			}
			if d.Text != "" || d.Reasoning != "" {
				return d, nil
			}
		}
	}
	err := s.sc.Err()
	if err == nil {
		err = io.EOF
	}
	_ = s.Close()
	return Delta{}, err
}

func (s *oaStream) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	return s.resp.Body.Close()
}
