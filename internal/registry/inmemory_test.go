package registry

import (
	"context"
	"errors"
	"testing"
)

func validCard() ModelCard {
	return ModelCard{
		ModelID:       "qwen2.5-0.5b-instruct",
		Version:       "1",
		Provider:      ProviderSelfHosted,
		Task:          TaskChat,
		ContextWindow: 4096,
		Owner:         "platform",
	}
}

func TestModelCardValidate(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ModelCard)
		want   error
	}{
		{name: "valid", mutate: func(*ModelCard) {}},
		{name: "missing model id", mutate: func(c *ModelCard) { c.ModelID = "" }, want: ErrInvalid},
		{name: "missing version", mutate: func(c *ModelCard) { c.Version = "" }, want: ErrInvalid},
		{name: "missing owner", mutate: func(c *ModelCard) { c.Owner = "" }, want: ErrInvalid},
		{name: "zero context window", mutate: func(c *ModelCard) { c.ContextWindow = 0 }, want: ErrInvalid},
		{name: "unknown provider", mutate: func(c *ModelCard) { c.Provider = "openai-ish" }, want: ErrInvalid},
		{name: "unknown task", mutate: func(c *ModelCard) { c.Task = "summarize" }, want: ErrInvalid},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := validCard()
			tt.mutate(&c)
			if err := c.Validate(); !errors.Is(err, tt.want) {
				t.Fatalf("Validate() = %v, want error is %v", err, tt.want)
			}
		})
	}
}

func TestCanTransition(t *testing.T) {
	// The HLD §9 lifecycle: registered → shadow → canary → production → deprecated → retired,
	// plus rollbacks out of shadow/canary. Retired is terminal.
	tests := []struct {
		name string
		from RolloutState
		to   RolloutState
		want bool
	}{
		{name: "registered to shadow", from: StateRegistered, to: StateShadow, want: true},
		{name: "shadow to canary", from: StateShadow, to: StateCanary, want: true},
		{name: "canary to production", from: StateCanary, to: StateProduction, want: true},
		{name: "production to deprecated", from: StateProduction, to: StateDeprecated, want: true},
		{name: "deprecated to retired", from: StateDeprecated, to: StateRetired, want: true},
		{name: "deprecated back to production", from: StateDeprecated, to: StateProduction, want: true},
		{name: "canary rollback to shadow", from: StateCanary, to: StateShadow, want: true},
		{name: "same state is a no-op", from: StateProduction, to: StateProduction, want: true},

		{name: "registered straight to production", from: StateRegistered, to: StateProduction, want: false},
		{name: "shadow straight to production", from: StateShadow, to: StateProduction, want: false},
		{name: "production back to canary", from: StateProduction, to: StateCanary, want: false},
		{name: "retired is terminal", from: StateRetired, to: StateProduction, want: false},
		{name: "retired cannot be re-registered", from: StateRetired, to: StateRegistered, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CanTransition(tt.from, tt.to); got != tt.want {
				t.Fatalf("CanTransition(%q, %q) = %v, want %v", tt.from, tt.to, got, tt.want)
			}
		})
	}
}

func TestRegisterAndGetModel(t *testing.T) {
	ctx := context.Background()
	r := NewInMemoryRegistry()

	got, err := r.RegisterModel(ctx, validCard())
	if err != nil {
		t.Fatalf("RegisterModel(): %v", err)
	}
	if got.RolloutState != StateRegistered {
		t.Errorf("RolloutState = %q, want %q", got.RolloutState, StateRegistered)
	}
	if got.RegisteredAt.IsZero() {
		t.Error("RegisterModel() did not set RegisteredAt")
	}

	if _, err := r.RegisterModel(ctx, validCard()); !errors.Is(err, ErrAlreadyExists) {
		t.Errorf("duplicate RegisterModel() error = %v, want error is %v", err, ErrAlreadyExists)
	}
	if _, err := r.GetModel(ctx, got.Key()); err != nil {
		t.Errorf("GetModel(): %v", err)
	}
	if _, err := r.GetModel(ctx, ModelKey{ModelID: "nope", Version: "1"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetModel(unknown) error = %v, want error is %v", err, ErrNotFound)
	}
}

func TestListModelsFilters(t *testing.T) {
	ctx := context.Background()
	r := NewInMemoryRegistry()

	seed := []ModelCard{
		{ModelID: "a", Version: "1", Provider: ProviderSelfHosted, Task: TaskChat, ContextWindow: 4096, Owner: "platform"},
		{ModelID: "a", Version: "2", Provider: ProviderSelfHosted, Task: TaskChat, ContextWindow: 4096, Owner: "platform"},
		{ModelID: "b", Version: "1", Provider: ProviderBedrock, Task: TaskChat, ContextWindow: 200000, Owner: "platform"},
		{ModelID: "c", Version: "1", Provider: ProviderSelfHosted, Task: TaskEmbedding, ContextWindow: 512, Owner: "platform"},
	}
	for _, c := range seed {
		if _, err := r.RegisterModel(ctx, c); err != nil {
			t.Fatalf("seed RegisterModel(%s@%s): %v", c.ModelID, c.Version, err)
		}
	}

	tests := []struct {
		name   string
		filter ModelFilter
		want   []ModelKey
	}{
		{
			name: "no filter is ordered by id then version",
			want: []ModelKey{{"a", "1"}, {"a", "2"}, {"b", "1"}, {"c", "1"}},
		},
		{name: "by model id", filter: ModelFilter{ModelID: "a"}, want: []ModelKey{{"a", "1"}, {"a", "2"}}},
		{name: "by provider", filter: ModelFilter{Provider: ProviderBedrock}, want: []ModelKey{{"b", "1"}}},
		{name: "by task", filter: ModelFilter{Task: TaskEmbedding}, want: []ModelKey{{"c", "1"}}},
		{name: "by rollout state", filter: ModelFilter{RolloutState: StateProduction}, want: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := r.ListModels(ctx, tt.filter)
			if err != nil {
				t.Fatalf("ListModels(): %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("ListModels() returned %d cards, want %d", len(got), len(tt.want))
			}
			for i, key := range tt.want {
				if got[i].Key() != key {
					t.Errorf("ListModels()[%d] = %v, want %v", i, got[i].Key(), key)
				}
			}
		})
	}
}

func TestSetRolloutState(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name string
		path []RolloutState
		want error
	}{
		{name: "full promotion path", path: []RolloutState{StateShadow, StateCanary, StateProduction}},
		{name: "retire keeps the card resolvable", path: []RolloutState{StateDeprecated, StateRetired}},
		{name: "skipping shadow and canary", path: []RolloutState{StateProduction}, want: ErrInvalidTransition},
		{name: "unknown state", path: []RolloutState{"live"}, want: ErrInvalid},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewInMemoryRegistry()
			card, err := r.RegisterModel(ctx, validCard())
			if err != nil {
				t.Fatalf("RegisterModel(): %v", err)
			}

			for i, state := range tt.path {
				_, err = r.SetRolloutState(ctx, card.Key(), state)
				last := i == len(tt.path)-1
				if !last && err != nil {
					t.Fatalf("SetRolloutState(%q): %v", state, err)
				}
				if last && !errors.Is(err, tt.want) {
					t.Fatalf("SetRolloutState(%q) error = %v, want error is %v", state, err, tt.want)
				}
			}

			// Retired is not deleted (HLD §9): the card is still resolvable for audit.
			if _, err := r.GetModel(ctx, card.Key()); err != nil {
				t.Errorf("GetModel() after rollout: %v", err)
			}
		})
	}

	t.Run("unknown model", func(t *testing.T) {
		r := NewInMemoryRegistry()
		_, err := r.SetRolloutState(ctx, ModelKey{ModelID: "nope", Version: "1"}, StateShadow)
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("SetRolloutState() error = %v, want error is %v", err, ErrNotFound)
		}
	})
}

func TestDeploymentBindings(t *testing.T) {
	ctx := context.Background()
	r := NewInMemoryRegistry()
	card, err := r.RegisterModel(ctx, validCard())
	if err != nil {
		t.Fatalf("RegisterModel(): %v", err)
	}

	tests := []struct {
		name string
		in   DeploymentBinding
		want error
	}{
		{
			name: "valid vllm binding",
			in: DeploymentBinding{
				Model: card.Key(), Runtime: "vllm", Endpoint: "http://vllm:8000",
				Replicas: ReplicaPolicy{Min: 1, Max: 3}, Weight: 100,
			},
		},
		{name: "missing model key", in: DeploymentBinding{Runtime: "vllm"}, want: ErrInvalid},
		{name: "missing runtime", in: DeploymentBinding{Model: card.Key()}, want: ErrInvalid},
		{
			name: "negative weight",
			in:   DeploymentBinding{Model: card.Key(), Runtime: "vllm", Weight: -1},
			want: ErrInvalid,
		},
		{
			name: "min above max",
			in: DeploymentBinding{
				Model: card.Key(), Runtime: "vllm", Replicas: ReplicaPolicy{Min: 5, Max: 2},
			},
			want: ErrInvalid,
		},
		{
			name: "unknown model",
			in: DeploymentBinding{
				Model: ModelKey{ModelID: "nope", Version: "1"}, Runtime: "vllm",
			},
			want: ErrNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := r.BindDeployment(ctx, tt.in)
			if !errors.Is(err, tt.want) {
				t.Fatalf("BindDeployment() error = %v, want error is %v", err, tt.want)
			}
			if tt.want != nil {
				return
			}
			if got.ID == "" {
				t.Fatal("BindDeployment() did not assign an ID")
			}
			bindings, err := r.Deployments(ctx, card.Key())
			if err != nil {
				t.Fatalf("Deployments(): %v", err)
			}
			if len(bindings) != 1 || bindings[0].ID != got.ID {
				t.Fatalf("Deployments() = %+v, want the one binding just created", bindings)
			}
			if err := r.UnbindDeployment(ctx, got.ID); err != nil {
				t.Fatalf("UnbindDeployment(): %v", err)
			}
			if err := r.UnbindDeployment(ctx, got.ID); !errors.Is(err, ErrNotFound) {
				t.Fatalf("UnbindDeployment() twice error = %v, want error is %v", err, ErrNotFound)
			}
		})
	}
}
