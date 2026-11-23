# user-template (Go)

Template **hexagonal** em Go 1.27 com Gin, pgx, franz-go. Feature única: `internal/user`. Mesmo contrato HTTP e de evento dos templates Java e NestJS (ver `api.http`).

## Comandos

Veja o `Makefile`. Uma tarefa está pronta quando `make check` (lint + todos os testes) passa. Os testes de `postgres` e `cmd/api` sobem containers (Docker); `make test-short` roda só os rápidos para iterar.

## Regra de dependência

- `internal/user` (só o pacote raiz) é o núcleo: domínio (`user.go`), casos de uso (`Service` em `service.go`) e as interfaces que ele consome. Só stdlib, garantido pelo `depguard` no `make lint`.
- Adapters ficam em subpacotes (`postgres`, `httpapi`, `kafka`, `security`, `notify`) e importam `user`; o núcleo nunca importa um subpacote.
- Interface é declarada no consumidor: o `Service` declara `Store`, `Hasher`...; o relay declara `Producer`. Implementação satisfaz implicitamente.
- Todo o wiring é manual em `cmd/api/main.go` (`newApp`).

## Convenções

- Identificadores em inglês; comentários, mensagens de erro e docs em português.
- Erros esperados são sentinelas (`user.ErrNotFound`, `ErrEmailTaken`...) ou `*user.ValidationError`; a borda HTTP os traduz em `fail()` (`httpapi`). Compare com `errors.Is` / `errors.AsType`.
- Segredo no domínio tem tipo próprio que mascara `String()` e `LogValue()` (ex.: `PasswordHash`).
- Tempo vem de `Service.Now` (nil = `time.Now`); testes fixam com `usertest.Clock`.
- `context.Context` é o primeiro parâmetro de tudo que faz I/O.
- Atalho consciente ganha `// shortcut: <limite>, upgrade: <o que fazer>`.

## Pegadinhas

- `uuid` é da stdlib (Go 1.27), não `github.com/google/uuid`; o pgx lê e grava direto.
- Gin: `r.ContextWithFallback = true` faz o `*gin.Context` valer como ctx da requisição; passe `c` para o `Service`.
- Migrations em `internal/user/postgres/migrations` (goose, embutidas, aplicadas no startup). Migration aplicada nunca é editada: crie a próxima.
- O módulo Kafka do testcontainers-go só aceita imagens Confluent; o e2e usa `confluentinc/confluent-local`, o compose usa `apache/kafka`.
- OpenTelemetry é configurado em `cmd/api/otel.go`, chamado só no `run`: testes e `newApp` ficam com providers noop. O contexto do trace passa pelo outbox na coluna `trace_context` (postgres) e não pelo núcleo.
- O compose tem `name: user-template-go` e usa as portas 5432/9092, as mesmas do template Java: pare um antes de subir o outro.
