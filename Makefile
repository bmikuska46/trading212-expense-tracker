.PHONY: install generate build web-build go-build test check run dev demo key docker-build docker-up smoke

install:
	cd web && pnpm install --frozen-lockfile
	cd backend && go mod download

generate:
	cd backend && sqlc generate

web-build:
	cd web && pnpm build

go-build: generate
	./scripts/go.sh build -trimpath -ldflags="-s -w" -o ../.build/server ./cmd/server

build: web-build go-build

test: generate
	./scripts/go.sh test -race ./...
	./scripts/go.sh vet ./...

check: test
	cd web && pnpm lint && pnpm typecheck

key:
	./scripts/create-key.sh

run:
	./.build/server

dev:
	./scripts/dev.sh

demo: build
	./scripts/demo.py

docker-build:
	docker compose $(if $(wildcard .env),--env-file .env) -f deploy/compose.yaml build app

docker-up:
	docker compose $(if $(wildcard .env),--env-file .env) -f deploy/compose.yaml up -d --build

smoke:
	./scripts/smoke.py http://127.0.0.1:8080
