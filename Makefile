.PHONY: run build migrate test docker-up docker-down docker-logs fmt

run:
	go run ./cmd/server

build:
	go build -o bin/aura-saku ./cmd/server

migrate:
	go run ./cmd/migrate

test:
	go test ./...

docker-up:
	docker compose up --build

docker-down:
	docker compose down

docker-logs:
	docker compose logs -f backend

fmt:
	go fmt ./...
