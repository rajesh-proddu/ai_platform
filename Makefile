SHELL := /bin/bash
GO ?= go
SERVICES := stateservice registry
COMPOSE := docker compose -f deploy/local/docker-compose.yml
AGENTGATEWAY_VERSION ?= v1.5.0
AGENTGATEWAY_SCHEMA := https://raw.githubusercontent.com/agentgateway/agentgateway/$(AGENTGATEWAY_VERSION)/schema/config.json

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-22s\033[0m %s\n", $$1, $$2}'

.PHONY: build
build: ## Compile all packages
	$(GO) build ./...

.PHONY: test
test: ## Run unit tests
	$(GO) test ./...

.PHONY: vet
vet: ## Run go vet
	$(GO) vet ./...

.PHONY: fmt
fmt: ## Format all Go files
	gofmt -w .

.PHONY: fmt-check
fmt-check: ## Fail if any Go file is unformatted
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "unformatted:"; echo "$$out"; exit 1; fi

.PHONY: lint
lint: ## Run golangci-lint (requires golangci-lint v2 on PATH)
	golangci-lint run ./...

.PHONY: vuln
vuln: ## Run govulncheck
	$(GO) run golang.org/x/vuln/cmd/govulncheck@latest ./...

.PHONY: check
check: fmt-check vet build test ## Everything CI runs that needs no extra tooling

.PHONY: run-stateservice
run-stateservice: ## Run the state service on :8090
	$(GO) run ./cmd/stateservice

.PHONY: run-registry
run-registry: ## Run the model registry on :8091
	$(GO) run ./cmd/registry

.PHONY: docker
docker: ## Build container images for all services
	@for svc in $(SERVICES); do \
		docker build -f build/Dockerfile --build-arg SERVICE=$$svc -t ai-platform/$$svc:dev . || exit 1; \
	done

.PHONY: gateway-validate
gateway-validate: ## Validate gateway/config.yaml against the pinned agentgateway schema
	@curl -sSL -o /tmp/agentgateway-$(AGENTGATEWAY_VERSION).json $(AGENTGATEWAY_SCHEMA)
	@python3 -c "import json,yaml,jsonschema,sys; \
	  jsonschema.Draft202012Validator(json.load(open('/tmp/agentgateway-$(AGENTGATEWAY_VERSION).json'))) \
	    .validate(yaml.safe_load(open('gateway/config.yaml'))); \
	  print('gateway/config.yaml: valid against agentgateway $(AGENTGATEWAY_VERSION)')"

.PHONY: up
up: ## Start the local stack (agentgateway + vLLM + Redis + sample MCP server)
	$(COMPOSE) up -d

.PHONY: down
down: ## Stop the local stack
	$(COMPOSE) down

.PHONY: logs
logs: ## Tail local stack logs
	$(COMPOSE) logs -f
