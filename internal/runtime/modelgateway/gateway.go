// Package modelgateway records model attempts and their structured results.
package modelgateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/journal"
	"github.com/imalimin/mass/internal/resource"
	"github.com/imalimin/mass/internal/resource/trace"
)

type Transport interface {
	Source() string
	Stream(context.Context, run.Snapshot, run.Prepared, string, func(string) error) (run.Reply, error)
}
type Credentials interface {
	ResolveRunSecret(context.Context, string, string, run.ModelConfig) ([]byte, string, int64, error)
}
type RunControl interface {
	Check(context.Context) error
	Reserve(context.Context, string, string, int64) error
	Settle(string, run.Usage) error
}
type Gateway struct {
	Journal     *journal.Journal
	Content     *resource.ContentStore
	Transport   Transport
	Credentials Credentials
	Control     RunControl
	Trace       *trace.Recorder
	Delta       func(spaceID, runID, attemptID, text string)
}
type attemptState struct {
	id       string
	retryOf  string
	recorded bool
	partial  bool
}

func (g *Gateway) Call(ctx context.Context, s run.Snapshot, p run.Prepared) (run.Reply, error) {
	previous := ""
	for ordinal := 0; ordinal < min(3, max(1, s.Definition.ModelAttempts)); ordinal++ {
		state := &attemptState{id: uuid.NewString(), retryOf: previous}
		reply, err := g.callOnce(ctx, s, p, state)
		var failure *run.ModelFailure
		if err == nil || !state.recorded || state.partial || !errors.As(err, &failure) || !failure.Retryable || ordinal+1 >= min(3, max(1, s.Definition.ModelAttempts)) {
			return reply, err
		}
		previous = state.id
		timer := time.NewTimer(time.Duration(ordinal+1) * 250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return reply, ctx.Err()
		case <-timer.C:
		}
	}
	return run.Reply{}, run.ErrProtocol
}
func (g *Gateway) callOnce(ctx context.Context, s run.Snapshot, p run.Prepared, state *attemptState) (reply run.Reply, err error) {
	if err = g.Control.Check(ctx); err != nil {
		return reply, err
	}
	purpose := p.Spec.Purpose
	if (purpose != "task" && purpose != "summary") || (purpose == "summary" && len(p.Tools) > 0) {
		return reply, run.ErrProtocol
	}
	key, baseURL, revision, err := g.Credentials.ResolveRunSecret(ctx, s.ActorID, s.SpaceID, s.Definition.Model)
	if err != nil {
		return reply, err
	}
	defer func() {
		for i := range key {
			key[i] = 0
		}
	}()
	// Resolve the URL and key together from the run's fixed model ID on every attempt.
	s.Definition.Model.Endpoint = baseURL
	attempt, message := state.id, uuid.NewString()
	if err = g.Control.Reserve(ctx, attempt, "model", int64(p.InputTokens+s.Definition.OutputTokens)); err != nil {
		return reply, err
	}
	started := journal.New(s.ActorID, "model.attempt.started", s.ID, journal.AttemptData{RetryOf: state.retryOf, ID: attempt, ProjectionEventID: p.EventID, MessageID: message, Purpose: purpose, Source: g.Transport.Source(), CredentialRevision: revision})
	started.CausationID = p.EventID
	started.OperationID = attempt
	if _, err = resource.AppendRun(ctx, g.Journal, s, started); err != nil {
		return reply, err
	}
	if g.Trace != nil {
		g.Trace.Start(s, attempt, state.retryOf, purpose, started.OccurredAt)
	}
	var text bytes.Buffer
	completed := false
	defer func() {
		if reply.Message.Content == "" {
			reply.Message.Content = text.String()
		}
		if completed && g.Trace != nil {
			g.Trace.ModelDone(s.ID, reply, "succeeded", "", time.Since(started.OccurredAt))
		}
		if err != nil && !completed {
			closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			code := "model_failed"
			if errors.Is(err, run.ErrProtocol) {
				code = "model_protocol_error"
			}
			if ctx.Err() != nil {
				code = "model_interrupted"
			}
			status := "failed"
			if ctx.Err() == context.Canceled {
				status = "cancelled"
			}
			if g.Trace != nil {
				g.Trace.ModelDone(s.ID, reply, status, code, time.Since(started.OccurredAt))
			}
			e := journal.New(s.ActorID, "model.attempt.failed", s.ID, journal.AttemptFailedData{Observation: reply.Observation, AttemptID: attempt, MessageID: message, Error: code, Usage: reply.Usage, Preview: previewText(reply.Message.Content)})
			e.OperationID = attempt
			_, saveErr := resource.AppendRun(closeCtx, g.Journal, s, e)
			err = errors.Join(err, saveErr)
			settleErr := g.Control.Settle(attempt, reply.Usage)
			state.recorded = saveErr == nil && settleErr == nil
			err = errors.Join(err, settleErr)
		}
	}()
	if err = g.Control.Check(ctx); err != nil {
		return reply, err
	}
	reply, err = g.Transport.Stream(ctx, s, p, string(key), func(delta string) error {
		if !utf8.ValidString(delta) {
			return run.ErrProtocol
		}
		if delta != "" {
			state.partial = true
			text.WriteString(delta)
			if purpose == "task" && g.Delta != nil {
				g.Delta(s.SpaceID, s.ID, attempt, delta)
			}
		}
		return nil
	})
	if err != nil {
		return reply, err
	}
	if !utf8.ValidString(reply.Message.Content) || (reply.FinishReason != "stop" && reply.FinishReason != "tool_calls") || (len(reply.Message.ToolCalls) > 0) != (reply.FinishReason == "tool_calls") || len(reply.Message.ToolCalls) > 16 {
		return reply, run.ErrProtocol
	}
	if purpose == "summary" && len(reply.Message.ToolCalls) > 0 {
		return reply, run.ErrProtocol
	}
	if reply.FinishReason == "stop" && reply.Message.Content == "" {
		return reply, run.ErrProtocol
	}
	reply.MessageID = message
	reply.Source = g.Transport.Source()
	reply.ResponseEventID = uuid.NewString()
	seen := map[string]bool{}
	for i, c := range reply.Message.ToolCalls {
		if c.ID == "" || len(c.ID) > 128 || !utf8.ValidString(c.ID) || seen[c.ID] || !json.Valid([]byte(c.Arguments)) || len(c.Arguments) > 2<<20 {
			return reply, run.ErrProtocol
		}
		seen[c.ID] = true
		reply.Message.ToolCalls[i].NativeID = c.ID
		reply.Message.ToolCalls[i].ID = uuid.NewString()
		reply.Message.ToolCalls[i].ResponseEventID = reply.ResponseEventID
	}
	raw, _ := json.Marshal(reply)
	o, err := g.Content.Put(ctx, s.ActorID, s.SpaceID, uuid.NewString(), "model.response", "application/json", bytes.NewReader(raw))
	if err != nil {
		return reply, err
	}

	// Raw arguments stay in the immutable response. The Journal carries bounded
	// display arguments and their digest, so a valid large code body fits its envelope.
	calls := append([]run.Call(nil), reply.Message.ToolCalls...)
	inlineBytes := 16 << 10
	for i := range calls {
		calls[i].ArgumentsSHA256 = journal.Digest(calls[i].Arguments)
		encoded, _ := json.Marshal(calls[i])
		if len(calls[i].Arguments) > 8192 || len(encoded) > inlineBytes {
			calls[i].Arguments = ""
		} else {
			inlineBytes -= len(encoded)
		}
	}
	e := journal.New(s.ActorID, "model.response.completed", s.ID, journal.ResponseData{Observation: reply.Observation, AttemptID: attempt, MessageID: message, ContentID: o.ID, FinishReason: reply.FinishReason, Usage: reply.Usage, Calls: calls, Preview: previewText(reply.Message.Content)}, storage.Ref{Role: "response", ObjectID: o.ID, SHA256: o.SHA256})
	e.EventID = reply.ResponseEventID
	e.OperationID = attempt
	e.CausationID = started.EventID
	if _, err = resource.AppendRun(ctx, g.Journal, s, e); err != nil {
		return reply, err
	}
	completed = true
	err = g.Control.Settle(attempt, reply.Usage)
	return reply, err
}

func previewText(text string) string {
	if len(text) <= 8192 {
		return text
	}
	text = text[:8192]
	for !utf8.ValidString(text) {
		text = text[:len(text)-1]
	}
	return text
}
