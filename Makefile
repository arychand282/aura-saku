.PHONY: run build migrate test docker-up docker-down docker-logs fmt

run:
	go run ./cmd/server

build:
	go build -o bin/aura-saku ./cmd/server

migrate:
	go run ./cmd/migrate

test:
	@# Integration tests call config.Load(), which reads .env from the test
	@# binary's own package directory (not the module root) — without this,
	@# `go test ./...` silently skips every integration test in the repo.
	@if [ -f .env ]; then set -a && . ./.env && set +a && go test ./...; else go test ./...; fi

docker-up:
	docker compose up --build

docker-down:
	docker compose down

docker-logs:
	docker compose logs -f backend

fmt:
	go fmt ./...
