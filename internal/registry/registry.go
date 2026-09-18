package registry

import "context"

// Registry is the model registry interface of HLD §9.
//
// Retirement is a state, never a delete: retired versions stay resolvable for audit
// reproducibility, so there is deliberately no DeleteModel method.
type Registry interface {
	// RegisterModel stores a new model card in state [StateRegistered].
	RegisterModel(ctx context.Context, c ModelCard) (ModelCard, error)
	// GetModel returns one model version, or [ErrNotFound].
	GetModel(ctx context.Context, key ModelKey) (ModelCard, error)
	// ListModels returns the cards matching a filter, ordered by model ID then version.
	ListModels(ctx context.Context, f ModelFilter) ([]ModelCard, error)
	// SetRolloutState advances a model through the lifecycle. It returns
	// [ErrInvalidTransition] for a move the lifecycle graph does not allow.
	SetRolloutState(ctx context.Context, key ModelKey, state RolloutState) (ModelCard, error)

	// BindDeployment attaches a serving deployment to a model version.
	BindDeployment(ctx context.Context, b DeploymentBinding) (DeploymentBinding, error)
	// Deployments returns the bindings for one model version.
	Deployments(ctx context.Context, key ModelKey) ([]DeploymentBinding, error)
	// UnbindDeployment removes one binding.
	UnbindDeployment(ctx context.Context, id string) error
}
