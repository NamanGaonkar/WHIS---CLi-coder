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
)

// anthropic implements the Messages API with SSE streaming, tool use and
// ephemeral prompt caching (cache_control) on stable prefix blocks.
type anthropic struct {
	apiKey string
	model  string
	http   *http.Client
}

// NewAnthropic builds an Anthropic Messages client.
func NewAnthropic(key, model string) *anthropic {
	return &anthropic{apiKey: key, model: model, http: &http.Client{}}
}

func (a *anthropic) Name() string  { return "anthropic" }
func (a *anthropic) Label() string { return a.model }

type aBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	// tool_use
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
	// tool_result
	ToolUseID string `json:"tool_use_id,omitempty"`
	// ephemeral cache hint
	CacheControl *struct {
		Type string `json:"type"`
	} `json:"cache_control,omitempty"`
}

type aMessage struct {
	Role    string   `json:"role"`
	Content []aBlock `json:"content"`
}

type aTool struct {
	Name         string         `json:"name"`
	Description  string         `json:"description"`
	InputSchema  map[string]any `json:"input_schema"`
	CacheControl *struct {
		Type string `json:"type"`
	} `json:"cache_control,omitempty"`
}

type aRequest struct {
	Model     string     `json:"model"`
	MaxTokens int        `json:"max_tokens"`
	System    []aBlock   `json:"system,omitempty"`
	Messages  []aMessage `json:"messages"`
	Tools     []aTool    `json:"tools,omitempty"`
	Stream    bool       `json:"stream"`
}

// toAnthropic converts neutral messages, splitting the leading system block and
// applying ephemeral cache_control to the system block and last tool.
func toAnthropic(msgs []Message) (system []aBlock, convo []aMessage) {
	conv := msgs
	if len(conv) > 0 && conv[0].Role == "system" {
		cc := struct {
			Type string `json:"type"`
		}{"ephemeral"}
		system = []aBlock{{Type: "text", Text: conv[0].Content, CacheControl: &cc}}
		conv = conv[1:]
	}
	for _, m := range conv {
		am := aMessage{Role: m.Role}
		switch m.Role {
		case "assistant":
			if m.Content != "" {
				am.Content = append(am.Content, aBlock{Type: "text", Text: m.Content})
			}
			for _, tc := range m.ToolCalls {
				am.Content = append(am.Content, aBlock{Type: "tool_use", ID: tc.ID, Name: tc.Name, Input: json.RawMessage(tc.Args)})
			}
		case "tool":
			am.Role = "user"
			am.Content = append(am.Content, aBlock{Type: "tool_result", ToolUseID: m.ToolCallID, Text: m.Content})
		default:
			am.Content = append(am.Content, aBlock{Type: "text", Text: m.Content})
		}
		if len(am.Content) == 0 {
			am.Content = append(am.Content, aBlock{Type: "text", Text: "(empty)"})
		}
		convo = append(convo, am)
	}
	return system, convo
}

func (a *anthropic) Stream(ctx context.Context, model string, msgs []Message, tools []Tool) (Stream, error) {
	if a.apiKey == "" {
		return nil, errors.New("missing Anthropic API key (run `whis init`)")
	}
	wire := model
	if wire == "" {
		wire = a.model
	}
	system, convo := toAnthropic(msgs)
	req := aRequest{Model: wire, MaxTokens: 8192, System: system, Messages: convo, Stream: true}
	for i, t := range tools {
		at := aTool{Name: t.Name, Description: t.Description, InputSchema: t.Schema}
		// ephemeral cache on the last tool: tool defs are static per session
		if i == len(tools)-1 {
			cc := struct {
				Type string `json:"type"`
			}{"ephemeral"}
			at.CacheControl = &cc
		}
		req.Tools = append(req.Tools, at)
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.anthropic.com/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", a.apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	resp, err := a.http.Do(httpReq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("anthropic: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return &aStream{resp: resp, sc: bufio.NewScanner(resp.Body)}, nil
}

type aStream struct {
	resp    *http.Response
	sc      *bufio.Scanner
	closed  bool
	usage   *Usage
	curCall *ToolCall
	argBuf  strings.Builder
}

type aEvent struct {
	Type  string `json:"type"`
	Delta struct {
		Type       string `json:"type"`
		Text       string `json:"text"`
		Thinking   string `json:"thinking"`
		StopReason string `json:"stop_reason"`
	} `json:"delta"`
	Message struct {
		Usage struct {
			InputTokens              int `json:"input_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens"`
			OutputTokens             int `json:"output_tokens"`
		} `json:"usage"`
	} `json:"message"`
	Usage struct {
		InputTokens              int `json:"input_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		OutputTokens             int `json:"output_tokens"`
	} `json:"usage"`
	ContentBlock struct {
		Type string `json:"type"`
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"content_block"`
	PartialJSON string `json:"partial_json"`
}

func (s *aStream) Next() (Delta, error) {
	for s.sc.Scan() {
		line := strings.TrimSpace(s.sc.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		var ev aEvent
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			continue
		}
		switch ev.Type {
		case "message_start":
			u := Usage{In: ev.Message.Usage.InputTokens, Out: ev.Message.Usage.OutputTokens}
			u.Cached = ev.Message.Usage.CacheReadInputTokens
			s.usage = &u
		case "content_block_start":
			if ev.ContentBlock.Type == "tool_use" {
				s.curCall = &ToolCall{ID: ev.ContentBlock.ID, Name: ev.ContentBlock.Name}
				s.argBuf.Reset()
			}
		case "content_block_delta":
			switch ev.Delta.Type {
			case "input_json_delta":
				if s.curCall != nil {
					s.argBuf.WriteString(ev.PartialJSON)
				}
			case "thinking_delta":
				if ev.Delta.Thinking != "" {
					return Delta{Reasoning: ev.Delta.Thinking}, nil
				}
			default:
				if ev.Delta.Text != "" {
					return Delta{Text: ev.Delta.Text}, nil
				}
			}
		case "content_block_stop":
			if s.curCall != nil {
				call := *s.curCall
				call.Args = json.RawMessage(s.argBuf.String())
				if len(call.Args) == 0 {
					call.Args = json.RawMessage("{}")
				}
				s.curCall = nil
				return Delta{Call: &call}, nil
			}
		case "message_delta":
			if s.usage != nil && ev.Usage.OutputTokens > 0 {
				s.usage.Out = ev.Usage.OutputTokens
				s.usage.In = ev.Usage.InputTokens
				s.usage.Cached = ev.Usage.CacheReadInputTokens
			}
		case "message_stop":
			_ = s.Close()
			if s.usage != nil {
				u := *s.usage
				return Delta{Usage: &u}, io.EOF
			}
			return Delta{}, io.EOF
		case "error":
			_ = s.Close()
			return Delta{}, fmt.Errorf("anthropic stream error: %s", payload)
		}
	}
	err := s.sc.Err()
	if err == nil {
		err = io.EOF
	}
	_ = s.Close()
	return Delta{}, err
}

func (s *aStream) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	return s.resp.Body.Close()
}
