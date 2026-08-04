# Image is tagged with the git SHA so every build is addressable and
# :latest never masks a stale image in the cluster.
TAG      ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo dev)
REGISTRY ?= registry.home.lab
IMAGE    := $(REGISTRY)/inventory-api:$(TAG)

DB_URL ?= $(DATABASE_URL)

.PHONY: help build run test test-short lint fmt tidy \
        docker docker-push k3s-import deploy undeploy \
        migrate-up migrate-down migrate-status seed

help: ## Show available targets
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) \
		| awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

## ---- development ----------------------------------------------------

build: ## Compile the binary into bin/
	go build -trimpath -o bin/api ./cmd/api

run: ## Run locally (expects .env to be sourced)
	go run ./cmd/api

test: ## Full test suite with the race detector
	go test -race -count=1 -coverprofile=coverage.out ./...

test-short: ## Unit tests only, skipping testcontainers
	go test -race -short ./...

lint: ## Static analysis
	go vet ./...
	golangci-lint run

fmt: ## Format and simplify
	gofmt -s -w .

tidy: ## Prune go.mod
	go mod tidy

## ---- database -------------------------------------------------------

migrate-up: ## Apply all pending migrations
	goose -dir migrations postgres "$(DB_URL)" up

migrate-down: ## Roll back the last migration
	goose -dir migrations postgres "$(DB_URL)" down

migrate-status: ## Show migration state
	goose -dir migrations postgres "$(DB_URL)" status

seed: ## Load the large synthetic dataset used for pagination benchmarks
	psql "$(DB_URL)" -f migrations/seed.sql

## ---- container ------------------------------------------------------

docker: ## Build the image (BuildKit required for cache mounts)
	DOCKER_BUILDKIT=1 docker build --build-arg VERSION=$(TAG) -t $(IMAGE) .

docker-push: docker ## Push to the local registry
	docker push $(IMAGE)

k3s-import: docker ## Import straight into k3s containerd (no registry)
	# -n=k8s.io is mandatory: ctr defaults to the "default" namespace,
	# which the kubelet cannot see, and the pod ends up in ErrImagePull.
	docker save $(IMAGE) | sudo k3s ctr -n=k8s.io images import -

## ---- kubernetes -----------------------------------------------------

deploy: ## Apply manifests with the current image tag
	kubectl apply -f k8s/configmap.yaml
	kubectl apply -f k8s/service.yaml
	kubectl apply -f k8s/pdb.yaml
	sed 's|inventory-api:CHANGEME|inventory-api:$(TAG)|' k8s/deployment.yaml \
		| kubectl apply -f -
	kubectl rollout status deployment/inventory-api --timeout=90s

undeploy: ## Remove the workload (Secret is left in place)
	kubectl delete -f k8s/ --ignore-not-found
