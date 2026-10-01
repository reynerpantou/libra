.PHONY: setup web build run dev-api dev-web docker db demo demo-fresh reset link claim users test tidy

# Load .env (if present) so the binary uses the same settings as docker compose.
-include .env
export

setup:        ## install frontend deps
	cd web && npm install

web/node_modules: web/package.json web/package-lock.json
	cd web && npm install
	@touch web/node_modules

web: web/node_modules ## build the React app into the embed dir
	cd web && npm run build

tidy:         ## resolve Go deps + write go.sum
	go mod tidy

build: tidy web ## full production build -> ./libra
	CGO_ENABLED=0 go build -ldflags="-s -w" -o libra ./cmd/libra

run:          ## run the built binary
	./libra

db:           ## start just Postgres (for running the binary outside Docker)
	docker compose up -d postgres

dev-api: db   ## run the API against local Postgres
	go run ./cmd/libra

dev-web: web/node_modules ## Vite dev server on :5173 (proxies /api to :8080)
	cd web && npm run dev

demo:         ## build the full demo (every feature) on an empty database
	go run ./cmd/libra demo

reset:        ## DROP EVERYTHING in the database and recreate it empty (asks first; force=1 skips)
	@if [ "$(force)" != "1" ]; then \
		printf "This drops every table and all data in $${LIBRA_DATABASE_URL:-the configured database}. Type 'yes' to continue: "; \
		read ans; [ "$$ans" = "yes" ] || { echo "aborted"; exit 1; }; \
	fi
	go run ./cmd/libra reset -yes

demo-fresh:   ## drop everything, then build the full demo
	@$(MAKE) --no-print-directory reset force=$(force)
	@$(MAKE) --no-print-directory demo

link:         ## one-time sign-in link: make link user=<username>
	@test -n "$(user)" || (echo "usage: make link user=<username>   (see: make users)"; exit 1)
	@go run ./cmd/libra sign-in-link $(user)

claim:        ## print a new owner setup link (new install only)
	@go run ./cmd/libra setup-link

users:        ## list accounts (username, email, role)
	@go run ./cmd/libra list-users

test:         ## unit tests (+ integration tests when LIBRA_TEST_DATABASE_URL is set)
	go test ./...

docker:       ## build + run everything via docker compose
	docker compose up --build
