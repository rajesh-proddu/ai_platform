// Package registry is the model registry plane: HLD §9.
//
// It answers three questions: what models exist, which version is live where, and is it allowed to
// be. Aliases in the gateway resolve to deployment bindings held here, which is what makes a model
// rollout a registry write rather than a redeploy (HLD §5.5, §9).
//
// P0 ships types, the [Registry] interface, lifecycle rules and an in-memory implementation.
// Durable metadata (MySQL/Postgres) and S3 artifact checksum verification are P1.
package registry

import (
	"errors"
	"fmt"
	"time"
)

// Errors returned by a [Registry]. Callers compare with errors.Is.
var (
	ErrNotFound          = errors.New("registry: not found")
	ErrAlreadyExists     = errors.New("registry: already exists")
	ErrInvalid           = errors.New("registry: invalid argument")
	ErrInvalidTransition = errors.New("registry: invalid rollout transition")
)

// Provider is where a model version actually runs (HLD §9).
//
// Local vLLM and a GPU-backed vLLM are both [ProviderSelfHosted]; they differ in the deployment
// binding's runtime and hardware profile, not in the model card.
type Provider string

const (
	ProviderSelfHosted Provider = "self-hosted"
	ProviderBedrock    Provider = "bedrock"
	ProviderOllama     Provider = "ollama"
	ProviderAnthropic  Provider = "anthropic"
)

// Task is what the model is for (HLD §9).
type Task string

const (
	TaskChat      Task = "chat"
	TaskEmbedding Task = "embedding"
	TaskRerank    Task = "rerank"
)

// RolloutState is the model lifecycle of HLD §9:
//
//	registered → shadow → canary → production → deprecated → retired
//
// Retire is not delete: a retired version stays resolvable so an audited output can still be
// attributed to the model that produced it.
type RolloutState string

const (
	StateRegistered RolloutState = "registered"
	StateShadow     RolloutState = "shadow"
	StateCanary     RolloutState = "canary"
	StateProduction RolloutState = "production"
	StateDeprecated RolloutState = "deprecated"
	StateRetired    RolloutState = "retired"
)

// allowedTransitions is the lifecycle graph. Forward moves follow HLD §9; the backward edges from
// shadow/canary are rollbacks, which the phasing needs and which promotion gates depend on.
var allowedTransitions = map[RolloutState][]RolloutState{
	StateRegistered: {StateShadow, StateCanary, StateDeprecated},
	StateShadow:     {StateCanary, StateRegistered, StateDeprecated},
	StateCanary:     {StateProduction, StateShadow, StateDeprecated},
	StateProduction: {StateDeprecated},
	StateDeprecated: {StateRetired, StateProduction},
	StateRetired:    {},
}

// CanTransition reports whether from → to is a legal rollout move.
func CanTransition(from, to RolloutState) bool {
	if from == to {
		return true
	}
	for _, next := range allowedTransitions[from] {
		if next == to {
			return true
		}
	}
	return false
}

// ModelCard is one model at one version — the HLD §9 field list, verbatim.
type ModelCard struct {
	ModelID       string   `json:"model_id"`
	Version       string   `json:"version"`
	Provider      Provider `json:"provider"`
	Task          Task     `json:"task"`
	ContextWindow int      `json:"context_window"`
	Quantization  string   `json:"quantization,omitempty"`
	// WeightsURI points at the versioned, checksummed artifact in S3. Empty for hosted providers.
	WeightsURI string `json:"weights_uri,omitempty"`
	// Checksum is verified at pod start: a model artifact is executable content, and silently
	// loading a mutated one is the serving equivalent of pulling an unpinned image (HLD §9).
	// TODO(P1): the verification itself belongs in the serving path, not here.
	Checksum  string `json:"checksum,omitempty"`
	Tokenizer string `json:"tokenizer,omitempty"`
	License   string `json:"license,omitempty"`
	// EvalScorecardRef points at the ADLC eval scorecard. The registry stores the reference and
	// gates promotion on it; it does not redefine eval policy (HLD §9).
	EvalScorecardRef string `json:"eval_scorecard_ref,omitempty"`
	// CostPer1KTokens is the HLD §9 field. TODO(P1): metering needs separate input/output rates.
	CostPer1KTokens float64      `json:"cost_per_1k_tokens"`
	Owner           string       `json:"owner"`
	RolloutState    RolloutState `json:"rollout_state"`
	RegisteredAt    time.Time    `json:"registered_at"`
	UpdatedAt       time.Time    `json:"updated_at"`
}

// Key identifies a model card.
func (c ModelCard) Key() ModelKey { return ModelKey{ModelID: c.ModelID, Version: c.Version} }

// Validate checks the fields a card cannot be useful without.
func (c ModelCard) Validate() error {
	switch {
	case c.ModelID == "":
		return fmt.Errorf("%w: model_id is required", ErrInvalid)
	case c.Version == "":
		return fmt.Errorf("%w: version is required", ErrInvalid)
	case c.Owner == "":
		return fmt.Errorf("%w: owner is required", ErrInvalid)
	case c.ContextWindow <= 0:
		return fmt.Errorf("%w: context_window must be positive", ErrInvalid)
	}
	switch c.Provider {
	case ProviderSelfHosted, ProviderBedrock, ProviderOllama, ProviderAnthropic:
	default:
		return fmt.Errorf("%w: unknown provider %q", ErrInvalid, c.Provider)
	}
	switch c.Task {
	case TaskChat, TaskEmbedding, TaskRerank:
	default:
		return fmt.Errorf("%w: unknown task %q", ErrInvalid, c.Task)
	}
	return nil
}

// ModelKey addresses one model version.
type ModelKey struct {
	ModelID string `json:"model_id"`
	Version string `json:"version"`
}

// HardwareProfile is the hardware a deployment needs. Empty for hosted providers.
type HardwareProfile struct {
	// Accelerator is the GPU class, e.g. "a10g". Empty means CPU-only.
	Accelerator string `json:"accelerator,omitempty"`
	Count       int    `json:"count,omitempty"`
}

// ReplicaPolicy bounds how many replicas a deployment runs.
//
// Min > 0 is the warm-pool decision of HLD §6: GPU cold start is minutes, so scale-to-zero trades a
// standing bill for a multi-minute first request.
type ReplicaPolicy struct {
	Min int `json:"min"`
	Max int `json:"max"`
}

// DeploymentBinding is `model_version → serving runtime + hardware profile + replica policy`
// (HLD §9). This is what the gateway router resolves an alias to.
type DeploymentBinding struct {
	ID    string   `json:"id"`
	Model ModelKey `json:"model"`
	// Runtime is the serving stack, e.g. "vllm" or "bedrock".
	Runtime string `json:"runtime"`
	// Endpoint is the OpenAI-compatible base URL the gateway routes to. Empty for providers the
	// gateway addresses by name rather than by URL.
	Endpoint string          `json:"endpoint,omitempty"`
	Hardware HardwareProfile `json:"hardware,omitzero"`
	Replicas ReplicaPolicy   `json:"replicas,omitzero"`
	// Weight is the share of alias traffic this binding takes — the canary dial of HLD §5.5.
	Weight    int       `json:"weight"`
	CreatedAt time.Time `json:"created_at"`
}

// Validate checks the fields a binding cannot be useful without.
func (b DeploymentBinding) Validate() error {
	switch {
	case b.Model.ModelID == "" || b.Model.Version == "":
		return fmt.Errorf("%w: model.model_id and model.version are required", ErrInvalid)
	case b.Runtime == "":
		return fmt.Errorf("%w: runtime is required", ErrInvalid)
	case b.Weight < 0:
		return fmt.Errorf("%w: weight must not be negative", ErrInvalid)
	case b.Replicas.Min < 0 || b.Replicas.Max < 0:
		return fmt.Errorf("%w: replica bounds must not be negative", ErrInvalid)
	case b.Replicas.Max > 0 && b.Replicas.Min > b.Replicas.Max:
		return fmt.Errorf("%w: replicas.min must not exceed replicas.max", ErrInvalid)
	default:
		return nil
	}
}

// ModelFilter narrows a model listing. Zero-valued fields are ignored.
type ModelFilter struct {
	ModelID      string       `json:"model_id,omitempty"`
	Provider     Provider     `json:"provider,omitempty"`
	Task         Task         `json:"task,omitempty"`
	RolloutState RolloutState `json:"rollout_state,omitempty"`
}
