// Package session persists resumable conversation snapshots in ~/.whis/sessions.
package session

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"whis/internal/config"
)

// ToolCall records an assistant tool invocation in snapshots.
type ToolCall struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Args string `json:"args"`
}

// Msg is a provider-agnostic conversation message.
type Msg struct {
	Role       string     `json:"role"` // system | user | assistant | tool
	Content    string     `json:"content"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	Reasoning  string     `json:"reasoning,omitempty"` // plan / thinking trace
	At         time.Time  `json:"at,omitempty"`
}

// Turn records per-turn telemetry.
type Turn struct {
	Model      string    `json:"model"`
	In         int       `json:"in"`
	Cached     int       `json:"cached"`
	Out        int       `json:"out"`
	Cost       float64   `json:"cost"`
	DurationMS int64     `json:"duration_ms"`
	At         time.Time `json:"at"`
}

// Session is the persisted state of one WHIS conversation.
type Session struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Model     string    `json:"model"`
	Title     string    `json:"title"`
	Root      string    `json:"root,omitempty"` // workspace folder this session belongs to
	Messages  []Msg     `json:"messages"`
	Turns     []Turn    `json:"turns,omitempty"`
}

// Summary is a list-entry for pickers.
type Summary struct {
	ID    string
	Title string
	Model string
}

// ListFor returns sessions belonging to a workspace root, newest first.
// Sessions without a Root (legacy) are only shown when root is empty.
func ListFor(root string) []Summary {
	ids := List()
	out := []Summary{}
	for _, id := range ids {
		s, err := Load(id)
		if err != nil {
			continue
		}
		if root != "" {
			if s.Root != "" && s.Root != root {
				continue
			}
		} else if s.Root != "" {
			continue
		}
		out = append(out, Summary{ID: s.ID, Title: s.Title, Model: s.Model})
	}
	return out
}

// Dir returns ~/.whis/sessions (created if missing).
func Dir() string {
	d := filepath.Join(config.Dir(), "sessions")
	_ = os.MkdirAll(d, 0o755)
	return d
}

// New creates a session with an id like 20260926-153012.
func New(model string) *Session {
	id := time.Now().Format("20060102-150405")
	return &Session{
		ID:        id,
		Title:     id,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Model:     model,
		Messages:  []Msg{},
	}
}

// NewForRoot creates a session tagged with its workspace folder.
func NewForRoot(model, root string) *Session {
	s := New(model)
	s.Root = root
	return s
}

// SetTitle assigns a human-readable title and persists.
func (s *Session) SetTitle(t string) {
	s.Title = t
	s.Save()
}

// Path is the JSON file for this session.
func (s *Session) Path() string { return filepath.Join(Dir(), s.ID+".json") }

// Save atomically writes the snapshot.
func (s *Session) Save() {
	s.UpdatedAt = time.Now()
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return
	}
	p := s.Path()
	tmp := p + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, p)
	}
}

// Append adds a message and persists.
func (s *Session) Append(m Msg) {
	m.At = time.Now()
	s.Messages = append(s.Messages, m)
	s.Save()
}

// AddTurn records telemetry for a completed model turn.
func (s *Session) AddTurn(t Turn) {
	s.Model = t.Model
	s.Turns = append(s.Turns, t)
	s.Save()
}

// List returns saved session ids, newest first.
func List() []string {
	ents, err := os.ReadDir(Dir())
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		name := e.Name()
		if !e.IsDir() && strings.HasSuffix(name, ".json") {
			out = append(out, strings.TrimSuffix(name, ".json"))
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(out)))
	return out
}

// Load restores a session by id.
func Load(id string) (*Session, error) {
	if id == "" {
		return nil, errors.New("empty session id")
	}
	if !strings.HasSuffix(id, ".json") {
		id += ".json"
	}
	b, err := os.ReadFile(filepath.Join(Dir(), id))
	if err != nil {
		return nil, err
	}
	var s Session
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	if s.ID == "" {
		s.ID = strings.TrimSuffix(filepath.Base(id), ".json")
	}
	return &s, nil
}

// Latest returns the most recent session, or nil.
func Latest() *Session {
	ids := List()
	if len(ids) == 0 {
		return nil
	}
	s, err := Load(ids[0])
	if err != nil {
		return nil
	}
	return s
}

// Totals sums telemetry across turns.
func (s *Session) Totals() (in, cached, out int, cost float64) {
	for _, t := range s.Turns {
		in += t.In
		cached += t.Cached
		out += t.Out
		cost += t.Cost
	}
	return
}
