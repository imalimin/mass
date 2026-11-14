// Package control enforces one run's limits independently of scheduler capacity.
package control

import (
	"context"
	"sync"
	"time"

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
	mu                 sync.Mutex
	deadline           time.Time
	maxCalls, maxTools int
	calls, tools       int
	maxTokens, charged int64
	items              map[string]reservation
	check              func(context.Context) error
}

func New(snapshot run.Snapshot, started time.Time, check func(context.Context) error) *Control {
	return &Control{deadline: started.Add(time.Duration(snapshot.Definition.TimeoutSeconds) * time.Second), maxCalls: snapshot.Definition.MaxSteps, maxTools: snapshot.Definition.MaxSteps * 16, maxTokens: int64(snapshot.Definition.MaxSteps) * int64(snapshot.Definition.MaxInputTokens+snapshot.Definition.OutputTokens), items: map[string]reservation{}, check: check}
}
func (c *Control) Check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !time.Now().Before(c.deadline) {
		return run.ErrBudget
	}
	if c.check != nil {
		return c.check(ctx)
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
		if c.calls >= c.maxCalls || upper > c.maxTokens-c.charged {
			return run.ErrBudget
		}
		c.calls++
		c.charged += upper
	case "tool":
		if c.tools >= c.maxTools {
			return run.ErrBudget
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
		return run.ErrBudget
	}
	return nil
}
