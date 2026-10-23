package control

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/imalimin/mass/internal/domain/run"
)

func TestReservationsAndUnknownUsage(t *testing.T) {
	s := run.Snapshot{Definition: run.Definition{MaxSteps: 2, MaxInputTokens: 100, OutputTokens: 10, TimeoutSeconds: 60}}
	c := New(s, time.Now(), nil, nil)
	ctx := context.Background()
	if err := c.Reserve(ctx, "a", "model", 110); err != nil {
		t.Fatal(err)
	}
	if err := c.Reserve(ctx, "a", "model", 110); err != nil {
		t.Fatal("retry consumed capacity", err)
	}
	if !errors.Is(c.Reserve(ctx, "a", "model", 100), run.ErrState) {
		t.Fatal("changed reservation accepted")
	}
	if err := c.Settle("a", run.Usage{}); err != nil {
		t.Fatal(err)
	}
	if c.charged != 110 {
		t.Fatal("unknown usage treated as zero")
	}
	zero := int64(0)
	if !errors.Is(c.Settle("a", run.Usage{Input: &zero, Output: &zero}), run.ErrState) {
		t.Fatal("changed settlement accepted")
	}
	if err := c.Reserve(ctx, "b", "model", 110); err != nil {
		t.Fatal(err)
	}
	if err := c.Settle("b", run.Usage{Input: &zero, Output: &zero}); err != nil {
		t.Fatal(err)
	}
	if c.charged != 110 {
		t.Fatal("known zero not refunded")
	}
	if err := c.Settle("b", run.Usage{Input: &zero, Output: &zero}); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(c.Reserve(ctx, "c", "model", 1), run.ErrBudget) {
		t.Fatal("call limit exceeded")
	}
	expired := New(s, time.Now().Add(-time.Hour), nil, nil)
	if !errors.Is(expired.Check(ctx), run.ErrBudget) {
		t.Fatal("deadline ignored")
	}
}

func TestActualUsageExceedingBudgetRemainsSettled(t *testing.T) {
	s := run.Snapshot{Definition: run.Definition{MaxSteps: 1, MaxInputTokens: 100, OutputTokens: 10, TimeoutSeconds: 60}}
	c := New(s, time.Now(), nil, nil)
	if err := c.Reserve(context.Background(), "attempt", "model", 110); err != nil {
		t.Fatal(err)
	}
	input, output := int64(100), int64(20)
	usage := run.Usage{Input: &input, Output: &output}
	for i := 0; i < 2; i++ {
		if !errors.Is(c.Settle("attempt", usage), run.ErrBudget) {
			t.Fatal("over-budget settlement accepted")
		}
	}
	if c.charged != 120 {
		t.Fatal("settled twice", c.charged)
	}
}
