package registry

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"sync"
	"time"
)

// InMemoryRegistry is the P0 [Registry]: process-local and lost on restart.
//
// TODO(P1): durable metadata store (MySQL/Postgres, per HLD §9) behind the same interface.
type InMemoryRegistry struct {
	mu          sync.RWMutex
	models      map[ModelKey]ModelCard
	deployments map[string]DeploymentBinding
}

// NewInMemoryRegistry returns an empty [InMemoryRegistry].
func NewInMemoryRegistry() *InMemoryRegistry {
	return &InMemoryRegistry{
		models:      make(map[ModelKey]ModelCard),
		deployments: make(map[string]DeploymentBinding),
	}
}

var _ Registry = (*InMemoryRegistry)(nil)

func (r *InMemoryRegistry) RegisterModel(_ context.Context, c ModelCard) (ModelCard, error) {
	if err := c.Validate(); err != nil {
		return ModelCard{}, err
	}
	now := time.Now().UTC()
	c.RolloutState = StateRegistered
	if c.RegisteredAt.IsZero() {
		c.RegisteredAt = now
	}
	c.UpdatedAt = now

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.models[c.Key()]; ok {
		return ModelCard{}, fmt.Errorf("%w: model %s@%s", ErrAlreadyExists, c.ModelID, c.Version)
	}
	r.models[c.Key()] = c
	return c, nil
}

func (r *InMemoryRegistry) GetModel(_ context.Context, key ModelKey) (ModelCard, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.models[key]
	if !ok {
		return ModelCard{}, fmt.Errorf("%w: model %s@%s", ErrNotFound, key.ModelID, key.Version)
	}
	return c, nil
}

func (r *InMemoryRegistry) ListModels(_ context.Context, f ModelFilter) ([]ModelCard, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var out []ModelCard
	for _, c := range r.models {
		if f.ModelID != "" && c.ModelID != f.ModelID {
			continue
		}
		if f.Provider != "" && c.Provider != f.Provider {
			continue
		}
		if f.Task != "" && c.Task != f.Task {
			continue
		}
		if f.RolloutState != "" && c.RolloutState != f.RolloutState {
			continue
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ModelID == out[j].ModelID {
			return out[i].Version < out[j].Version
		}
		return out[i].ModelID < out[j].ModelID
	})
	return out, nil
}

func (r *InMemoryRegistry) SetRolloutState(_ context.Context, key ModelKey, state RolloutState) (ModelCard, error) {
	if _, known := allowedTransitions[state]; !known {
		return ModelCard{}, fmt.Errorf("%w: unknown rollout state %q", ErrInvalid, state)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.models[key]
	if !ok {
		return ModelCard{}, fmt.Errorf("%w: model %s@%s", ErrNotFound, key.ModelID, key.Version)
	}
	if !CanTransition(c.RolloutState, state) {
		return ModelCard{}, fmt.Errorf("%w: %s → %s", ErrInvalidTransition, c.RolloutState, state)
	}
	c.RolloutState = state
	c.UpdatedAt = time.Now().UTC()
	r.models[key] = c
	return c, nil
}

func (r *InMemoryRegistry) BindDeployment(_ context.Context, b DeploymentBinding) (DeploymentBinding, error) {
	if err := b.Validate(); err != nil {
		return DeploymentBinding{}, err
	}
	if b.ID == "" {
		b.ID = newID()
	}
	if b.CreatedAt.IsZero() {
		b.CreatedAt = time.Now().UTC()
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.models[b.Model]; !ok {
		return DeploymentBinding{}, fmt.Errorf("%w: model %s@%s", ErrNotFound, b.Model.ModelID, b.Model.Version)
	}
	if _, ok := r.deployments[b.ID]; ok {
		return DeploymentBinding{}, fmt.Errorf("%w: deployment %q", ErrAlreadyExists, b.ID)
	}
	r.deployments[b.ID] = b
	return b, nil
}

func (r *InMemoryRegistry) Deployments(_ context.Context, key ModelKey) ([]DeploymentBinding, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var out []DeploymentBinding
	for _, b := range r.deployments {
		if b.Model == key {
			out = append(out, b)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (r *InMemoryRegistry) UnbindDeployment(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.deployments[id]; !ok {
		return fmt.Errorf("%w: deployment %q", ErrNotFound, id)
	}
	delete(r.deployments, id)
	return nil
}

func newID() string {
	var b [16]byte
	// crypto/rand.Read never returns an error as of Go 1.24.
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
