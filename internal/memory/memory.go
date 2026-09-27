// Package memory persists durable user facts across WHIS sessions
// (~/.whis/memory.json): "remember that..." -> saved forever, recalled
// in any later chat via injected digest or the memory tools.
package memory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"whis/internal/config"
)

// Entry is one remembered fact.
type Entry struct {
	ID        int       `json:"id"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"created_at"`
}

// Store is the persistent memory list (guarded, atomic writes).
type Store struct {
	mu   sync.Mutex
	path string
	list []Entry
}

// Load opens (or creates) the global memory store.
func Load() *Store {
	s := &Store{path: filepath.Join(config.Dir(), "memory.json")}
	s.reload()
	return s
}

func (s *Store) reload() {
	b, err := os.ReadFile(s.path)
	if err == nil {
		_ = json.Unmarshal(b, &s.list)
	}
}

func (s *Store) flushLocked() {
	b, err := json.MarshalIndent(s.list, "", "  ")
	if err != nil {
		return
	}
	tmp := s.path + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, s.path)
	}
}

// Save remembers a fact. Exact duplicates refresh the timestamp instead of
// piling up. Returns the stored entry.
func (s *Store) Save(text string) Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	text = strings.TrimSpace(text)
	for i := range s.list {
		if strings.EqualFold(s.list[i].Text, text) {
			s.list[i].CreatedAt = time.Now()
			s.flushLocked()
			return s.list[i]
		}
	}
	next := 1
	for _, e := range s.list {
		if e.ID >= next {
			next = e.ID + 1
		}
	}
	e := Entry{ID: next, Text: text, CreatedAt: time.Now()}
	s.list = append(s.list, e)
	s.flushLocked()
	return e
}

// Forget removes by id (or, when id<=0, by case-insensitive text match).
// Returns how many entries were removed.
func (s *Store) Forget(id int, textQuery string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	q := strings.ToLower(strings.TrimSpace(textQuery))
	kept := s.list[:0]
	removed := 0
	for _, e := range s.list {
		match := (id > 0 && e.ID == id) || (id <= 0 && q != "" && strings.Contains(strings.ToLower(e.Text), q))
		if match {
			removed++
			continue
		}
		kept = append(kept, e)
	}
	if removed > 0 {
		s.list = kept
		s.flushLocked()
	}
	return removed
}

// Search returns entries whose text contains ALL query words (any order),
// newest first. Empty query returns everything.
func (s *Store) Search(query string) []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	words := strings.Fields(strings.ToLower(query))
	out := []Entry{}
	for _, e := range s.list {
		low := strings.ToLower(e.Text)
		ok := true
		for _, w := range words {
			if !strings.Contains(low, w) {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// Digest renders numbered memories for the system prompt, newest last,
// capped to maxChars. Returns "" when the store is empty.
func (s *Store) Digest(maxChars int) string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.list) == 0 {
		return ""
	}
	var b strings.Builder
	// oldest first so the most recent memories survive the cap
	for i := len(s.list) - 1; i >= 0; i-- {
		line := fmt.Sprintf("%d. %s", s.list[i].ID, s.list[i].Text)
		if b.Len()+len(line)+1 > maxChars && b.Len() > 0 {
			break
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(line)
	}
	return b.String()
}

// Count returns the number of stored memories.
func (s *Store) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.list)
}
