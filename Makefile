.PHONY: setup web build run dev-api dev-web docker db demo link test tidy

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

demo:         ## seed the demo "search" business and simulate 14 days of traffic
	go run ./cmd/libra demo

link:         ## print a one-time sign-in link for the admin
	go run ./cmd/libra sign-in-link $${LIBRA_ADMIN_USER:-admin}

test:         ## unit tests (+ integration tests when LIBRA_TEST_DATABASE_URL is set)
	go test ./...

docker:       ## build + run everything via docker compose
	docker compose up --build
