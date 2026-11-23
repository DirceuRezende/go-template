# Postgres + Kafka locais (docker-compose.yml)
up:
	docker compose up -d --wait

down:
	docker compose down

# APP_ENV=dev: segredo JWT de dev + admin@example.com / admin12345
run:
	APP_ENV=dev go run ./cmd/api

# Testes de integração e e2e sobem containers (Testcontainers); precisa de Docker.
test:
	go test ./...

# Só os testes sem container (domínio, casos de uso, HTTP com fakes).
test-short:
	go test -short ./...

lint:
	go tool golangci-lint run ./...

# "Pronto" = isto passa.
check: lint test

.PHONY: up down run test test-short lint check
