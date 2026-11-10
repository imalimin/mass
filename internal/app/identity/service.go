package identity

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"math"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

const ArkEndpoint = "https://ark.cn-beijing.volces.com/api/v3"

var usernamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{2,63}$`)
var modelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)

type Service struct {
	Repo      Repository
	Cipher    Cipher
	TTL       time.Duration
	cursorKey []byte
	dummyHash []byte
	mu        sync.Mutex
	watchers  map[string]map[*watch]struct{}
}
type watch struct{ cancel context.CancelFunc }

func New(repo Repository, cipher Cipher, ttl time.Duration, key []byte) (*Service, error) {
	dummy, err := bcrypt.GenerateFromPassword([]byte("mass-invalid-user-placeholder"), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(append([]byte("mass.cursor.v1:"), key...))
	return &Service{Repo: repo, Cipher: cipher, TTL: ttl, cursorKey: sum[:], dummyHash: dummy, watchers: map[string]map[*watch]struct{}{}}, nil
}
func validName(v string) bool {
	return strings.TrimSpace(v) == v && utf8.ValidString(v) && len([]rune(v)) >= 1 && len([]rune(v)) <= 100 && !strings.ContainsAny(v, "\x00\r\n")
}
func validID(v string) bool { u, err := uuid.Parse(v); return err == nil && u.String() == v }
func (s *Service) CreateUser(ctx context.Context, name, password string) (User, error) {
	if !usernamePattern.MatchString(name) || len(password) < 12 || len(password) > 72 {
		return User{}, ErrInvalid
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return User{}, err
	}
	u := User{uuid.NewString(), name}
	return u, s.Repo.CreateUser(ctx, u, hash)
}
func tokenHash(token string) ([]byte, error) {
	b, err := hex.DecodeString(token)
	if err != nil || len(b) != 32 || token != hex.EncodeToString(b) {
		return nil, ErrUnauthenticated
	}
	sum := sha256.Sum256(b)
	return sum[:], nil
}
func (s *Service) Login(ctx context.Context, name, password string) (u User, token string, expires time.Time, err error) {
	if !usernamePattern.MatchString(name) || len(password) > 72 {
		return u, "", expires, ErrUnauthenticated
	}
	u, hash, err := s.Repo.Password(ctx, name)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return u, "", expires, err
	}
	missing := errors.Is(err, ErrNotFound)
	if missing {
		hash = s.dummyHash
	}
	compare := bcrypt.CompareHashAndPassword(hash, []byte(password))
	if missing || compare != nil {
		return User{}, "", expires, ErrUnauthenticated
	}
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return
	}
	token = hex.EncodeToString(b)
	hash, _ = tokenHash(token)
	expires = time.Now().UTC().Add(s.TTL)
	err = s.Repo.SaveSession(ctx, hash, u.ID, expires)
	return
}
func (s *Service) Me(ctx context.Context, token string) (User, error) {
	h, e := tokenHash(token)
	if e != nil {
		return User{}, e
	}
	return s.Repo.Session(ctx, h)
}
func (s *Service) Logout(ctx context.Context, token string) error {
	h, e := tokenHash(token)
	if e != nil {
		return nil
	}
	return s.Repo.DeleteSession(ctx, h)
}
func (s *Service) CreateSpace(ctx context.Context, actor, name string) (Space, error) {
	if !validName(name) {
		return Space{}, ErrInvalid
	}
	v := Space{ID: uuid.NewString(), Name: name, IsOwner: true, CreatedAt: time.Now().UTC().Truncate(time.Microsecond)}
	return v, s.Repo.CreateSpace(ctx, actor, v)
}
func (s *Service) AddMember(ctx context.Context, actor, space, name string) error {
	if !usernamePattern.MatchString(name) {
		return ErrInvalid
	}
	return s.Repo.AddMember(ctx, actor, space, name)
}
func (s *Service) RemoveMember(ctx context.Context, actor, space, user string) error {
	if !validID(user) {
		return ErrInvalid
	}
	if err := s.Repo.RemoveMember(ctx, actor, space, user); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for w := range s.watchers[space+":"+user] {
		w.cancel()
	}
	return nil
}

// WatchAccess registers before checking access, closing the subscribe/revoke
// race. Supervisor will wait for actual work exit; cancellation alone is not exit.
func (s *Service) WatchAccess(parent context.Context, actor, space string) (context.Context, func(), error) {
	ctx, cancel := context.WithCancel(parent)
	w := &watch{cancel}
	key := space + ":" + actor
	s.mu.Lock()
	if s.watchers[key] == nil {
		s.watchers[key] = map[*watch]struct{}{}
	}
	s.watchers[key][w] = struct{}{}
	s.mu.Unlock()
	release := func() {
		cancel()
		s.mu.Lock()
		delete(s.watchers[key], w)
		if len(s.watchers[key]) == 0 {
			delete(s.watchers, key)
		}
		s.mu.Unlock()
	}
	if err := s.CheckAccess(ctx, actor, space); err != nil {
		release()
		return nil, nil, err
	}
	return ctx, release, nil
}
func (s *Service) CheckAccess(ctx context.Context, actor, space string) error {
	return s.Repo.CheckAccess(ctx, actor, space, false)
}

func (s *Service) SaveModel(ctx context.Context, actor, space, id string, m Model) (Model, error) {
	create := id == ""
	if m.Provider != "ark" {
		return Model{}, ErrCapability
	}
	if !validName(m.Name) || !modelPattern.MatchString(m.ModelID) || m.ContextWindow <= m.MaxOutputTokens || m.ContextWindow > 2000000 || m.MaxOutputTokens < 1 || m.MaxOutputTokens > 1000000 || math.IsNaN(m.Temperature) || math.IsInf(m.Temperature, 0) || m.Temperature < 0 || m.Temperature > 2 || (!create && (!validID(id) || m.Revision < 1)) {
		return Model{}, ErrInvalid
	}
	if create {
		id = uuid.NewString()
	}
	baseURL, err := NormalizeBaseURL(m.BaseURL)
	if err != nil || len(m.APIKey) > 8192 || strings.ContainsAny(m.APIKey, "\x00\r\n") || (create && strings.TrimSpace(m.APIKey) == "") {
		return Model{}, ErrInvalid
	}
	if err := s.Repo.CheckAccess(ctx, actor, space, true); err != nil {
		return Model{}, err
	}
	m.BaseURL = baseURL
	// Never accept persisted encryption metadata from an input value.
	m.KeyID, m.Ciphertext, m.SecretBindingID = "", nil, ""
	if m.APIKey != "" {
		m.SecretBindingID = id
		m.KeyID, m.Ciphertext, err = s.Cipher.Seal(space, id, []byte(m.APIKey))
		if err != nil {
			return Model{}, err
		}
	}
	m.APIKey = ""
	m.ID = id
	if err := s.Repo.SaveModel(ctx, actor, space, m, create); err != nil {
		return Model{}, err
	}
	if create {
		m.Revision = 1
	} else {
		m.Revision++
	}
	m.KeyID, m.Ciphertext, m.SecretBindingID = "", nil, ""
	return m, nil
}

// Delete disables the model, retaining references for immutable future versions.
func (s *Service) DeleteModel(ctx context.Context, actor, space, id string, revision int64) error {
	m, err := s.Repo.Model(ctx, actor, space, id)
	if err != nil {
		return err
	}
	if m.Revision != revision {
		return ErrConflict
	}
	m.Enabled = false
	return s.Repo.SaveModel(ctx, actor, space, m, false)
}
func (s *Service) ModelSecret(ctx context.Context, actor, space, model string) ([]byte, error) {
	c, err := s.Repo.ResolveModelSecret(ctx, actor, space, model)
	if err != nil {
		return nil, err
	}
	return s.Cipher.Open(space, c.SecretBindingID, c.KeyID, c.Ciphertext)
}

// Cursor authentication binds live keyset pagination to actor, space and list.
func (s *Service) Query(scope, cursor string, limit int) (Query, error) {
	if limit < 1 || limit > 100 {
		return Query{}, ErrInvalid
	}
	q := Query{Limit: limit + 1}
	if cursor == "" {
		return q, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil || len(b) != 68 {
		return q, ErrInvalid
	}
	mac := hmac.New(sha256.New, s.cursorKey)
	mac.Write([]byte(scope + ":"))
	mac.Write(b[:36])
	if !hmac.Equal(b[36:], mac.Sum(nil)) || !validID(string(b[:36])) {
		return q, ErrInvalid
	}
	q.After = string(b[:36])
	return q, nil
}
func (s *Service) Cursor(scope, id string) string {
	mac := hmac.New(sha256.New, s.cursorKey)
	mac.Write([]byte(scope + ":" + id))
	return base64.RawURLEncoding.EncodeToString(append([]byte(id), mac.Sum(nil)...))
}
func Paginate[T any](s *Service, scope string, items []T, limit int, id func(T) string) Page[T] {
	page := Page[T]{Items: items}
	if len(items) > limit {
		page.Items = items[:limit]
		cursor := s.Cursor(scope, id(items[limit-1]))
		page.NextCursor = &cursor
	}
	return page
}

// Tool pages carry opaque, authenticated snapshot positions, separate from keyset IDs.
func (s *Service) SealPage(scope, payload string) string {
	mac := hmac.New(sha256.New, s.cursorKey)
	mac.Write([]byte("tool-page:" + scope + ":" + payload))
	return base64.RawURLEncoding.EncodeToString(append([]byte(payload), mac.Sum(nil)...))
}
func (s *Service) OpenPage(scope, token string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) <= 32 || len(raw) > 4096 {
		return "", ErrInvalid
	}
	payload := raw[:len(raw)-32]
	mac := hmac.New(sha256.New, s.cursorKey)
	mac.Write([]byte("tool-page:" + scope + ":" + string(payload)))
	if !hmac.Equal(raw[len(raw)-32:], mac.Sum(nil)) {
		return "", ErrInvalid
	}
	return string(payload), nil
}
