.PHONY: setup web build run dev-api dev-web docker db demo test tidy

setup:        ## install frontend deps
	cd web && npm install

web:          ## build the React app into the embed dir
	cd web && npm run build

tidy:         ## resolve Go deps + write go.sum
	go mod tidy

build: tidy web ## full production build -> ./libra
	CGO_ENABLED=0 go build -ldflags="-s -w" -o libra ./cmd/libra

run:          ## run the built binary
	./libra

db:           ## start just Postgres (for dev-api/dev-web outside Docker)
	docker compose up -d postgres

dev-api: db   ## run the API against local Postgres
	go run ./cmd/libra

dev-web:      ## Vite dev server on :5173 (proxies /api to :8080)
	cd web && npm run dev

demo:         ## seed the demo "search" business and simulate 14 days of traffic
	go run ./cmd/libra demo

test:         ## unit tests (+ integration tests when LIBRA_TEST_DATABASE_URL is set)
	go test ./...

docker:       ## build + run everything via docker compose
	docker compose up --build
