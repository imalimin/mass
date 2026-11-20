package evaluation

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/imalimin/mass/internal/app/identity"
	sessionapp "github.com/imalimin/mass/internal/app/session"
	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/journal"
)

type memoryRepository struct {
	mu          sync.Mutex
	dataset     Dataset
	experiments map[string]Experiment
}

func newMemoryRepository() *memoryRepository {
	return &memoryRepository{dataset: Dataset{ID: storage.NewShortID(), Version: 1, Name: "inputs", Cases: []Case{{Turns: []Turn{{Content: []Part{{Type: "text", Text: "first"}}}, {Content: []Part{{Type: "text", Text: "second"}}}}}, {Turns: []Turn{{Content: []Part{{Type: "text", Text: "failure"}}}, {Content: []Part{{Type: "text", Text: "must not send"}}}}}}}, experiments: map[string]Experiment{}}
}
func (r *memoryRepository) SaveEvaluationDataset(context.Context, string, string, string, DatasetInput) (Dataset, error) {
	panic("unused")
}
func (r *memoryRepository) EvaluationDataset(context.Context, string, string, string, int64) (Dataset, error) {
	return r.dataset, nil
}
func (r *memoryRepository) EvaluationDatasets(context.Context, string, string, identity.Query) ([]Dataset, error) {
	panic("unused")
}
func (r *memoryRepository) FindEvaluationRequest(_ context.Context, actor, space, key, digest string) (Experiment, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, v := range r.experiments {
		if v.Key == key {
			if v.Digest != digest {
				return v, false, identity.ErrConflict
			}
			return v, true, nil
		}
	}
	return Experiment{}, false, nil
}
func (r *memoryRepository) CreateEvaluationExperiment(_ context.Context, v Experiment) (Experiment, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v.DatasetVersion = r.dataset.Version
	v.CaseCount = len(r.dataset.Cases)
	r.experiments[v.ID] = v
	return v, nil
}
func (r *memoryRepository) EvaluationExperiment(_ context.Context, _, _, id string) (Experiment, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.experiments[id]
	if !ok {
		return v, identity.ErrNotFound
	}
	return v, nil
}
func (r *memoryRepository) EvaluationExperiments(context.Context, string, string, identity.Query) ([]Experiment, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	items := make([]Experiment, 0, len(r.experiments))
	for _, v := range r.experiments {
		items = append(items, v)
	}
	return items, nil
}
func (r *memoryRepository) CompletedEvaluationExperiments(_ context.Context, _, _ string, ids []string) ([]Experiment, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var items []Experiment
	for _, id := range ids {
		if v := r.experiments[id]; v.CompletedAt != nil {
			items = append(items, v)
		}
	}
	return items, nil
}
func (r *memoryRepository) FinishEvaluationExperiment(_ context.Context, _, _, id string, result map[string]Progress, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	v := r.experiments[id]
	v.Result = result
	v.CompletedAt = &at
	r.experiments[id] = v
	return nil
}
func awaitExperiment(t *testing.T, s *Service, actor, space, id, status string) Experiment {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		v, err := s.Experiment(context.Background(), actor, space, id)
		if err != nil {
			t.Fatal(err)
		}
		if v.Status == status {
			return v
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("experiment did not reach %s", status)
	return Experiment{}
}

type completionDuringRead struct {
	*memoryRepository
	complete func()
}

func (r completionDuringRead) EvaluationExperiment(ctx context.Context, actor, space, id string) (Experiment, error) {
	v, err := r.memoryRepository.EvaluationExperiment(ctx, actor, space, id)
	r.complete()
	return v, err
}

func (r completionDuringRead) EvaluationExperiments(ctx context.Context, actor, space string, q identity.Query) ([]Experiment, error) {
	items, err := r.memoryRepository.EvaluationExperiments(ctx, actor, space, q)
	r.complete()
	return items, err
}

func (r completionDuringRead) FindEvaluationRequest(ctx context.Context, actor, space, key, digest string) (Experiment, bool, error) {
	v, found, err := r.memoryRepository.FindEvaluationRequest(ctx, actor, space, key, digest)
	r.complete()
	return v, found, err
}

func TestExperimentReadsDuringCompletion(t *testing.T) {
	for _, entry := range []string{"detail", "list", "duplicate launch"} {
		t.Run(entry, func(t *testing.T) {
			ctx := context.Background()
			repo := newMemoryRepository()
			actor, space, id := uuid.NewString(), storage.NewShortID(), storage.NewShortID()
			in := ExperimentInput{DatasetID: repo.dataset.ID, AgentID: storage.NewShortID(), AgentVersionID: uuid.NewString(), Concurrency: 1, Key: "finishing"}
			exp := Experiment{ID: id, SpaceID: space, ActorID: actor, Key: in.Key, Digest: journal.Digest(in), CaseCount: 1}
			repo.experiments[id] = exp
			interruptedID := storage.NewShortID()
			repo.experiments[interruptedID] = Experiment{ID: interruptedID, SpaceID: space}

			taskCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			worker := &task{experiment: exp, ctx: taskCtx, cancel: cancel, finishing: true, progress: map[string]Progress{
				"1": {Status: "succeeded", RunIDs: []string{storage.NewShortID()}},
			}}
			s := &Service{callTimeout: time.Second, ctx: ctx, tasks: map[string]*task{id: worker}}
			s.Repo = completionDuringRead{memoryRepository: repo, complete: func() {
				// Save the result and remove the task after taking the first
				// storage snapshot, before the request consults memory.
				s.wg.Add(1)
				s.finish(worker)
			}}

			var got Experiment
			var err error
			switch entry {
			case "detail":
				got, err = s.Experiment(ctx, actor, space, id)
			case "list":
				var items []Experiment
				items, err = s.Experiments(ctx, actor, space, identity.Query{Limit: 20})
				for _, item := range items {
					if item.ID == id {
						got = item
					}
					if item.ID == interruptedID && item.Status != "interrupted" {
						t.Fatalf("lost task reported as %s", item.Status)
					}
				}
				if len(items) != 2 || got.Result != nil {
					t.Fatalf("list shape changed: %+v", items)
				}
			case "duplicate launch":
				got, err = s.CreateExperiment(ctx, actor, space, in)
			}
			if err != nil || got.ID != id || got.Status != "completed" || got.CompletedAt == nil || got.Succeeded != 1 {
				t.Fatalf("completion was not reflected in the response: %+v, err = %v", got, err)
			}
			if entry != "list" && got.Result["1"].Status != "succeeded" {
				t.Fatalf("saved result is missing: %+v", got.Result)
			}
		})
	}
}

func TestMultiTurnIsolationFailureAndIdempotency(t *testing.T) {
	repo := newMemoryRepository()
	actor, space := uuid.NewString(), storage.NewShortID()
	var mu sync.Mutex
	sessions := map[string][]string{}
	runs := map[string]run.View{}
	creates := 0
	calls := Calls{
		CheckAgent: func(context.Context, string, string, string, string) error { return nil },
		Create: func(_ context.Context, _, _ string, in sessionapp.CreateInput) (run.Session, error) {
			mu.Lock()
			defer mu.Unlock()
			if !in.Hide {
				t.Error("evaluation session is visible")
			}
			creates++
			id := storage.NewShortID()
			sessions[id] = []string{}
			return run.Session{ID: id}, nil
		},
		Submit: func(_ context.Context, _, _, session string, in sessionapp.SubmitInput) (run.View, error) {
			mu.Lock()
			defer mu.Unlock()
			if in.Text == "must not send" {
				t.Error("sent after failed turn")
			}
			if in.Text == "second" && (len(sessions[session]) != 1 || sessions[session][0] != "first") {
				t.Error("history was not shared within case")
			}
			sessions[session] = append(sessions[session], in.Text)
			id := storage.NewShortID()
			v := run.View{Snapshot: run.Snapshot{ID: id, SessionID: session, CreatedAt: time.Now(), Definition: run.Definition{TimeoutSeconds: 10}}, Status: "queued"}
			runs[id] = v
			return v, nil
		},
		Run: func(_ context.Context, _, _, id string) (run.View, error) {
			mu.Lock()
			defer mu.Unlock()
			v := runs[id]
			v.Status = "succeeded"
			if sessions[v.SessionID][0] == "failure" {
				v.Status = "failed"
				v.Error = "context_overflow"
			}
			return v, nil
		},
		Diagnostics: func(context.Context, string, string, []string) ([]json.RawMessage, error) { return nil, nil },
	}
	s := &Service{Repo: repo, Calls: calls, pollInterval: time.Millisecond, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	s.Start(context.Background())
	defer s.Close(context.Background())
	in := ExperimentInput{DatasetID: repo.dataset.ID, AgentID: storage.NewShortID(), AgentVersionID: uuid.NewString(), Concurrency: 2, Key: "experiment"}
	v, err := s.CreateExperiment(context.Background(), actor, space, in)
	if err != nil {
		t.Fatal(err)
	}
	done := awaitExperiment(t, s, actor, space, v.ID, "completed")
	if done.Succeeded != 1 || done.Failed != 1 || len(done.Result["1"].RunIDs) != 2 || len(done.Result["2"].RunIDs) != 1 {
		t.Fatalf("wrong result: %+v", done)
	}
	if done.Result["1"].SessionID == done.Result["2"].SessionID || creates != 2 {
		t.Fatal("cases did not use separate sessions")
	}
	duplicate, err := s.CreateExperiment(context.Background(), actor, space, in)
	if err != nil || duplicate.ID != v.ID {
		t.Fatalf("duplicate changed experiment: %v", err)
	}
	in.Concurrency = 1
	if _, err = s.CreateExperiment(context.Background(), actor, space, in); err != identity.ErrConflict {
		t.Fatalf("different input did not conflict: %v", err)
	}
}
func TestWaitDeadlineInterruptsWithoutChangingRun(t *testing.T) {
	repo := newMemoryRepository()
	actor, space := uuid.NewString(), storage.NewShortID()
	submitted := 0
	calls := Calls{CheckAgent: func(context.Context, string, string, string, string) error { return nil }, Create: func(context.Context, string, string, sessionapp.CreateInput) (run.Session, error) {
		return run.Session{ID: storage.NewShortID()}, nil
	}, Submit: func(context.Context, string, string, string, sessionapp.SubmitInput) (run.View, error) {
		submitted++
		return run.View{Snapshot: run.Snapshot{ID: storage.NewShortID(), CreatedAt: time.Now().Add(-time.Hour)}, Status: "queued"}, nil
	}}
	s := &Service{Repo: repo, Calls: calls, pollInterval: time.Millisecond, queueTimeout: time.Millisecond}
	s.Start(context.Background())
	defer s.Close(context.Background())
	v, err := s.CreateExperiment(context.Background(), actor, space, ExperimentInput{DatasetID: repo.dataset.ID, AgentID: storage.NewShortID(), AgentVersionID: uuid.NewString(), Concurrency: 1, Key: "interrupt"})
	if err != nil {
		t.Fatal(err)
	}
	done := awaitExperiment(t, s, actor, space, v.ID, "interrupted")
	if done.Result != nil || submitted != 1 {
		t.Fatalf("interrupted task was replayed: %+v %d", done, submitted)
	}
	restarted := &Service{Repo: repo}
	restarted.Start(context.Background())
	defer restarted.Close(context.Background())
	after, err := restarted.Experiment(context.Background(), actor, space, v.ID)
	if err != nil || after.Status != "interrupted" {
		t.Fatal(fmt.Sprint(after, err))
	}
}

func TestExperimentsInSameSpaceUseTheirOwnConcurrency(t *testing.T) {
	repo := newMemoryRepository()
	repo.dataset.Cases = []Case{}
	for range 4 {
		repo.dataset.Cases = append(repo.dataset.Cases, Case{Turns: []Turn{{Content: []Part{{Type: "text", Text: "work"}}}}})
	}
	actor, space := uuid.NewString(), storage.NewShortID()
	var mu sync.Mutex
	active, peak, submitted := 0, 0, 0
	ready := make(chan struct{}, 5)
	release := make(chan struct{})
	calls := Calls{
		CheckAgent: func(context.Context, string, string, string, string) error { return nil },
		Create: func(context.Context, string, string, sessionapp.CreateInput) (run.Session, error) {
			return run.Session{ID: storage.NewShortID()}, nil
		},
		Submit: func(context.Context, string, string, string, sessionapp.SubmitInput) (run.View, error) {
			mu.Lock()
			active++
			submitted++
			if active > peak {
				peak = active
			}
			mu.Unlock()
			select {
			case ready <- struct{}{}:
			default:
			}
			return run.View{Snapshot: run.Snapshot{ID: storage.NewShortID(), CreatedAt: time.Now(), Definition: run.Definition{TimeoutSeconds: 30}}, Status: "queued"}, nil
		},
		Run: func(_ context.Context, _, _, id string) (run.View, error) {
			v := run.View{Snapshot: run.Snapshot{ID: id, CreatedAt: time.Now(), Definition: run.Definition{TimeoutSeconds: 30}}, Status: "queued"}
			select {
			case <-release:
				mu.Lock()
				active--
				mu.Unlock()
				v.Status = "succeeded"
			default:
			}
			return v, nil
		},
		Diagnostics: func(context.Context, string, string, []string) ([]json.RawMessage, error) { return nil, nil },
	}
	s := &Service{Repo: repo, Calls: calls, pollInterval: time.Millisecond}
	s.Start(context.Background())
	defer s.Close(context.Background())
	input := ExperimentInput{DatasetID: repo.dataset.ID, AgentID: storage.NewShortID(), AgentVersionID: uuid.NewString(), Concurrency: 3, Key: "one"}
	first, err := s.CreateExperiment(context.Background(), actor, space, input)
	if err != nil {
		t.Fatal(err)
	}
	input.Key = "two"
	input.Concurrency = 2
	second, err := s.CreateExperiment(context.Background(), actor, space, input)
	if err != nil {
		t.Fatal(err)
	}
	for range 5 {
		select {
		case <-ready:
		case <-time.After(time.Second):
			t.Fatal("workers did not start")
		}
	}
	time.Sleep(10 * time.Millisecond)
	mu.Lock()
	before := submitted
	mu.Unlock()
	if before != 5 {
		t.Fatalf("submitted %d runs with per-experiment concurrency of 3 and 2", before)
	}
	close(release)
	awaitExperiment(t, s, actor, space, first.ID, "completed")
	awaitExperiment(t, s, actor, space, second.ID, "completed")
	mu.Lock()
	defer mu.Unlock()
	if peak != 5 || submitted != 8 || active != 0 {
		t.Fatalf("wrong concurrency: peak=%d submitted=%d active=%d", peak, submitted, active)
	}
}

func TestMultiTurnFailurePosition(t *testing.T) {
	for _, stage := range []string{"submit_run", "run"} {
		t.Run(stage, func(t *testing.T) {
			repo := newMemoryRepository()
			repo.dataset.Cases = []Case{{Turns: []Turn{
				{Content: []Part{{Type: "text", Text: "one"}}},
				{Content: []Part{{Type: "text", Text: "two"}}},
				{Content: []Part{{Type: "text", Text: "three"}}},
			}}}
			attempts := 0
			calls := Calls{
				CheckAgent: func(context.Context, string, string, string, string) error { return nil },
				Create: func(context.Context, string, string, sessionapp.CreateInput) (run.Session, error) {
					return run.Session{ID: storage.NewShortID()}, nil
				},
				Submit: func(context.Context, string, string, string, sessionapp.SubmitInput) (run.View, error) {
					attempts++
					if stage == "submit_run" && attempts == 3 {
						return run.View{}, identity.ErrConflict
					}
					v := run.View{Snapshot: run.Snapshot{ID: storage.NewShortID()}, Status: "succeeded"}
					if stage == "run" && attempts == 2 {
						v.Status, v.Error = "failed", "context_overflow"
					}
					return v, nil
				},
				Diagnostics: func(context.Context, string, string, []string) ([]json.RawMessage, error) { return nil, nil },
			}
			s := &Service{Repo: repo, Calls: calls, pollInterval: time.Millisecond}
			s.Start(context.Background())
			defer s.Close(context.Background())
			actor, space := uuid.NewString(), storage.NewShortID()
			v, err := s.CreateExperiment(context.Background(), actor, space, ExperimentInput{DatasetID: repo.dataset.ID, AgentID: storage.NewShortID(), AgentVersionID: uuid.NewString(), Concurrency: 1, Key: stage})
			if err != nil {
				t.Fatal(err)
			}
			done := awaitExperiment(t, s, actor, space, v.ID, "completed")
			p := done.Result["1"]
			expectedAttempts := 2
			if stage == "submit_run" {
				expectedAttempts = 3
			}
			if p.Status != "failed" || p.FailureStage != stage || len(p.RunIDs) != 2 || attempts != expectedAttempts {
				t.Fatalf("wrong failure position: progress=%+v attempts=%d", p, attempts)
			}
		})
	}
}
