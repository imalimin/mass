// Package control enforces one run's limits independently of scheduler capacity.
package control

import (
	"context"
	"sync"
	"time"

	"github.com/imalimin/mass/internal/diagnostic"
	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/journal"
)

type reservation struct {
	Kind    string
	Upper   int64
	Settled bool
	Digest  string
	Usage   run.Usage
}
type Control struct {
	DiagnosticContext  context.Context
	mu                 sync.Mutex
	started            time.Time
	deadline           time.Time
	maxCalls, maxTools int
	calls, tools       int
	maxTokens, charged int64
	items              map[string]reservation
}

func New(snapshot run.Snapshot, started time.Time) *Control {
	return &Control{started: started, deadline: started.Add(time.Duration(snapshot.Definition.TimeoutSeconds) * time.Second), maxCalls: snapshot.Definition.MaxSteps, maxTools: snapshot.Definition.MaxSteps * 16, maxTokens: int64(snapshot.Definition.MaxSteps) * int64(snapshot.Definition.MaxInputTokens+snapshot.Definition.OutputTokens), items: map[string]reservation{}}
}
func (c *Control) Check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return context.Cause(ctx)
	}
	if !time.Now().Before(c.deadline) {
		return diagnostic.Failure(ctx, "run.limit_exceeded", run.ErrBudget, "limit=execution_time elapsed_ms=%d max_ms=%d", time.Since(c.started).Milliseconds(), c.deadline.Sub(c.started).Milliseconds())
	}
	return nil
}
func (c *Control) Reserve(ctx context.Context, id, kind string, upper int64) error {
	if err := c.Check(ctx); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if old, ok := c.items[id]; ok {
		if old.Kind != kind || old.Upper != upper {
			return run.ErrState
		}
		return nil
	}
	if upper < 0 {
		return run.ErrBudget
	}
	switch kind {
	case "model":
		if c.calls >= c.maxCalls {
			return diagnostic.Failure(ctx, "run.limit_exceeded", run.ErrBudget, "limit=model_calls calls=%d max_calls=%d", c.calls, c.maxCalls)
		}
		if upper > c.maxTokens-c.charged {
			return diagnostic.Failure(ctx, "run.limit_exceeded", run.ErrBudget, "limit=tokens stage=reserve charged=%d requested=%d max_tokens=%d", c.charged, upper, c.maxTokens)
		}
		c.calls++
		c.charged += upper
	case "tool":
		if c.tools >= c.maxTools {
			return diagnostic.Failure(ctx, "run.limit_exceeded", run.ErrBudget, "limit=tool_calls calls=%d max_calls=%d", c.tools, c.maxTools)
		}
		c.tools++
	default:
		return run.ErrState
	}
	c.items[id] = reservation{Kind: kind, Upper: upper}
	return nil
}
func (c *Control) Settle(id string, u run.Usage) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	old, ok := c.items[id]
	if !ok || old.Kind != "model" {
		return run.ErrState
	}
	digest := journal.Digest(u)
	if old.Settled {
		if old.Digest != digest {
			return run.ErrState
		}
		if c.charged > c.maxTokens {
			return run.ErrBudget
		}
		return nil
	}
	if u.Input != nil && u.Output != nil {
		actual := *u.Input + *u.Output
		if *u.Input < 0 || *u.Output < 0 || actual < 0 {
			return run.ErrBudget
		}
		c.charged += actual - old.Upper
	}
	old.Settled = true
	old.Digest = digest
	old.Usage = u
	c.items[id] = old
	if c.charged > c.maxTokens {
		return diagnostic.Failure(c.DiagnosticContext, "run.limit_exceeded", run.ErrBudget, "limit=tokens stage=settle charged=%d max_tokens=%d", c.charged, c.maxTokens)
	}
	return nil
}
