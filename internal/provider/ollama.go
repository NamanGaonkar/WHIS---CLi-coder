package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

// ollamaClient talks to the Ollama native /api/chat endpoint (local daemon or
// ollama.com cloud). Token counts come from the final NDJSON message.
type ollamaClient struct {
	base   string
	apiKey string // cloud only
	local  bool
	model  string
	http   *http.Client
}

// NewOllama builds a client for the local daemon (default http://127.0.0.1:11434).
func NewOllama(model string) *ollamaClient {
	base := osOllamaHost()
	return &ollamaClient{base: base, local: true, model: model, http: &http.Client{}}
}

// NewOllamaCloud builds a client for ollama.com cloud models.
func NewOllamaCloud(key, model string) *ollamaClient {
	return &ollamaClient{base: "https://ollama.com", apiKey: key, model: model, http: &http.Client{}}
}

func (o *ollamaClient) Name() string {
	if o.local {
		return "ollama"
	}
	return "ollama-cloud"
}

func (o *ollamaClient) Label() string { return o.model }

type olMessage struct {
	Role      string   `json:"role"`
	Content   string   `json:"content,omitempty"`
	ToolCalls []olCall `json:"tool_calls,omitempty"`
}

type olCall struct {
	Function struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"function"`
}

type olTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Parameters  map[string]any `json:"parameters"`
	} `json:"function"`
}

type olRequest struct {
	Model    string      `json:"model"`
	Messages []olMessage `json:"messages"`
	Stream   bool        `json:"stream"`
	Tools    []olTool    `json:"tools,omitempty"`
}

type olChunk struct {
	Message struct {
		Role      string   `json:"role"`
		Content   string   `json:"content"`
		ToolCalls []olCall `json:"tool_calls"`
	} `json:"message"`
	Done            bool `json:"done"`
	PromptEvalCount int  `json:"prompt_eval_count"`
	EvalCount       int  `json:"eval_count"`
}

var olCallSeq atomic.Int64

func (o *ollamaClient) Stream(ctx context.Context, model string, msgs []Message, tools []Tool) (Stream, error) {
	wire := model
	if wire == "" {
		wire = o.model
	}
	req := olRequest{Model: wire, Stream: true}
	for _, m := range msgs {
		om := olMessage{Role: m.Role, Content: m.Content}
		if m.Role == "assistant" && len(m.ToolCalls) > 0 {
			for _, tc := range m.ToolCalls {
				var c olCall
				c.Function.Name = tc.Name
				c.Function.Arguments = json.RawMessage(tc.Args)
				om.ToolCalls = append(om.ToolCalls, c)
			}
		}
		req.Messages = append(req.Messages, om)
	}
	for _, t := range tools {
		var ot olTool
		ot.Type = "function"
		ot.Function.Name = t.Name
		ot.Function.Description = t.Description
		ot.Function.Parameters = t.Schema
		req.Tools = append(req.Tools, ot)
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, o.base+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if o.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+o.apiKey)
	}
	resp, err := o.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ollama unreachable at %s (is it running?): %w", o.base, err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("ollama: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return &olStream{resp: resp, sc: bufio.NewScanner(resp.Body)}, nil
}

type olStream struct {
	resp    *http.Response
	sc      *bufio.Scanner
	closed  bool
	usage   *Usage
	flushed bool
}

func (s *olStream) Next() (Delta, error) {
	for s.sc.Scan() {
		line := strings.TrimSpace(s.sc.Text())
		if line == "" {
			continue
		}
		var ch olChunk
		if err := json.Unmarshal([]byte(line), &ch); err != nil {
			continue
		}
		d := Delta{}
		if ch.Message.Content != "" {
			d.Text = ch.Message.Content
		}
		for _, tc := range ch.Message.ToolCalls {
			args := tc.Function.Arguments
			if len(args) == 0 {
				args = json.RawMessage("{}")
			}
			d.Call = &ToolCall{ID: fmt.Sprintf("ol_%d", olCallSeq.Add(1)), Name: tc.Function.Name, Args: args}
			break // one call per chunk in practice
		}
		if ch.Done {
			u := Usage{In: ch.PromptEvalCount, Out: ch.EvalCount}
			s.usage = &u
			if d.Text != "" || d.Call != nil {
				return d, nil // final content first; usage delivered on next call
			}
			_ = s.Close()
			if s.flushed {
				return Delta{}, io.EOF
			}
			s.flushed = true
			return Delta{Usage: &u}, io.EOF
		}
		if d.Text != "" || d.Call != nil {
			return d, nil
		}
	}
	err := s.sc.Err()
	if err == nil {
		err = io.EOF
	}
	_ = s.Close()
	return Delta{}, err
}

func (s *olStream) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	return s.resp.Body.Close()
}

// osOllamaHost resolves the local daemon base URL from OLLAMA_HOST or default.
func osOllamaHost() string {
	h := getEnv("OLLAMA_HOST", "http://127.0.0.1:11434")
	if !strings.HasPrefix(h, "http://") && !strings.HasPrefix(h, "https://") {
		h = "http://" + h
	}
	return h
}

// timeNow is a test seam.
var timeNow = time.Now
