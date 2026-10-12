package session

import "context"

// Store is the state plane's single interface, spanning both scopes of HLD §7.
//
// Implementations must reject a [Memory] whose [Provenance] does not validate.
type Store interface {
	// --- session scope (HLD §7.1) ---

	// CreateSession stores a new session. It returns [ErrAlreadyExists] if the ID is taken.
	CreateSession(ctx context.Context, s Session) (Session, error)
	// GetSession returns a session, or [ErrNotFound] if it is unknown or expired.
	GetSession(ctx context.Context, id string) (Session, error)
	// SetRunState updates the run status of a session.
	SetRunState(ctx context.Context, id string, state RunState) error
	// AppendTurn adds a turn and folds its token counts into the session's account.
	AppendTurn(ctx context.Context, sessionID string, t Turn) (Turn, error)
	// Turns returns a session's turns in insertion order.
	Turns(ctx context.Context, sessionID string) ([]Turn, error)
	// DeleteSession removes a session and its turns.
	DeleteSession(ctx context.Context, id string) error

	// --- memory scope (HLD §7.2) ---

	// PutMemory stores a durable fact. It returns [ErrInvalid] if provenance is missing or
	// malformed — provenance is mandatory, not advisory.
	PutMemory(ctx context.Context, m Memory) (Memory, error)
	// Memories returns the memories matching a query, newest first.
	Memories(ctx context.Context, q MemoryQuery) ([]Memory, error)
	// DeleteMemory removes one memory.
	DeleteMemory(ctx context.Context, id string) error
}
