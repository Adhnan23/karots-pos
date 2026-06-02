.PHONY: help dev build run templ test migrate seed db-up db-down docker-up docker-down tidy

help:
	@echo "Targets: db-up, migrate, seed, dev, build, run, test, templ, docker-up, docker-down"

# Generate Templ components then build a fully static, self-contained binary
# (static assets + migrations are embedded; only the binary + .env are needed).
build: templ
	CGO_ENABLED=0 go build -ldflags="-s -w" -o bin/karots-pos ./cmd/server

templ:
	templ generate

# Run locally (expects Postgres up via db-up and a .env file present).
run: templ
	set -a && . ./.env && set +a && go run ./cmd/server

dev: db-up templ
	set -a && . ./.env && set +a && go run ./cmd/server

test:
	go test ./...

# Apply migrations only, then exit.
migrate:
	set -a && . ./.env && set +a && go run ./cmd/server -migrate

# Seed starter data (admin/cashier users + sample products).
seed:
	set -a && . ./.env && set +a && go run ./cmd/server -seed

# Start/stop just the Postgres container.
db-up:
	docker compose up -d postgres

db-down:
	docker compose stop postgres

# Full stack in Docker.
docker-up:
	docker compose up --build -d

docker-down:
	docker compose down

tidy:
	go mod tidy
