# ---------------------------------------------------------------------------
# User Management API — developer tasks
# ---------------------------------------------------------------------------
MODULE      := user-management-api
BIN_DIR     := bin
APP_BIN     := $(BIN_DIR)/api
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT      ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
BUILD_DATE  := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS     := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.buildDate=$(BUILD_DATE)
COMPOSE     := docker compose -f deploy/docker-compose.yml --env-file .env

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show every available target
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

# ---------------------------------------------------------------------------
# build / run
# ---------------------------------------------------------------------------
.PHONY: build
build: ## Compile the binary into bin/api
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(APP_BIN) ./cmd/api

.PHONY: run
run: ## Run the app locally (needs .env and a reachable MongoDB)
	go run ./cmd/api

.PHONY: tidy
tidy: ## go mod tidy
	go mod tidy

# ---------------------------------------------------------------------------
# test
# ---------------------------------------------------------------------------
.PHONY: test
test: ## Run every unit test (no MongoDB needed) with the race detector
	go test ./... -race -count=1 -cover

.PHONY: test-integration
test-integration: ## Run the integration tests against a real MongoDB via testcontainers (needs Docker)
	go test ./... -race -count=1 -tags=integration -run Integration -v

.PHONY: cover
cover: ## Per-package coverage report plus an HTML file
	go test ./... -count=1 -coverprofile=coverage.out -covermode=atomic
	@go tool cover -func=coverage.out | tail -40
	@go tool cover -html=coverage.out -o coverage.html
	@echo "open coverage.html to browse it"

.PHONY: vet
vet: ## go vet
	go vet ./...

.PHONY: lint
lint: ## Run golangci-lint (install it first with: make tools)
	golangci-lint run ./...

.PHONY: check
check: vet lint test ## vet + lint + test

# ---------------------------------------------------------------------------
# protobuf / gRPC
# ---------------------------------------------------------------------------
.PHONY: proto
proto: ## Generate the Go code from proto/user/v1/user.proto
	protoc --proto_path=proto \
		--go_out=. --go_opt=module=$(MODULE) \
		--go-grpc_out=. --go-grpc_opt=module=$(MODULE) \
		proto/user/v1/user.proto
	@echo "generated: internal/adapter/grpcapi/pb/user/v1"

.PHONY: tools
tools: ## Install the dev tools (protoc plugins, grpcurl, golangci-lint)
	go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
	go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
	go install github.com/fullstorydev/grpcurl/cmd/grpcurl@latest
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest

# ---------------------------------------------------------------------------
# docker
# ---------------------------------------------------------------------------
.PHONY: docker-up
docker-up: ## Build and start api and mongo with docker compose
	@test -f .env || cp .env.example .env
	$(COMPOSE) up --build -d
	@echo "API:  http://localhost:8080  (healthz /healthz, readyz /readyz)"
	@echo "gRPC: localhost:50051"

.PHONY: docker-down
docker-down: ## Stop and remove the containers (the volume is kept)
	$(COMPOSE) down

.PHONY: docker-clean
docker-clean: ## Stop and remove the containers and the MongoDB data volume
	$(COMPOSE) down -v

.PHONY: docker-logs
docker-logs: ## Follow the api logs (shows user_count_report every 10s)
	$(COMPOSE) logs -f api

# ---------------------------------------------------------------------------
# utils
# ---------------------------------------------------------------------------
.PHONY: secret
secret: ## Generate a fresh JWT_SECRET
	@openssl rand -base64 48

.PHONY: rename-module
rename-module: ## Change the module path before pushing to GitHub: make rename-module NEW=github.com/you/repo
	@test -n "$(NEW)" || (echo "usage: make rename-module NEW=github.com/you/repo"; exit 1)
	go mod edit -module $(NEW)
	@grep -rl '$(MODULE)/' --include='*.go' --include='*.proto' --include='Makefile' . | xargs sed -i '' 's|$(MODULE)/|$(NEW)/|g'
	@sed -i '' 's|^MODULE      := .*|MODULE      := $(NEW)|' Makefile
	go mod tidy
	@echo "module renamed to $(NEW) — run make test again to confirm"

.PHONY: clean
clean: ## Remove build artifacts
	rm -rf $(BIN_DIR) coverage.out coverage.html
