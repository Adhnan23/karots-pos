.PHONY: help dev watch css-watch build build-windows bootstrap run templ css test migrate seed demo reset reset-seed reset-demo db-up db-down docker-up docker-down tidy

help:
	@echo "Targets: db-up, migrate, seed, demo, reset, reset-seed, reset-demo, dev, watch, css-watch, build, build-windows, bootstrap, run, test, templ, css, docker-up, docker-down"

# Generate Templ components and the stylesheet, then build a fully static,
# self-contained binary (static assets + migrations are embedded; only the
# binary + .env are needed).
build: templ css
	CGO_ENABLED=0 go build -ldflags="-s -w" -o bin/karots-pos ./cmd/server

# Cross-compile a self-contained Windows executable. Printing uses the Windows
# print spooler (RAW) via winspool — see internal/printing/printing_windows.go.
build-windows: templ css
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -ldflags="-s -w" -o bin/karots-pos.exe ./cmd/server

# Build a per-shop binary with a chosen plugin set compiled in. Interactive by
# default; pass ARGS for non-interactive, e.g.
#   make bootstrap ARGS="-plugins recharge -os windows -name acme-pos"
# The bootstrapper rewrites cmd/server/enabled_plugins.go, builds, then restores
# it. Output (binary + merged .env.sample) lands in dist/.
bootstrap:
	go run ./cmd/bootstrap $(ARGS)

templ:
	templ generate

# Compile the minified Tailwind stylesheet (replaces the runtime CDN). Needs
# Node/npx available at build time only — not at runtime.
css:
	npx -y tailwindcss@3 -c tailwind.config.js -i static/css/tailwind.input.css -o static/css/tailwind.css --minify

# Run locally (expects Postgres up via db-up and a .env file present).
run: templ css
	set -a && . ./.env && set +a && go run ./cmd/server

dev: db-up templ css
	set -a && . ./.env && set +a && go run ./cmd/server

# Live-reload dev server (preferred): rebuilds + restarts on any .go/.templ
# change via air (config in .air.toml). No more manual kill/restart. The server
# inherits the env loaded here. Run `make css-watch` in a second terminal if you
# are editing Tailwind classes; otherwise run `make css` once when you add some.
watch: db-up css
	set -a && . ./.env && set +a && air

# Rebuild static/css/tailwind.css on change (Tailwind --watch). Pair with `watch`.
css-watch:
	npx -y tailwindcss@3 -c tailwind.config.js -i static/css/tailwind.input.css -o static/css/tailwind.css --watch

test:
	go test ./...

# Apply migrations only, then exit.
migrate:
	set -a && . ./.env && set +a && go run ./cmd/server -migrate

# Seed starter data (admin/cashier users + sample products), entities only.
seed:
	set -a && . ./.env && set +a && go run ./cmd/server -seed

# Seed a transaction-rich demo shop (entities + backdated purchases, sales,
# expenses, returns, customer payment, cash register sessions).
demo:
	set -a && . ./.env && set +a && go run ./cmd/server -demo

# Wipe the database (DROP SCHEMA) and re-run migrations. Stop the server first.
# Combine with seed/demo to repopulate in one step. Refuses on APP_ENV=production
# unless you also pass -force.
reset:
	set -a && . ./.env && set +a && go run ./cmd/server -reset

reset-seed:
	set -a && . ./.env && set +a && go run ./cmd/server -reset -seed

reset-demo:
	set -a && . ./.env && set +a && go run ./cmd/server -reset -demo

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
