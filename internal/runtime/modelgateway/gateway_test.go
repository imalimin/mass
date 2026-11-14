package modelgateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/journal"
	"github.com/imalimin/mass/internal/resource"
	"github.com/imalimin/mass/tests/fakes"
	"testing"
)

type memory struct {
	objects map[string]storage.Object
	events  []journal.Event
	fail    bool
}

func (m *memory) BeginObject(_ context.Context, _ string, o storage.Object) (storage.Object, error) {
	m.objects[o.ID] = o
	return o, nil
}
func (m *memory) ReadyObject(_ context.Context, _ string, o storage.Object) error {
	o.State = "ready"
	m.objects[o.ID] = o
	return nil
}
func (m *memory) Object(_ context.Context, _, _, id string, _ bool) (storage.Object, error) {
	return m.objects[id], nil
}
func (m *memory) AppendEvents(_ context.Context, _ string, b journal.Batch) ([]journal.Event, error) {
	if m.fail {
		return nil, errors.New("journal unavailable")
	}
	result := []journal.Event{}
	for _, p := range b.Events {
		e := journal.Event{Proposed: p, SpaceID: b.SpaceID, SessionID: b.SessionID, Sequence: int64(len(m.events) + 1)}
		m.events = append(m.events, e)
		result = append(result, e)
	}
	return result, nil
}
func (m *memory) ReadEvents(context.Context, string, string, string, int64, *int64, int) (journal.Page, error) {
	return journal.Page{}, nil
}

type secrets struct {
	resolves  int
	denyAfter int
}

func (s *secrets) ResolveRunSecret(context.Context, string, string, run.ModelConfig) ([]byte, string, int64, error) {
	s.resolves++
	if s.denyAfter > 0 && s.resolves > s.denyAfter {
		return nil, "", 0, run.ErrPermission
	}
	return []byte(fmt.Sprintf("key-%d", s.resolves)), fmt.Sprintf("https://provider-%d.example/api", s.resolves), int64(s.resolves), nil
}

type budget struct{ reserve, settle int }

func (b *budget) Check(context.Context) error                          { return nil }
func (b *budget) Reserve(context.Context, string, string, int64) error { b.reserve++; return nil }
func (b *budget) Settle(string, run.Usage) error                       { b.settle++; return nil }

type provider struct {
	endpoints []string
	keys      []string
	calls     int
	partial   bool
	repo      *memory
	failLog   bool
}

func (p *provider) Source() string { return "fake" }
func (p *provider) Stream(_ context.Context, snapshot run.Snapshot, _ run.Prepared, key string, delta func(string) error) (run.Reply, error) {
	p.calls++
	p.endpoints = append(p.endpoints, snapshot.Definition.Model.Endpoint)
	p.keys = append(p.keys, key)
	if p.calls == 1 {
		if p.partial {
			if err := delta("partial"); err != nil {
				return run.Reply{}, err
			}
		}
		if p.failLog {
			p.repo.fail = true
		}
		return run.Reply{}, &run.ModelFailure{Code: "model_http_failed", Status: 503, Retryable: true}
	}
	return run.Reply{Message: run.Message{Role: "assistant", Content: "done"}, FinishReason: "stop"}, nil
}
func TestExplicitRetrySafety(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		attempts, deny, want int
		partial, fail        bool
	}{{"default", 0, 0, 1, false, false}, {"enabled", 2, 0, 2, false, false}, {"partial", 2, 0, 1, true, false}, {"journal failure", 2, 0, 1, false, true}, {"revoked before retry", 2, 1, 1, false, false}} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &memory{objects: map[string]storage.Object{}}
			credentials := &secrets{denyAfter: tc.deny}
			control := &budget{}
			transport := &provider{partial: tc.partial, repo: repo, failLog: tc.fail}
			g := &Gateway{Journal: &journal.Journal{Repo: repo}, Content: &resource.ContentStore{Repo: repo, Blobs: fakes.NewBlobs(), Prefix: "test", MaxBytes: 1 << 20}, Transport: transport, Credentials: credentials, Control: control}
			s := run.Snapshot{ID: uuid.NewString(), ActorID: uuid.NewString(), SpaceID: uuid.NewString(), SessionID: uuid.NewString(), Definition: run.Definition{ModelAttempts: tc.attempts, OutputTokens: 10}}
			p := run.Prepared{EventID: uuid.NewString(), InputTokens: 10, Spec: run.ProjectionSpec{Purpose: "task"}}
			reply, err := g.Call(context.Background(), s, p)
			for i, endpoint := range transport.endpoints {
				if endpoint != fmt.Sprintf("https://provider-%d.example/api", i+1) || transport.keys[i] != fmt.Sprintf("key-%d", i+1) {
					t.Fatal("URL and key were not resolved together", transport.endpoints)
				}
			}
			if transport.calls != tc.want || control.reserve != tc.want || control.settle != tc.want {
				t.Fatal("attempt accounting", transport.calls, control)
			}
			if tc.name == "enabled" {
				if err != nil || reply.Message.Content != "done" {
					t.Fatal(err)
				}
				attempts := []journal.AttemptData{}
				for _, e := range repo.events {
					if e.Type == "model.attempt.started" {
						var d journal.AttemptData
						json.Unmarshal(e.Data, &d)
						attempts = append(attempts, d)
					}
				}
				if len(attempts) != 2 || attempts[1].RetryOf != attempts[0].ID || attempts[0].MessageID == attempts[1].MessageID || attempts[1].CredentialRevision != 2 {
					t.Fatal("retry provenance")
				}
			} else if err == nil {
				t.Fatal("unexpected success")
			}
		})
	}
}
