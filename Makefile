.PHONY: help test test-verbose test-race lint vet fmt tidy coverage \
        example example-pgx example-gorm docker-pg docker-pg-stop

# ── Default ───────────────────────────────────────────────────────────────────
help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-22s\033[0m %s\n", $$1, $$2}'

# ── Testing ───────────────────────────────────────────────────────────────────
test: ## Run unit tests (no DB required)
	go test ./... -count=1

test-verbose: ## Run unit tests with verbose output
	go test ./... -v -count=1

test-race: ## Run unit tests with race detector
	go test ./... -count=1 -race

coverage: ## Generate HTML coverage report and open it
	go test ./... -coverprofile=coverage.out
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report written to coverage.html"

test-integration: ## Run integration tests (requires DATABASE_URL)
	@[ -n "$$DATABASE_URL" ] || (echo "error: DATABASE_URL is not set"; exit 1)
	go test ./... -count=1 -tags=integration -timeout=120s

# ── Code quality ──────────────────────────────────────────────────────────────
vet: ## Run go vet
	go vet ./...

fmt: ## Format all Go source files
	go fmt ./...

tidy: ## Run go mod tidy
	go mod tidy

lint: ## Run golangci-lint (install: brew install golangci-lint)
	golangci-lint run ./...

# ── Local Postgres via Docker ─────────────────────────────────────────────────
docker-pg: ## Start a local Postgres 16 container
	@docker run -d \
		--name pgelect-postgres \
		-e POSTGRES_PASSWORD=postgres \
		-e POSTGRES_DB=mydb \
		-p 5432:5432 \
		postgres:16-alpine
	@echo "Postgres running — set: export DATABASE_URL=postgres://postgres:postgres@localhost:5432/mydb?sslmode=disable"

docker-pg-stop: ## Stop and remove the local Postgres container
	docker rm -f pgelect-postgres

# ── Demo examples ─────────────────────────────────────────────────────────────
example: ## Run demo with plain database/sql (requires DATABASE_URL)
	@[ -n "$$DATABASE_URL" ] || (echo "error: DATABASE_URL is not set"; exit 1)
	APP_NAME=demo POD_NAME=pod-a go run ./examples/demo --driver=stdlib

example-pgx: ## Run demo with pgx driver (requires DATABASE_URL)
	@[ -n "$$DATABASE_URL" ] || (echo "error: DATABASE_URL is not set"; exit 1)
	APP_NAME=demo POD_NAME=pod-a go run ./examples/demo --driver=pgx

example-gorm: ## Run demo with GORM driver (requires DATABASE_URL)
	@[ -n "$$DATABASE_URL" ] || (echo "error: DATABASE_URL is not set"; exit 1)
	APP_NAME=demo POD_NAME=pod-a go run ./examples/demo --driver=gorm
