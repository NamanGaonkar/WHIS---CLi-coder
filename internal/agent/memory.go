package agent

import (
	"whis/internal/memory"
	"whis/internal/tool"
)

// memAdapter adapts *memory.Store to tool.MemoryStore (keeps the tool
// package free of the memory dependency).
type memAdapter struct{ s *memory.Store }

func (m memAdapter) Save(text string) tool.MemHit {
	e := m.s.Save(text)
	return tool.MemHit{ID: e.ID, Text: e.Text}
}

func (m memAdapter) Forget(id int, q string) int { return m.s.Forget(id, q) }

func (m memAdapter) Search(q string) []tool.MemHit {
	ents := m.s.Search(q)
	out := make([]tool.MemHit, 0, len(ents))
	for _, e := range ents {
		out = append(out, tool.MemHit{ID: e.ID, Text: e.Text})
	}
	return out
}

func (m memAdapter) Count() int { return m.s.Count() }

// attachMemory wires the global memory store into the agent's tool env and
// bakes the digest into the cached system prompt (so remembered facts are
// "known" in every session without a tool call).
func (a *Agent) attachMemory() {
	store := memory.Load()
	a.Mem = store
	a.Tools.Mem = memAdapter{store}
	a.rebuildSystem()
}

// RebuildSystemFromMemory re-derives the system prompt after memories
// changed via /memory (keeps the cached prefix in sync).
func (a *Agent) RebuildSystemFromMemory() { a.rebuildSystem() }
