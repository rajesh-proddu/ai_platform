package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"sync"
	"time"
)

// InMemoryStore is the P0 [Store]: process-local, lost on restart, no eviction loop.
//
// TODO(P1): Redis-backed session scope (redis://…, keyed by session ID, TTL-bounded) — HLD §7.1.
// TODO(P1): pgvector-backed memory scope on the existing StatefulSet
// (pgvector.infra.svc.cluster.local:5432), with similarity search — HLD §7.2.
// Both are P1 so that P0 adds no dependency; this type is what they must stay drop-in compatible with.
type InMemoryStore struct {
	mu       sync.RWMutex
	sessions map[string]Session
	turns    map[string][]Turn
	memories map[string]Memory
}

// NewInMemoryStore returns an empty [InMemoryStore].
func NewInMemoryStore() *InMemoryStore {
	return &InMemoryStore{
		sessions: make(map[string]Session),
		turns:    make(map[string][]Turn),
		memories: make(map[string]Memory),
	}
}

var _ Store = (*InMemoryStore)(nil)

func (s *InMemoryStore) CreateSession(_ context.Context, in Session) (Session, error) {
	if in.Caller == "" {
		return Session{}, fmt.Errorf("%w: session.caller is required", ErrInvalid)
	}
	if in.ID == "" {
		in.ID = newID()
	}
	if in.RunState == "" {
		in.RunState = RunPending
	}
	now := time.Now().UTC()
	if in.CreatedAt.IsZero() {
		in.CreatedAt = now
	}
	in.UpdatedAt = now

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.sessions[in.ID]; ok {
		return Session{}, fmt.Errorf("%w: session %q", ErrAlreadyExists, in.ID)
	}
	s.sessions[in.ID] = in
	return in, nil
}

func (s *InMemoryStore) GetSession(_ context.Context, id string) (Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.getLocked(id)
}

func (s *InMemoryStore) SetRunState(_ context.Context, id string, state RunState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, err := s.getLocked(id)
	if err != nil {
		return err
	}
	sess.RunState = state
	sess.UpdatedAt = time.Now().UTC()
	s.sessions[id] = sess
	return nil
}

func (s *InMemoryStore) AppendTurn(_ context.Context, sessionID string, t Turn) (Turn, error) {
	if t.Role == "" {
		return Turn{}, fmt.Errorf("%w: turn.role is required", ErrInvalid)
	}
	if t.ID == "" {
		t.ID = newID()
	}
	if t.CreatedAt.IsZero() {
		t.CreatedAt = time.Now().UTC()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	sess, err := s.getLocked(sessionID)
	if err != nil {
		return Turn{}, err
	}
	s.turns[sessionID] = append(s.turns[sessionID], t)
	sess.TokensIn += t.TokensIn
	sess.TokensOut += t.TokensOut
	sess.UpdatedAt = t.CreatedAt
	s.sessions[sessionID] = sess
	return t, nil
}

func (s *InMemoryStore) Turns(_ context.Context, sessionID string) ([]Turn, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, err := s.getLocked(sessionID); err != nil {
		return nil, err
	}
	out := make([]Turn, len(s.turns[sessionID]))
	copy(out, s.turns[sessionID])
	return out, nil
}

func (s *InMemoryStore) DeleteSession(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.sessions[id]; !ok {
		return fmt.Errorf("%w: session %q", ErrNotFound, id)
	}
	delete(s.sessions, id)
	delete(s.turns, id)
	return nil
}

func (s *InMemoryStore) PutMemory(_ context.Context, m Memory) (Memory, error) {
	if m.Subject == "" {
		return Memory{}, fmt.Errorf("%w: memory.subject is required", ErrInvalid)
	}
	switch m.Kind {
	case KindSemantic, KindEpisodic:
	default:
		return Memory{}, fmt.Errorf("%w: memory.kind must be %q or %q, got %q",
			ErrInvalid, KindSemantic, KindEpisodic, m.Kind)
	}
	// HLD §7.2: provenance is mandatory. A memory row without it is rejected, not defaulted.
	if err := m.Provenance.Validate(); err != nil {
		return Memory{}, err
	}
	if m.ID == "" {
		m.ID = newID()
	}
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now().UTC()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.memories[m.ID] = m
	return m, nil
}

func (s *InMemoryStore) Memories(_ context.Context, q MemoryQuery) ([]Memory, error) {
	if q.Subject == "" {
		return nil, fmt.Errorf("%w: query.subject is required", ErrInvalid)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	var out []Memory
	for _, m := range s.memories {
		if m.Subject != q.Subject {
			continue
		}
		if q.Kind != "" && m.Kind != q.Kind {
			continue
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	if q.Limit > 0 && len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, nil
}

func (s *InMemoryStore) DeleteMemory(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.memories[id]; !ok {
		return fmt.Errorf("%w: memory %q", ErrNotFound, id)
	}
	delete(s.memories, id)
	return nil
}

// getLocked treats an elapsed ExpiresAt as absent. Expired entries are not reclaimed here;
// TODO(P1): Redis does this with a real TTL, which is the reason there is no sweeper in P0.
func (s *InMemoryStore) getLocked(id string) (Session, error) {
	sess, ok := s.sessions[id]
	if !ok {
		return Session{}, fmt.Errorf("%w: session %q", ErrNotFound, id)
	}
	if !sess.ExpiresAt.IsZero() && !time.Now().UTC().Before(sess.ExpiresAt) {
		return Session{}, fmt.Errorf("%w: session %q expired", ErrNotFound, id)
	}
	return sess, nil
}

func newID() string {
	var b [16]byte
	// crypto/rand.Read never returns an error as of Go 1.24.
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
