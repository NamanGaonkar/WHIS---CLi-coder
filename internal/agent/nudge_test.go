package agent

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"whis/internal/provider"
)

// fakeStream replays scripted deltas then EOF.
type fakeStream struct {
	deltas []provider.Delta
	i      int
}

func (s *fakeStream) Next() (provider.Delta, error) {
	if s.i >= len(s.deltas) {
		return provider.Delta{}, io.EOF
	}
	d := s.deltas[s.i]
	s.i++
	return d, nil
}
func (s *fakeStream) Close() error { return nil }

// fakeProv hands out one scripted turn per Stream call and records the
// messages it was given (so tests can assert the nudge was injected).
type fakeProv struct {
	turns [][]provider.Delta
	n     int
	got   [][]provider.Message
}

func (p *fakeProv) Name() string  { return "fake" }
func (p *fakeProv) Label() string { return "fake-1b" }
func (p *fakeProv) Stream(_ context.Context, _ string, msgs []provider.Message, _ []provider.Tool) (provider.Stream, error) {
	p.got = append(p.got, msgs)
	if p.n >= len(p.turns) {
		return nil, errors.New("script exhausted")
	}
	t := p.turns[p.n]
	p.n++
	return &fakeStream{deltas: t}, nil
}

func newTestAgent(t *testing.T, p provider.Provider) *Agent {
	t.Helper()
	a := NewUnbound(t.TempDir(), true)
	a.Prov = p
	a.Wire = "fake"
	a.Slug = "fake"
	a.System = "test system"
	a.MaxTurn = 6
	return a
}

func collect(t *testing.T, ch <-chan Event) []Event {
	t.Helper()
	var evs []Event
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return evs
			}
			evs = append(evs, ev)
		case <-time.After(30 * time.Second):
			t.Fatal("agent loop hung")
		}
	}
}

// A small local model that replies with nothing must be nudged and then
// recover — the run ends with the model's real text, not a silent stall.
func TestEmptyReplyNudgeRecovers(t *testing.T) {
	p := &fakeProv{turns: [][]provider.Delta{
		{},                 // empty reply 1
		{},                 // empty reply 2
		{{Text: "hello!"}}, // nudged model finally answers
	}}
	a := newTestAgent(t, p)
	evs := collect(t, mustRun(t, a, "hi"))

	nudges := 0
	var done Event
	for _, ev := range evs {
		if ev.Type == "notice" && strings.Contains(ev.Text, "nudging (1/2)") {
			nudges++
		}
		if ev.Type == "notice" && strings.Contains(ev.Text, "nudging (2/2)") {
			nudges++
		}
		if ev.Type == "turn_done" {
			done = ev
		}
		if ev.Type == "error" {
			t.Fatalf("unexpected error event: %s", ev.Text)
		}
	}
	if nudges != 2 {
		t.Fatalf("expected 2 nudge notices, got %d", nudges)
	}
	if done.Text != "hello!" {
		t.Fatalf("run did not finish with the recovered text: %q", done.Text)
	}
	if a.pendingNudge != "" {
		t.Fatal("pendingNudge not cleared after use")
	}
	// the nudge must ride along as a one-shot user message
	if cnt := countNudges(p.got[1]); cnt != 1 {
		t.Fatalf("expected exactly 1 nudge message on retry, got %d", cnt)
	}
	if cnt := countNudges(p.got[2]); cnt != 1 {
		t.Fatalf("nudge must be one-shot (old one cleared), got %d", cnt)
	}
}

// Three empty replies in a row must fail LOUDLY with guidance instead of
// the silent "done with no reply" stall users saw on 1B models.
func TestEmptyReplyFailsLoudly(t *testing.T) {
	p := &fakeProv{turns: [][]provider.Delta{{}, {}, {}, {}}}
	a := newTestAgent(t, p)
	evs := collect(t, mustRun(t, a, "hi"))

	var errText string
	for _, ev := range evs {
		if ev.Type == "error" {
			errText = ev.Text
		}
	}
	if !strings.Contains(errText, "three empty replies") {
		t.Fatalf("expected loud failure, got error=%q", errText)
	}
	if p.n != 3 {
		t.Fatalf("loop should stop after 3 empty replies, made %d calls", p.n)
	}
}

func mustRun(t *testing.T, a *Agent, prompt string) <-chan Event {
	t.Helper()
	ch, err := a.Run(context.Background(), prompt)
	if err != nil {
		t.Fatal(err)
	}
	return ch
}

func countNudges(msgs []provider.Message) int {
	n := 0
	for _, m := range msgs {
		if strings.HasPrefix(m.Content, "[whis]") {
			n++
		}
	}
	return n
}
