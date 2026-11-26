package mariadb

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	"github.com/imalimin/mass/internal/app/identity"
	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/journal"
)

type RuntimeLease struct {
	store         *Store
	conn          *sql.Conn
	ID, name      string
	database      string
	peerAddr      string
	multiNode     bool
	connection    int64
	mu            sync.Mutex
	alive         atomic.Bool
	closed        bool
	stopKeepalive context.CancelFunc
}

const (
	leaseWaitTimeoutSeconds = 30
	leaseDurationSeconds    = 30
	nodeOnlineWindowSeconds = 45
	leaseKeepaliveInterval  = 10 * time.Second
	sessionLockTimeoutSecs  = 5
)

// OpenRuntime acquires an exclusive database-wide runtime lock for offline CLI
// maintenance and legacy single-node callers.
func (s *Store) OpenRuntime(ctx context.Context) (*RuntimeLease, error) {
	conn, err := s.DB.Conn(ctx)
	if err != nil {
		return nil, err
	}
	// Bound how long a half-open TCP connection (e.g. mobile kill / network drop)
	// can retain the runtime lock on the MariaDB server.
	_, _ = conn.ExecContext(ctx, "SET SESSION wait_timeout=?", leaseWaitTimeoutSeconds)
	l := &RuntimeLease{store: s, conn: conn, ID: uuid.NewString()}
	var database string
	if err = conn.QueryRowContext(ctx, "SELECT DATABASE(),CONNECTION_ID()").Scan(&database, &l.connection); err != nil {
		conn.Close()
		return nil, err
	}
	l.database = database
	l.name = "mass:" + database + ":runtime"
	var acquired sql.NullInt64
	if err = conn.QueryRowContext(ctx, "SELECT GET_LOCK(?,0)", l.name).Scan(&acquired); err != nil || !acquired.Valid || acquired.Int64 != 1 {
		conn.Close()
		return nil, run.ErrUnavailable
	}
	// Refuse exclusive maintenance while any multi-node runtime instance is active.
	var activeNodes int
	if err = conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM runtime_nodes WHERE heartbeat_at >= DATE_SUB(UTC_TIMESTAMP(6), INTERVAL ? SECOND)", nodeOnlineWindowSeconds).Scan(&activeNodes); err != nil {
		var mysqlErr *mysql.MySQLError
		if !errors.As(err, &mysqlErr) || mysqlErr.Number != 1146 {
			_, _ = conn.ExecContext(context.Background(), "SELECT RELEASE_LOCK(?)", l.name)
			conn.Close()
			return nil, err
		}
	} else if activeNodes > 0 {
		_, _ = conn.ExecContext(context.Background(), "SELECT RELEASE_LOCK(?)", l.name)
		conn.Close()
		return nil, run.ErrUnavailable
	}
	l.alive.Store(true)
	keepCtx, cancel := context.WithCancel(context.Background())
	l.stopKeepalive = cancel
	go l.keepalive(keepCtx)
	return l, nil
}

// OpenRuntimeNode registers this instance in runtime_nodes for multi-node active
// execution without taking a database-wide exclusive lock.
func (s *Store) OpenRuntimeNode(ctx context.Context, peerAddr string) (*RuntimeLease, error) {
	var database string
	if err := s.DB.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&database); err != nil {
		return nil, err
	}
	l := &RuntimeLease{
		store:     s,
		ID:        uuid.NewString(),
		database:  database,
		peerAddr:  peerAddr,
		multiNode: true,
	}
	if _, err := s.DB.ExecContext(ctx,
		"INSERT INTO runtime_nodes(node_id,peer_addr,started_at,heartbeat_at) VALUES(?,?,UTC_TIMESTAMP(6),UTC_TIMESTAMP(6))",
		l.ID, peerAddr,
	); err != nil {
		return nil, err
	}
	l.alive.Store(true)
	keepCtx, cancel := context.WithCancel(context.Background())
	l.stopKeepalive = cancel
	go l.keepalive(keepCtx)
	return l, nil
}

func (l *RuntimeLease) NodeID() string { return l.ID }

func (l *RuntimeLease) LeaseDuration() time.Duration {
	return leaseDurationSeconds * time.Second
}

func (l *RuntimeLease) keepalive(ctx context.Context) {
	ticker := time.NewTicker(leaseKeepaliveInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if l.multiNode {
				if err := l.renewNode(ctx); err != nil {
					return
				}
			} else if err := l.Check(ctx); err != nil {
				return
			}
		}
	}
}

func (l *RuntimeLease) renewNode(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.alive.Load() || l.closed {
		return run.ErrUnavailable
	}
	probe, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	res, err := l.store.DB.ExecContext(probe, "UPDATE runtime_nodes SET heartbeat_at=UTC_TIMESTAMP(6) WHERE node_id=?", l.ID)
	if err != nil {
		return nil
	}
	if n, _ := res.RowsAffected(); n != 1 {
		l.alive.Store(false)
		return run.ErrUnavailable
	}
	_, _ = l.store.DB.ExecContext(probe,
		"UPDATE runs SET lease_expires_at=DATE_ADD(UTC_TIMESTAMP(6), INTERVAL ? SECOND) WHERE owner_node_id=? AND status='running'",
		leaseDurationSeconds, l.ID,
	)
	_, _ = l.store.DB.ExecContext(probe,
		"UPDATE evaluation_experiments SET lease_expires_at=DATE_ADD(UTC_TIMESTAMP(6), INTERVAL ? SECOND) WHERE owner_node_id=? AND completed_at IS NULL",
		leaseDurationSeconds, l.ID,
	)
	_, _ = l.store.DB.ExecContext(probe, "DELETE FROM runtime_nodes WHERE heartbeat_at < DATE_SUB(UTC_TIMESTAMP(6), INTERVAL 5 MINUTE)")
	return nil
}

func (l *RuntimeLease) Check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.alive.Load() {
		return run.ErrUnavailable
	}
	probe, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	if l.multiNode {
		var one int
		err := l.store.DB.QueryRowContext(probe, "SELECT 1 FROM runtime_nodes WHERE node_id=?", l.ID).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			l.alive.Store(false)
			return run.ErrUnavailable
		}
		if err != nil {
			return run.ErrUnavailable
		}
		return nil
	}
	var owner sql.NullInt64
	// Cancelling a request must not close the lock connection while workers still exit.
	if err := l.conn.QueryRowContext(probe, "SELECT IS_USED_LOCK(?)", l.name).Scan(&owner); err != nil || !owner.Valid || owner.Int64 != l.connection {
		l.alive.Store(false)
		return run.ErrUnavailable
	}
	return nil
}

func (l *RuntimeLease) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	l.alive.Store(false)
	if l.stopKeepalive != nil {
		l.stopKeepalive()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if l.multiNode {
		_, _ = l.store.DB.ExecContext(ctx, "UPDATE runs SET lease_expires_at=UTC_TIMESTAMP(6) WHERE owner_node_id=? AND status='running'", l.ID)
		_, _ = l.store.DB.ExecContext(ctx, "UPDATE evaluation_experiments SET lease_expires_at=UTC_TIMESTAMP(6) WHERE owner_node_id=? AND completed_at IS NULL", l.ID)
		_, err := l.store.DB.ExecContext(ctx, "DELETE FROM runtime_nodes WHERE node_id=?", l.ID)
		return err
	}
	_, err := l.conn.ExecContext(ctx, "SELECT RELEASE_LOCK(?)", l.name)
	closeErr := l.conn.Close()
	if errors.Is(err, driver.ErrBadConn) || errors.Is(err, sql.ErrConnDone) {
		err = nil
	}
	if errors.Is(closeErr, sql.ErrConnDone) {
		closeErr = nil
	}
	return errors.Join(err, closeErr)
}

// WithSessionLock serializes writes to a single Session across nodes via a
// connection-scoped MySQL advisory lock.
func (l *RuntimeLease) WithSessionLock(ctx context.Context, sessionID string, fn func(context.Context) error) error {
	if l == nil || !l.multiNode || l.store == nil || l.store.DB == nil {
		return fn(ctx)
	}
	conn, err := l.store.DB.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	lockName := "mass:" + l.database + ":session:" + sessionID
	var acquired sql.NullInt64
	if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK(?,?)", lockName, sessionLockTimeoutSecs).Scan(&acquired); err != nil {
		return err
	}
	if !acquired.Valid || acquired.Int64 != 1 {
		return storage.ErrActive
	}
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, _ = conn.ExecContext(releaseCtx, "SELECT RELEASE_LOCK(?)", lockName)
	}()
	return fn(ctx)
}

// RunPeer returns the peer address owning an active Run when it is executing on
// a different node.
func (l *RuntimeLease) RunPeer(ctx context.Context, space, runID string) (peerAddr string, remote bool, err error) {
	if l == nil || !l.multiNode || l.store == nil || l.store.DB == nil {
		return "", false, nil
	}
	var status, owner, addr string
	err = l.store.DB.QueryRowContext(ctx, `
		SELECT r.status, COALESCE(r.owner_node_id,''), COALESCE(n.peer_addr,'')
		FROM runs r
		JOIN sessions s ON s.id = r.session_id
		LEFT JOIN runtime_nodes n ON n.node_id = r.owner_node_id
			AND n.heartbeat_at >= DATE_SUB(UTC_TIMESTAMP(6), INTERVAL ? SECOND)
		WHERE r.id = ? AND s.space_id = ?`,
		nodeOnlineWindowSeconds, runID, space,
	).Scan(&status, &owner, &addr)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if (status == "queued" || status == "running") && owner != "" && owner != l.ID {
		return addr, true, nil
	}
	return "", false, nil
}

// ExperimentPeer returns the peer address owning an active evaluation experiment
// when it is executing on a different node.
func (l *RuntimeLease) ExperimentPeer(ctx context.Context, space, experimentID string) (peerAddr string, remote bool, err error) {
	if l == nil || !l.multiNode || l.store == nil || l.store.DB == nil {
		return "", false, nil
	}
	var owner, addr string
	err = l.store.DB.QueryRowContext(ctx, `
		SELECT COALESCE(e.owner_node_id,''), COALESCE(n.peer_addr,'')
		FROM evaluation_experiments e
		LEFT JOIN runtime_nodes n ON n.node_id = e.owner_node_id
			AND n.heartbeat_at >= DATE_SUB(UTC_TIMESTAMP(6), INTERVAL ? SECOND)
		WHERE e.space_id = ? AND e.id = ? AND e.completed_at IS NULL
			AND e.lease_expires_at >= UTC_TIMESTAMP(6)`,
		nodeOnlineWindowSeconds, space, experimentID,
	).Scan(&owner, &addr)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if owner != "" && owner != l.ID {
		return addr, true, nil
	}
	return "", false, nil
}
func (l *RuntimeLease) Candidates(ctx context.Context, limit int) (items []run.Snapshot, err error) {
	if err = l.Check(ctx); err != nil {
		return nil, err
	}
	rows, err := l.store.DB.QueryContext(ctx, "SELECT snapshot FROM runs WHERE snapshot IS NOT NULL AND status='queued' ORDER BY created_at,id LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		var s run.Snapshot
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &s); err != nil {
			return nil, err
		}
		items = append(items, s)
	}
	return items, rows.Err()
}
func (l *RuntimeLease) Claim(ctx context.Context, s run.Snapshot) (run.View, error) {
	if err := l.Check(ctx); err != nil {
		return run.View{}, err
	}
	e := journal.New(s.ActorID, "run.started", s.ID, journal.StartData{OwnerID: l.ID})
	b, err := journal.Prepare(s.ActorID, journal.Request{SpaceID: s.SpaceID, SessionID: s.SessionID, AppendKey: e.EventID, Events: []journal.Proposed{e}})
	if err != nil {
		return run.View{}, err
	}
	if _, err = l.store.AppendEvents(ctx, s.ActorID, b); err != nil {
		return run.View{}, err
	}
	return l.store.RuntimeRun(ctx, s.ActorID, s.SpaceID, s.ID)
}

// checkRun reads only the facts needed to authorize continued execution.
func (l *RuntimeLease) checkRun(ctx context.Context, tx *sql.Tx, s run.Snapshot) error {
	var status, owner string
	var cancelled bool
	if err := tx.QueryRowContext(ctx, "SELECT status,COALESCE(owner_id,''),cancel_requested FROM runs WHERE space_id=? AND id=? AND snapshot IS NOT NULL", s.SpaceID, s.ID).Scan(&status, &owner, &cancelled); err != nil {
		return checkError("read_run", dbError(err))
	}
	if status != "running" || owner != l.ID {
		return run.ErrState
	}
	if cancelled {
		return run.ErrCancelled
	}
	var id string
	err := tx.QueryRowContext(ctx, "SELECT id FROM model_configs WHERE space_id=? AND id=? AND enabled=TRUE", s.SpaceID, s.Definition.Model.ID).Scan(&id)
	return checkError("check_model", dbError(err))
}

func (l *RuntimeLease) CheckRun(ctx context.Context, s run.Snapshot) error {
	if !l.alive.Load() {
		return run.ErrUnavailable
	}
	return l.store.scoped(ctx, s.ActorID, s.SpaceID, false, func(tx *sql.Tx) error {
		return l.checkRun(ctx, tx, s)
	})
}

// CleanupRepository is a capability for one claimed run. It cannot expose
// identity, model, arbitrary query or tool APIs, and cannot append new actions.
type CleanupRepository struct {
	store    *Store
	lease    *RuntimeLease
	snapshot run.Snapshot
}

func (l *RuntimeLease) Cleanup(s run.Snapshot) *CleanupRepository {
	scoped := &Store{DB: l.store.DB, systemScope: true}
	scoped.scopeOverride = func(ctx context.Context, tx *sql.Tx, actor, space string, owner bool) error {
		if !l.alive.Load() || actor != s.ActorID || space != s.SpaceID || owner {
			return identity.ErrForbidden
		}
		var id string
		if err := tx.QueryRowContext(ctx, "SELECT id FROM spaces WHERE id=? FOR UPDATE", space).Scan(&id); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, "SELECT id FROM sessions WHERE space_id=? AND id=? FOR UPDATE", space, s.SessionID).Scan(&id); err != nil {
			return err
		}
		var actorID, ownerID string
		if err := tx.QueryRowContext(ctx, "SELECT actor_id,COALESCE(owner_id,'') FROM runs WHERE space_id=? AND session_id=? AND id=? FOR UPDATE", space, s.SessionID, s.ID).Scan(&actorID, &ownerID); err != nil {
			return err
		}
		if actorID != s.ActorID || ownerID != l.ID {
			return identity.ErrForbidden
		}
		return nil
	}
	return &CleanupRepository{store: scoped, lease: l, snapshot: s}
}
func (c *CleanupRepository) AppendEvents(ctx context.Context, actor string, b journal.Batch) ([]journal.Event, error) {
	s := c.snapshot
	if b.SpaceID != s.SpaceID || b.SessionID != s.SessionID {
		return nil, identity.ErrForbidden
	}
	for _, e := range b.Events {
		if e.RunID != s.ID || e.Actor.Kind != "system" || e.Actor.ID != s.ActorID {
			return nil, identity.ErrForbidden
		}
		switch e.Type {
		case "run.finished", "workspace.snapshot.created", "workspace.snapshot.failed", "model.attempt.failed":
		default:
			return nil, identity.ErrForbidden
		}
	}
	checked := *c.store
	base := checked.scopeOverride
	requireMember := false
	for _, event := range b.Events {
		if event.Type == "run.finished" {
			var data journal.FinishData
			journal.Decode(event.Data, &data)
			if data.Status == "succeeded" {
				requireMember = true
			}
		}
	}
	if requireMember {
		checked.scopeOverride = func(ctx context.Context, tx *sql.Tx, actor, space string, owner bool) error {
			if err := base(ctx, tx, actor, space, owner); err != nil {
				return err
			}
			var n int
			if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM space_members WHERE space_id=? AND user_id=?", space, actor).Scan(&n); err != nil {
				return err
			}
			if n != 1 {
				return run.ErrPermission
			}
			return nil
		}
	}
	return checked.AppendEvents(ctx, actor, b)
}
func (c *CleanupRepository) ReadEvents(ctx context.Context, actor, space, session string, after int64, through *int64, limit int) (journal.Page, error) {
	if space != c.snapshot.SpaceID || session != c.snapshot.SessionID {
		return journal.Page{}, identity.ErrForbidden
	}
	return c.store.ReadEvents(ctx, actor, space, session, after, through, limit)
}
func (c *CleanupRepository) BeginObject(ctx context.Context, actor string, o storage.Object) (storage.Object, error) {
	if o.Kind != "workspace.archive" && o.Kind != "workspace.manifest" {
		return storage.Object{}, identity.ErrForbidden
	}
	return c.store.BeginObject(ctx, actor, o)
}
func (c *CleanupRepository) ReadyObject(ctx context.Context, actor string, o storage.Object) error {
	if o.Kind != "workspace.archive" && o.Kind != "workspace.manifest" {
		return identity.ErrForbidden
	}
	return c.store.ReadyObject(ctx, actor, o)
}
func (c *CleanupRepository) Object(ctx context.Context, actor, space, id string, referenced bool) (storage.Object, error) {
	o, err := c.store.Object(ctx, actor, space, id, referenced)
	if err == nil && o.Kind != "workspace.archive" && o.Kind != "workspace.manifest" {
		return o, identity.ErrForbidden
	}
	return o, err
}
func (c *CleanupRepository) Snapshot(ctx context.Context, actor, space, id string) (storage.Snapshot, error) {
	return c.store.Snapshot(ctx, actor, space, id)
}
func (c *CleanupRepository) SnapshotBase(ctx context.Context, actor, space, session, runID string) (string, error) {
	if session != c.snapshot.SessionID || runID != c.snapshot.ID {
		return "", identity.ErrForbidden
	}
	return c.store.SnapshotBase(ctx, actor, space, session, runID)
}
func (c *CleanupRepository) SnapshotEvent(ctx context.Context, actor, space, session, eventID string) (storage.Snapshot, bool, error) {
	if session != c.snapshot.SessionID {
		return storage.Snapshot{}, false, identity.ErrForbidden
	}
	return c.store.SnapshotEvent(ctx, actor, space, session, eventID)
}
func (l *RuntimeLease) RecoveryCandidates(ctx context.Context) (items []run.Snapshot, err error) {
	if err = l.Check(ctx); err != nil {
		return nil, err
	}
	rows, err := l.store.DB.QueryContext(ctx, "SELECT snapshot FROM runs WHERE snapshot IS NOT NULL AND status='running' ORDER BY created_at,id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		var s run.Snapshot
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &s); err != nil {
			return nil, err
		}
		items = append(items, s)
	}
	return items, rows.Err()
}
func (l *RuntimeLease) AdoptInterrupted(ctx context.Context, s run.Snapshot) error {
	if err := l.Check(ctx); err != nil {
		return err
	}
	return l.store.transaction(ctx, func(tx *sql.Tx) error {
		var id string
		if err := tx.QueryRowContext(ctx, "SELECT id FROM spaces WHERE id=? FOR UPDATE", s.SpaceID).Scan(&id); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, "SELECT id FROM sessions WHERE space_id=? AND id=? FOR UPDATE", s.SpaceID, s.SessionID).Scan(&id); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "UPDATE runs SET owner_id=? WHERE space_id=? AND session_id=? AND id=? AND status='running'", l.ID, s.SpaceID, s.SessionID, s.ID)
		return err
	})
}

// A queued run whose principal has lost access is failed without starting work.
func (l *RuntimeLease) RejectQueued(ctx context.Context, s run.Snapshot, code string) error {
	if err := l.Check(ctx); err != nil {
		return err
	}
	if err := l.store.transaction(ctx, func(tx *sql.Tx) error {
		var id string
		if err := tx.QueryRowContext(ctx, "SELECT id FROM spaces WHERE id=? FOR UPDATE", s.SpaceID).Scan(&id); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, "SELECT id FROM sessions WHERE space_id=? AND id=? FOR UPDATE", s.SpaceID, s.SessionID).Scan(&id); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "UPDATE runs SET owner_id=? WHERE space_id=? AND id=? AND status='queued'", l.ID, s.SpaceID, s.ID)
		return err
	}); err != nil {
		return err
	}
	e := journal.New(s.ActorID, "run.finished", s.ID, journal.FinishData{Status: "failed", Error: code})
	e.Actor.Kind = "system"
	j := &journal.Journal{Repo: l.Cleanup(s)}
	_, err := j.Append(ctx, s.ActorID, journal.Request{SpaceID: s.SpaceID, SessionID: s.SessionID, AppendKey: e.EventID, Events: []journal.Proposed{e}})
	return err
}

func (c *CleanupRepository) State(ctx context.Context) (v run.View, plan *run.CleanupPlan, err error) {
	err = c.store.scoped(ctx, c.snapshot.ActorID, c.snapshot.SpaceID, false, func(tx *sql.Tx) error {
		var e error
		v, e = runInTx(ctx, tx, c.snapshot.SpaceID, c.snapshot.ID)
		if e != nil {
			return e
		}
		var raw []byte
		if e = tx.QueryRowContext(ctx, "SELECT cleanup_plan FROM runs WHERE space_id=? AND id=?", c.snapshot.SpaceID, c.snapshot.ID).Scan(&raw); e != nil {
			return e
		}
		if len(raw) > 0 {
			plan = &run.CleanupPlan{}
			return json.Unmarshal(raw, plan)
		}
		return nil
	})
	return
}
func (c *CleanupRepository) SavePlan(ctx context.Context, plan run.CleanupPlan) error {
	return c.store.scoped(ctx, c.snapshot.ActorID, c.snapshot.SpaceID, false, func(tx *sql.Tx) error {
		raw, _ := json.Marshal(plan)
		_, err := tx.ExecContext(ctx, "UPDATE runs SET cleanup_plan=? WHERE space_id=? AND id=? AND status='running'", raw, c.snapshot.SpaceID, c.snapshot.ID)
		return err
	})
}
