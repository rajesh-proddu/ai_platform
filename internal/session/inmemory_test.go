package session

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestProvenanceValidate(t *testing.T) {
	observed := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		in   Provenance
		want error
	}{
		{
			name: "complete",
			in:   Provenance{SourceSessionID: "s1", ObservedAt: observed, Confidence: 0.9},
			want: nil,
		},
		{
			name: "with ttl",
			in:   Provenance{SourceSessionID: "s1", ObservedAt: observed, Confidence: 1, TTL: time.Hour},
			want: nil,
		},
		{
			name: "missing source session",
			in:   Provenance{ObservedAt: observed, Confidence: 0.9},
			want: ErrInvalid,
		},
		{
			name: "missing observed at",
			in:   Provenance{SourceSessionID: "s1", Confidence: 0.9},
			want: ErrInvalid,
		},
		{
			name: "zero confidence",
			in:   Provenance{SourceSessionID: "s1", ObservedAt: observed},
			want: ErrInvalid,
		},
		{
			name: "confidence above one",
			in:   Provenance{SourceSessionID: "s1", ObservedAt: observed, Confidence: 1.5},
			want: ErrInvalid,
		},
		{
			name: "negative ttl",
			in:   Provenance{SourceSessionID: "s1", ObservedAt: observed, Confidence: 0.5, TTL: -time.Second},
			want: ErrInvalid,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.in.Validate(); !errors.Is(err, tt.want) {
				t.Fatalf("Validate() = %v, want error is %v", err, tt.want)
			}
		})
	}
}

func TestPutMemoryRejectsIncompleteRecords(t *testing.T) {
	good := Provenance{
		SourceSessionID: "s1",
		ObservedAt:      time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC),
		Confidence:      0.8,
	}

	tests := []struct {
		name string
		in   Memory
		want error
	}{
		{
			name: "semantic with provenance",
			in:   Memory{Subject: "user-1", Kind: KindSemantic, Text: "prefers documentaries", Provenance: good},
			want: nil,
		},
		{
			name: "episodic with provenance",
			in:   Memory{Subject: "user-1", Kind: KindEpisodic, Text: "asked about sci-fi", Provenance: good},
			want: nil,
		},
		{
			name: "missing subject",
			in:   Memory{Kind: KindSemantic, Text: "x", Provenance: good},
			want: ErrInvalid,
		},
		{
			name: "unknown kind",
			in:   Memory{Subject: "user-1", Kind: "guess", Text: "x", Provenance: good},
			want: ErrInvalid,
		},
		{
			name: "missing provenance entirely",
			in:   Memory{Subject: "user-1", Kind: KindSemantic, Text: "x"},
			want: ErrInvalid,
		},
		{
			name: "provenance without confidence",
			in: Memory{Subject: "user-1", Kind: KindSemantic, Text: "x", Provenance: Provenance{
				SourceSessionID: "s1", ObservedAt: good.ObservedAt,
			}},
			want: ErrInvalid,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewInMemoryStore()
			got, err := s.PutMemory(context.Background(), tt.in)
			if !errors.Is(err, tt.want) {
				t.Fatalf("PutMemory() error = %v, want error is %v", err, tt.want)
			}
			if tt.want != nil {
				return
			}
			if got.ID == "" {
				t.Error("PutMemory() did not assign an ID")
			}
			if got.CreatedAt.IsZero() {
				t.Error("PutMemory() did not set CreatedAt")
			}
		})
	}
}

func TestMemoriesFilters(t *testing.T) {
	prov := Provenance{
		SourceSessionID: "s1",
		ObservedAt:      time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC),
		Confidence:      0.8,
	}
	seed := []Memory{
		{ID: "a", Subject: "user-1", Kind: KindSemantic, Text: "a", Provenance: prov, CreatedAt: time.Unix(3, 0).UTC()},
		{ID: "b", Subject: "user-1", Kind: KindEpisodic, Text: "b", Provenance: prov, CreatedAt: time.Unix(2, 0).UTC()},
		{ID: "c", Subject: "user-2", Kind: KindSemantic, Text: "c", Provenance: prov, CreatedAt: time.Unix(1, 0).UTC()},
	}

	tests := []struct {
		name    string
		query   MemoryQuery
		wantIDs []string
		wantErr error
	}{
		{name: "by subject, newest first", query: MemoryQuery{Subject: "user-1"}, wantIDs: []string{"a", "b"}},
		{name: "by subject and kind", query: MemoryQuery{Subject: "user-1", Kind: KindEpisodic}, wantIDs: []string{"b"}},
		{name: "limit", query: MemoryQuery{Subject: "user-1", Limit: 1}, wantIDs: []string{"a"}},
		{name: "other subject is isolated", query: MemoryQuery{Subject: "user-2"}, wantIDs: []string{"c"}},
		{name: "unknown subject", query: MemoryQuery{Subject: "nobody"}, wantIDs: nil},
		{name: "subject required", query: MemoryQuery{}, wantErr: ErrInvalid},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			s := NewInMemoryStore()
			for _, m := range seed {
				if _, err := s.PutMemory(ctx, m); err != nil {
					t.Fatalf("seed PutMemory(%s): %v", m.ID, err)
				}
			}

			got, err := s.Memories(ctx, tt.query)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Memories() error = %v, want error is %v", err, tt.wantErr)
			}
			if tt.wantErr != nil {
				return
			}
			if len(got) != len(tt.wantIDs) {
				t.Fatalf("Memories() returned %d rows, want %d", len(got), len(tt.wantIDs))
			}
			for i, id := range tt.wantIDs {
				if got[i].ID != id {
					t.Errorf("Memories()[%d].ID = %q, want %q", i, got[i].ID, id)
				}
			}
		})
	}
}

func TestSessionScope(t *testing.T) {
	ctx := context.Background()

	t.Run("create requires a caller", func(t *testing.T) {
		s := NewInMemoryStore()
		if _, err := s.CreateSession(ctx, Session{}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("CreateSession() error = %v, want error is %v", err, ErrInvalid)
		}
	})

	t.Run("create defaults and get round-trip", func(t *testing.T) {
		s := NewInMemoryStore()
		created, err := s.CreateSession(ctx, Session{Caller: "key-1"})
		if err != nil {
			t.Fatalf("CreateSession(): %v", err)
		}
		if created.ID == "" {
			t.Error("CreateSession() did not assign an ID")
		}
		if created.RunState != RunPending {
			t.Errorf("RunState = %q, want %q", created.RunState, RunPending)
		}
		got, err := s.GetSession(ctx, created.ID)
		if err != nil {
			t.Fatalf("GetSession(): %v", err)
		}
		if got.ID != created.ID {
			t.Errorf("GetSession().ID = %q, want %q", got.ID, created.ID)
		}
	})

	t.Run("duplicate id", func(t *testing.T) {
		s := NewInMemoryStore()
		if _, err := s.CreateSession(ctx, Session{ID: "dup", Caller: "key-1"}); err != nil {
			t.Fatalf("CreateSession(): %v", err)
		}
		if _, err := s.CreateSession(ctx, Session{ID: "dup", Caller: "key-1"}); !errors.Is(err, ErrAlreadyExists) {
			t.Fatalf("CreateSession() error = %v, want error is %v", err, ErrAlreadyExists)
		}
	})

	t.Run("expired session reads as not found", func(t *testing.T) {
		s := NewInMemoryStore()
		created, err := s.CreateSession(ctx, Session{
			Caller:    "key-1",
			ExpiresAt: time.Now().UTC().Add(-time.Minute),
		})
		if err != nil {
			t.Fatalf("CreateSession(): %v", err)
		}
		if _, err := s.GetSession(ctx, created.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("GetSession() error = %v, want error is %v", err, ErrNotFound)
		}
	})

	t.Run("turns accumulate token accounting", func(t *testing.T) {
		s := NewInMemoryStore()
		created, err := s.CreateSession(ctx, Session{Caller: "key-1"})
		if err != nil {
			t.Fatalf("CreateSession(): %v", err)
		}
		for _, turn := range []Turn{
			{Role: RoleUser, Content: "hi", TokensIn: 5},
			{Role: RoleAssistant, Content: "hello", TokensOut: 7},
		} {
			if _, err := s.AppendTurn(ctx, created.ID, turn); err != nil {
				t.Fatalf("AppendTurn(): %v", err)
			}
		}

		turns, err := s.Turns(ctx, created.ID)
		if err != nil {
			t.Fatalf("Turns(): %v", err)
		}
		if len(turns) != 2 || turns[0].Role != RoleUser || turns[1].Role != RoleAssistant {
			t.Fatalf("Turns() = %+v, want user then assistant", turns)
		}

		got, err := s.GetSession(ctx, created.ID)
		if err != nil {
			t.Fatalf("GetSession(): %v", err)
		}
		if got.TokensIn != 5 || got.TokensOut != 7 {
			t.Errorf("token account = (%d,%d), want (5,7)", got.TokensIn, got.TokensOut)
		}
	})

	t.Run("operations on unknown sessions", func(t *testing.T) {
		s := NewInMemoryStore()
		if _, err := s.GetSession(ctx, "nope"); !errors.Is(err, ErrNotFound) {
			t.Errorf("GetSession() error = %v, want error is %v", err, ErrNotFound)
		}
		if err := s.SetRunState(ctx, "nope", RunRunning); !errors.Is(err, ErrNotFound) {
			t.Errorf("SetRunState() error = %v, want error is %v", err, ErrNotFound)
		}
		if _, err := s.AppendTurn(ctx, "nope", Turn{Role: RoleUser}); !errors.Is(err, ErrNotFound) {
			t.Errorf("AppendTurn() error = %v, want error is %v", err, ErrNotFound)
		}
		if err := s.DeleteSession(ctx, "nope"); !errors.Is(err, ErrNotFound) {
			t.Errorf("DeleteSession() error = %v, want error is %v", err, ErrNotFound)
		}
	})

	t.Run("run state transitions are recorded", func(t *testing.T) {
		s := NewInMemoryStore()
		created, err := s.CreateSession(ctx, Session{Caller: "key-1"})
		if err != nil {
			t.Fatalf("CreateSession(): %v", err)
		}
		if err := s.SetRunState(ctx, created.ID, RunSucceeded); err != nil {
			t.Fatalf("SetRunState(): %v", err)
		}
		got, err := s.GetSession(ctx, created.ID)
		if err != nil {
			t.Fatalf("GetSession(): %v", err)
		}
		if got.RunState != RunSucceeded {
			t.Errorf("RunState = %q, want %q", got.RunState, RunSucceeded)
		}
	})
}
