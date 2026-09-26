package agent

import (
	"context"
	"fmt"
	"strings"

	"whis/internal/session"
	"whis/internal/tool"
)

// Task runs an isolated subagent loop with its own session and tool env.
// Only the final assistant summary is returned; intermediate tool noise,
// errors and traces stay in the sub-bubble and never touch the parent context.
func Task(parent *Agent, ctx context.Context, description string) (string, error) {
	sub := &Agent{
		Root: parent.Root, AutoApprove: parent.AutoApprove,
		Slug: parent.Slug, Wire: parent.Wire, Prov: parent.Prov, Prices: parent.Prices,
		Sess:    session.New(parent.Slug + "-task"),
		Tools:   tool.NewEnv(parent.Root),
		System:  parent.System + "\n\nMODE: subagent. You are running an isolated task. Work autonomously; your final message is the ONLY thing the parent session sees. Summarize outcome, files touched, and verification results tersely.",
		MaxTurn: 10,
	}
	sub.Tools.AutoApprove = parent.AutoApprove
	sub.Tools.OnSnapshot = parent.snapshot // share undo trail
	if !parent.AutoApprove {
		// subagents inherit a blanket approval: the user approved the task.
		sub.Tools.AutoApprove = true
	}

	ch, err := sub.Run(ctx, description)
	if err != nil {
		return "", err
	}
	var final string
	for ev := range ch {
		switch ev.Type {
		case "turn_done":
			final = ev.Text
		case "error":
			if final == "" {
				final = "task error: " + ev.Text
			}
		}
	}
	if strings.TrimSpace(final) == "" {
		return "", fmt.Errorf("task produced no output")
	}
	return final, nil
}
