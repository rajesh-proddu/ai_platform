// Package session is the state plane of the AI inference platform: HLD §7.
//
// It is ONE service with TWO scopes, not two services:
//
//   - session scope — turns, run state, TTL. Short-lived, read every turn (HLD §7.1).
//   - memory scope  — durable facts and their embeddings, with mandatory provenance (HLD §7.2).
//
// Both scopes sit behind a single [Store] because they share an identity model, a lifecycle and a
// consumer (the context assembler). Splitting them before a driver exists would be speculative;
// HLD §7 says to split later only if their scaling profiles diverge.
//
// P0 ships the in-memory implementation only. Redis (session scope) and pgvector (memory scope)
// are P1 — see [NewInMemoryStore].
package session

import (
	"errors"
	"fmt"
	"time"
)

// Errors returned by a [Store]. Callers compare with errors.Is.
var (
	ErrNotFound      = errors.New("session: not found")
	ErrAlreadyExists = errors.New("session: already exists")
	ErrInvalid       = errors.New("session: invalid argument")
)

// Role is the author of a [Turn], using the OpenAI chat-completions vocabulary because that is the
// gateway's caller-facing API (HLD §5.1).
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// RunState is the status of the run a session is executing.
//
// P0 covers the synchronous path only. Long-running runs — durable checkpointing, resumption after
// a pod restart, idempotency keys — are P1 (HLD §7.1).
type RunState string

const (
	RunPending   RunState = "pending"
	RunRunning   RunState = "running"
	RunSucceeded RunState = "succeeded"
	RunFailed    RunState = "failed"
)

// Session is the session-scope record: who is talking, what the run is doing, and when it expires.
type Session struct {
	ID string `json:"id"`
	// Caller is the identity the turns are billed and attributed to — a virtual key or a JWT
	// subject (HLD §5.2). It is also the cache/memory isolation boundary (HLD §8).
	Caller    string    `json:"caller"`
	RunState  RunState  `json:"run_state"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// ExpiresAt bounds the session scope. Zero means no expiry.
	ExpiresAt time.Time `json:"expires_at,omitzero"`
	// TokensIn/TokensOut are the running token account for this session — the cost primitive
	// budgets are enforced against (HLD §5.4, §11).
	TokensIn  int `json:"tokens_in"`
	TokensOut int `json:"tokens_out"`
}

// Turn is one message in a session: a prompt, a completion, or a tool result.
type Turn struct {
	ID         string    `json:"id"`
	Role       Role      `json:"role"`
	Content    string    `json:"content"`
	ToolCallID string    `json:"tool_call_id,omitempty"`
	TokensIn   int       `json:"tokens_in"`
	TokensOut  int       `json:"tokens_out"`
	CreatedAt  time.Time `json:"created_at"`
}

// MemoryKind distinguishes the two kinds of durable memory in HLD §7.2.
type MemoryKind string

const (
	// KindSemantic is an extracted fact or preference, retrieved by similarity.
	KindSemantic MemoryKind = "semantic"
	// KindEpisodic is a summary of a past session.
	KindEpisodic MemoryKind = "episodic"
)

// Provenance is mandatory on every memory row (HLD §7.2). Without it, memory accumulates stale and
// contradictory facts and the resulting quality loss is very hard to debug — so [Provenance.Validate]
// is enforced on write rather than left to callers.
type Provenance struct {
	// SourceSessionID is the session the fact was extracted from.
	SourceSessionID string `json:"source_session_id"`
	// ObservedAt is when the fact was observed, not when the row was written.
	ObservedAt time.Time `json:"observed_at"`
	// Confidence is in (0, 1].
	Confidence float64 `json:"confidence"`
	// TTL bounds how long the fact may be trusted. Zero means no expiry.
	TTL time.Duration `json:"ttl,omitzero"`
}

// Validate reports whether the provenance carries the fields HLD §7.2 makes mandatory.
func (p Provenance) Validate() error {
	switch {
	case p.SourceSessionID == "":
		return fmt.Errorf("%w: provenance.source_session_id is required", ErrInvalid)
	case p.ObservedAt.IsZero():
		return fmt.Errorf("%w: provenance.observed_at is required", ErrInvalid)
	case p.Confidence <= 0 || p.Confidence > 1:
		return fmt.Errorf("%w: provenance.confidence must be in (0,1], got %v", ErrInvalid, p.Confidence)
	case p.TTL < 0:
		return fmt.Errorf("%w: provenance.ttl must not be negative", ErrInvalid)
	default:
		return nil
	}
}

// Memory is a memory-scope record: a durable fact about a subject, plus how we came to believe it.
type Memory struct {
	ID string `json:"id"`
	// Subject is the agent or user the memory belongs to. Retrieval never crosses subjects.
	Subject string     `json:"subject"`
	Kind    MemoryKind `json:"kind"`
	Text    string     `json:"text"`
	// Embedding is populated asynchronously: extraction is itself an LLM call and must not sit on
	// the turn's critical path (HLD §7.2). P0 never fills it.
	Embedding  []float32  `json:"embedding,omitempty"`
	Provenance Provenance `json:"provenance"`
	CreatedAt  time.Time  `json:"created_at"`
}

// MemoryQuery selects memories for the context assembler (HLD §7.3).
//
// P0 filters on subject and kind only. Similarity search against pgvector is P1; the query vector
// belongs here when it lands.
type MemoryQuery struct {
	Subject string     `json:"subject"`
	Kind    MemoryKind `json:"kind,omitempty"`
	Limit   int        `json:"limit,omitempty"`
}
