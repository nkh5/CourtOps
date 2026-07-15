# Short commands for the things done every session. Everything here is also
# written out in the README, so the Makefile is a convenience, not a dependency.

DB_URL ?= postgres://courtops:courtops@localhost:5433/courtops?sslmode=disable
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: help
help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-16s %s\n", $$1, $$2}'

.PHONY: db
db: ## Start the local PostgreSQL container
	docker compose up -d
	@echo "waiting for postgres..."
	@until docker exec courtops-db-dev pg_isready -U courtops -d courtops >/dev/null 2>&1; do sleep 1; done
	@echo "ready on localhost:5433"

.PHONY: db-stop
db-stop: ## Stop the local database (keeps the volume)
	docker compose down

.PHONY: migrate
migrate: ## Apply migrations to the local database
	DATABASE_URL="$(DB_URL)" go run ./cmd/courtops -migrate-only

.PHONY: run
run: ## Run the app locally with development sign-in
	DATABASE_URL="$(DB_URL)" \
	COURTOPS_ADDR=":8080" \
	COURTOPS_BASE_URL="http://localhost:8080" \
	COURTOPS_SESSION_SECRET="local-development-secret-32-bytes-long" \
	COURTOPS_ORGANIZERS="dev@example.com" \
	COURTOPS_DEV_ORGANIZER="dev@example.com" \
	COURTOPS_RATINGS=true \
	go run ./cmd/courtops

.PHONY: test
test: ## Run every test, including the PostgreSQL ones
	TEST_DATABASE_URL="$(DB_URL)" go test -race -count=1 ./...

.PHONY: test-unit
test-unit: ## Run only the tests that need no database
	TEST_DATABASE_URL= go test -race -count=1 ./internal/matchmaking/ ./internal/ratings/ ./internal/roster/ ./internal/config/ ./internal/web/

.PHONY: bench
bench: ## Benchmark the matcher on 12, 30 and 50 player rosters
	go test -run '^$$' -bench . -benchmem ./internal/matchmaking/

.PHONY: check
check: ## Everything CI runs
	gofmt -l . | tee /dev/stderr | (! read)
	go vet ./...
	$(MAKE) test

.PHONY: image
image: ## Build the release container image
	docker build -f deploy/Dockerfile --build-arg VERSION="$(VERSION)" -t courtops:$(VERSION) .

.PHONY: load
load: ## Measure the board under a synthetic polling load
	go run ./cmd/loadgen -url http://localhost:8080/board/1 -viewers 30 -interval 5s -duration 60s

.PHONY: load-db
load-db: ## Measure concurrent database snapshots (EVENT_ID defaults to 1)
	DATABASE_URL="$(DB_URL)" go run ./cmd/loadgen -mode snapshot -event $(or $(EVENT_ID),1) -viewers 30 -duration 60s

.PHONY: health
health: ## Verify public app and DB health (set HEALTH_URL)
	./scripts/check-health.sh "$(HEALTH_URL)"
