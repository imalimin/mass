// Package modelgateway durably records attempts and wire bytes before consumption.
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
)

type Recorder interface {
	Request(context.Context, []byte) error
	Response(context.Context, int, []byte) error
}
type Transport interface {
	Source() string
	Stream(context.Context, run.Snapshot, run.Prepared, string, Recorder, func(string) error) (run.Reply, error)
}
type Credentials interface {
	ResolveRunSecret(context.Context, string, string, run.ModelConfig) ([]byte, int64, error)
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
}
type recording struct {
	g                               *Gateway
	s                               run.Snapshot
	attempt, message                string
	requestOrdinal, responseOrdinal int
}

func (r *recording) save(ctx context.Context, kind string, ordinal, status int, raw []byte) error {
	o, err := r.g.Content.Put(ctx, r.s.ActorID, r.s.SpaceID, uuid.NewString(), "model.wire", "application/octet-stream", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	e := journal.New(r.s.ActorID, kind, r.s.ID, journal.WireData{AttemptID: r.attempt, Ordinal: ordinal, Status: status, ContentID: o.ID}, storage.Ref{Role: "wire", ObjectID: o.ID, SHA256: o.SHA256})
	e.OperationID = r.attempt
	_, err = resource.AppendRun(ctx, r.g.Journal, r.s, e)
	return err
}
func (r *recording) Request(ctx context.Context, raw []byte) error {
	if err := r.save(ctx, "model.request.recorded", r.requestOrdinal, 0, raw); err != nil {
		return err
	}
	r.requestOrdinal++
	return r.g.Control.Check(ctx)
}
func (r *recording) Response(ctx context.Context, status int, raw []byte) error {
	if err := r.save(ctx, "model.response.recorded", r.responseOrdinal, status, raw); err != nil {
		return err
	}
	r.responseOrdinal++
	return nil
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
	key, revision, err := g.Credentials.ResolveRunSecret(ctx, s.ActorID, s.SpaceID, s.Definition.Model)
	if err != nil {
		return reply, err
	}
	defer func() {
		for i := range key {
			key[i] = 0
		}
	}()
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
	rec := &recording{g: g, s: s, attempt: attempt, message: message}
	completed := false
	defer func() {
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
			e := journal.New(s.ActorID, "model.attempt.failed", s.ID, journal.AttemptFailedData{Observation: reply.Observation, AttemptID: attempt, MessageID: message, Error: code, Usage: reply.Usage})
			e.OperationID = attempt
			_, saveErr := resource.AppendRun(closeCtx, g.Journal, s, e)
			err = errors.Join(err, saveErr)
			settleErr := g.Control.Settle(attempt, reply.Usage)
			state.recorded = saveErr == nil && settleErr == nil
			err = errors.Join(err, settleErr)
		}
	}()
	reply, err = g.Transport.Stream(ctx, s, p, string(key), rec, func(delta string) error {
		state.partial = true
		for len(delta) > 0 {
			n := min(len(delta), 8192)
			for n > 0 && !utf8.ValidString(delta[:n]) {
				n--
			}
			if n == 0 {
				return run.ErrProtocol
			}
			part := delta[:n]
			delta = delta[n:]
			e := journal.New(s.ActorID, "model.response.delta", s.ID, journal.DeltaData{AttemptID: attempt, MessageID: message, Text: part})
			e.OperationID = attempt
			if _, err := resource.AppendRun(ctx, g.Journal, s, e); err != nil {
				return err
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
	preview := reply.Message.Content
	if len(preview) > 8192 {
		preview = preview[:8192]
		for !utf8.ValidString(preview) {
			preview = preview[:len(preview)-1]
		}
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
	e := journal.New(s.ActorID, "model.response.completed", s.ID, journal.ResponseData{Observation: reply.Observation, AttemptID: attempt, MessageID: message, ContentID: o.ID, FinishReason: reply.FinishReason, Usage: reply.Usage, Calls: calls, Preview: preview}, storage.Ref{Role: "response", ObjectID: o.ID, SHA256: o.SHA256})
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
