package evaluation

import (
	"context"
	"log/slog"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/imalimin/mass/internal/app/identity"
	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/journal"
)

type task struct {
	experiment  Experiment
	dataset     Dataset
	progress    map[string]Progress
	ctx         context.Context
	cancel      context.CancelFunc
	writeCtx    context.Context
	stopWrites  context.CancelCauseFunc
	active      int
	finishing   bool
	interrupted bool
}
type Service struct {
	Repo            Repository
	Calls           Calls
	MaxPerSpace     int
	SnapshotTimeout time.Duration
	Logger          *slog.Logger
	mu              sync.Mutex
	createMu        sync.Mutex
	tasks           map[string]*task
	spaces          map[string]int
	ctx             context.Context
	cancel          context.CancelFunc
	stopped         bool
	wake            chan struct{}
	wg              sync.WaitGroup
	pollInterval    time.Duration
	callTimeout     time.Duration
	queueTimeout    time.Duration
	grace           time.Duration
}

func (s *Service) Start(parent context.Context) {
	if s.pollInterval == 0 {
		s.pollInterval = time.Second
	}
	if s.callTimeout == 0 {
		s.callTimeout = 15 * time.Second
	}
	if s.queueTimeout == 0 {
		s.queueTimeout = 10 * time.Minute
	}
	if s.grace == 0 {
		s.grace = time.Minute
	}
	s.ctx, s.cancel = context.WithCancel(parent)
	s.tasks = map[string]*task{}
	s.spaces = map[string]int{}
	s.wake = make(chan struct{}, 1)
	s.wg.Add(1)
	go s.loop()
}
func (s *Service) Close(ctx context.Context) error {
	s.mu.Lock()
	s.stopped = true
	s.cancel()
	s.mu.Unlock()
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (s *Service) notify() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}
func (s *Service) SaveDataset(ctx context.Context, actor, space, id string, in DatasetInput) (Dataset, error) {
	if !storage.ValidShortID(space) || (id != "" && !storage.ValidShortID(id)) {
		return Dataset{}, identity.ErrInvalid
	}
	if err := ValidateDataset(in); err != nil {
		return Dataset{}, err
	}
	return s.Repo.SaveEvaluationDataset(ctx, actor, space, id, in)
}
func (s *Service) CreateExperiment(ctx context.Context, actor, space string, in ExperimentInput) (Experiment, error) {
	if !storage.ValidShortID(space) {
		return Experiment{}, identity.ErrInvalid
	}
	if err := validateExperiment(in, s.MaxPerSpace); err != nil {
		return Experiment{}, err
	}
	s.createMu.Lock()
	defer s.createMu.Unlock()
	digest := journal.Digest(in)
	previous, found, err := s.Repo.FindEvaluationRequest(ctx, actor, space, in.Key, digest)
	if err != nil {
		return Experiment{}, err
	}
	if found {
		return s.experimentState(ctx, actor, space, previous)
	}
	s.mu.Lock()
	stopped := s.stopped || s.ctx.Err() != nil
	s.mu.Unlock()
	if stopped {
		return Experiment{}, context.Canceled
	}
	check, stop := context.WithTimeout(ctx, s.callTimeout)
	err = s.Calls.CheckAgent(check, actor, space, in.AgentID, in.AgentVersionID)
	stop()
	if err != nil {
		return Experiment{}, err
	}
	v, err := s.Repo.CreateEvaluationExperiment(ctx, Experiment{ID: storage.NewShortID(), SpaceID: space, ActorID: actor, CreatedAt: time.Now().UTC().Truncate(time.Microsecond), DatasetID: in.DatasetID, AgentID: in.AgentID, AgentVersionID: in.AgentVersionID, Concurrency: in.Concurrency, Key: in.Key, Digest: digest})
	if err != nil {
		return v, err
	}
	d, err := s.Repo.EvaluationDataset(ctx, actor, space, v.DatasetID, v.DatasetVersion)
	if err != nil {
		return v, err
	}
	s.mu.Lock()
	if !s.stopped && s.ctx.Err() == nil {
		background, cancel := context.WithCancel(s.ctx)
		writeCtx, stopWrites := context.WithCancelCause(background)
		s.tasks[v.ID] = &task{
			experiment: v, dataset: d, progress: map[string]Progress{},
			ctx: background, cancel: cancel, writeCtx: writeCtx, stopWrites: stopWrites,
		}
	}
	s.mu.Unlock()
	s.notify()
	return s.experimentState(ctx, actor, space, v)
}
func copyProgress(in map[string]Progress) map[string]Progress {
	out := make(map[string]Progress, len(in))
	for k, p := range in {
		p.RunIDs = append([]string{}, p.RunIDs...)
		out[k] = p
	}
	return out
}
func (s *Service) decorate(v Experiment) Experiment {
	s.mu.Lock()
	defer s.mu.Unlock()
	v.Status = "interrupted"
	progress := v.Result
	if v.CompletedAt != nil && v.Result != nil {
		v.Status = "completed"
	} else if t := s.tasks[v.ID]; t != nil && !t.interrupted {
		v.Status = "queued"
		if len(t.progress) > 0 {
			v.Status = "running"
		}
		v.CaseProgress = copyProgress(t.progress)
		progress = v.CaseProgress
	}
	v.Succeeded, v.Failed = 0, 0
	for _, p := range progress {
		if p.Status == "succeeded" {
			v.Succeeded++
		}
		if p.Status == "failed" {
			v.Failed++
		}
	}
	return v
}
func (s *Service) experimentState(ctx context.Context, actor, space string, v Experiment) (Experiment, error) {
	items := []Experiment{v}
	err := s.resolveExperimentStates(ctx, actor, space, items)
	return items[0], err
}

func (s *Service) resolveExperimentStates(ctx context.Context, actor, space string, items []Experiment) error {
	var recheck []string
	positions := make(map[string]int)
	for i, v := range items {
		items[i] = s.decorate(v)
		if items[i].Status == "interrupted" {
			recheck = append(recheck, v.ID)
			positions[v.ID] = i
		}
	}
	if len(recheck) == 0 {
		return nil
	}

	// A result can be committed after the initial read and before the task
	// lookup. Recheck storage after observing that the task is no longer active.
	completed, err := s.Repo.CompletedEvaluationExperiments(ctx, actor, space, recheck)
	if err != nil {
		return err
	}
	for _, v := range completed {
		items[positions[v.ID]] = s.decorate(v)
	}
	return nil
}

func (s *Service) Experiment(ctx context.Context, actor, space, id string) (Experiment, error) {
	if !storage.ValidShortID(id) || !storage.ValidShortID(space) {
		return Experiment{}, identity.ErrInvalid
	}
	v, err := s.Repo.EvaluationExperiment(ctx, actor, space, id)
	if err != nil {
		return v, err
	}
	return s.experimentState(ctx, actor, space, v)
}
func (s *Service) Experiments(ctx context.Context, actor, space string, q identity.Query) ([]Experiment, error) {
	items, err := s.Repo.EvaluationExperiments(ctx, actor, space, q)
	if err != nil {
		return nil, err
	}
	if err := s.resolveExperimentStates(ctx, actor, space, items); err != nil {
		return nil, err
	}
	for i := range items {
		items[i].CaseProgress = nil
		items[i].Result = nil
	}
	return items, nil
}
func (s *Service) Cases(ctx context.Context, actor, space, id string) ([]CaseView, error) {
	v, err := s.Experiment(ctx, actor, space, id)
	if err != nil {
		return nil, err
	}
	d, err := s.Repo.EvaluationDataset(ctx, actor, space, v.DatasetID, v.DatasetVersion)
	if err != nil {
		return nil, err
	}
	progress := v.CaseProgress
	if v.Result != nil {
		progress = v.Result
	}
	items := make([]CaseView, 0, len(d.Cases))
	for i, c := range d.Cases {
		p, ok := progress[strconv.Itoa(i+1)]
		if !ok {
			status := "pending"
			if v.Status == "interrupted" {
				status = "unavailable"
			}
			p = Progress{Status: status, RunIDs: []string{}}
		}
		items = append(items, CaseView{Ordinal: i + 1, Turns: c.Turns, Progress: p})
	}
	return items, nil
}
func (s *Service) loop() {
	defer s.wg.Done()
	ticker := time.NewTicker(s.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
		case <-s.wake:
		}
		s.mu.Lock()
		if s.stopped || s.ctx.Err() != nil {
			s.mu.Unlock()
			return
		}
		tasks := make([]*task, 0, len(s.tasks))
		for _, t := range s.tasks {
			tasks = append(tasks, t)
		}
		sort.Slice(tasks, func(i, j int) bool { return tasks[i].experiment.CreatedAt.Before(tasks[j].experiment.CreatedAt) })
		for _, t := range tasks {
			if t.interrupted {
				if t.active == 0 {
					delete(s.tasks, t.experiment.ID)
				}
				continue
			}
			if t.finishing {
				continue
			}
			for i := range t.dataset.Cases {
				if t.active >= t.experiment.Concurrency || s.spaces[t.experiment.SpaceID] >= s.MaxPerSpace {
					break
				}
				key := strconv.Itoa(i + 1)
				if _, ok := t.progress[key]; ok {
					continue
				}
				t.progress[key] = Progress{Status: "active", RunIDs: []string{}}
				t.active++
				s.spaces[t.experiment.SpaceID]++
				s.wg.Add(1)
				go s.runCase(t, i)
			}
			if len(t.progress) == len(t.dataset.Cases) && t.active == 0 {
				t.finishing = true
				s.wg.Add(1)
				go s.finish(t)
			}
		}
		s.mu.Unlock()
	}
}
func (s *Service) finish(t *task) {
	defer s.wg.Done()
	// Finalization follows the service lifetime, including after a write is denied.
	ctx, stop := context.WithTimeout(s.ctx, s.callTimeout)
	err := s.Repo.FinishEvaluationExperiment(ctx, t.experiment.ActorID, t.experiment.SpaceID, t.experiment.ID, copyProgress(t.progress), time.Now().UTC().Truncate(time.Microsecond))
	stop()
	if err != nil && s.Logger != nil {
		s.Logger.Warn("evaluation result save failed", "experiment_id", t.experiment.ID, "error", err)
	}
	s.mu.Lock()
	delete(s.tasks, t.experiment.ID)
	t.cancel()
	s.mu.Unlock()
	s.notify()
}
