---
name: persistence
description: Receita de persistência neste projeto Go (migration goose, SQL à mão com pgx, método no Store, teste com Testcontainers). Use ao criar ou alterar tabela, coluna, migration, query ou método do Store.
---

# Persistência (goose + pgx atrás do Store)

O núcleo vê só a interface `user.Store`; `internal/user/postgres` a implementa com SQL escrito à mão. Modelos: `CreateUser`/`SaveUser` (escrita em várias tabelas via `atomic`), `UserByID` (`scanUser`), `TakeRefreshToken` (`DELETE ... RETURNING`).

## Passos

1. **Migration:** `internal/user/postgres/migrations/<NNNNN>_<descricao_snake>.sql`, começando com `-- +goose Up`. Aplicada sozinha no startup e nos testes.
   - Cada mudança é uma migration **nova**; uma aplicada é imutável.
   - Tipos: `uuid.UUID` ↔ `UUID`; `time.Time` ↔ `TIMESTAMP(6) WITH TIME ZONE`; `string` ↔ `VARCHAR(n)`/`TEXT`; `bool` ↔ `BOOLEAN`.
   - Dê nome a constraints e índices (`uk_`, `fk_`, `idx_`); o adapter reconhece violação pelo nome (`pgErr.ConstraintName`).
   - O schema é o mesmo nos templates Java e NestJS: replique a mudança lá se o contrato mudar.
2. **Porta:** acrescente o método na interface `Store` (`internal/user/service.go`), em tipos do domínio; "não achou" devolve `user.ErrNotFound`.
3. **Adapter** em `postgres.go`: sempre `s.q` (vale para pool e transação). Escrita em mais de uma tabela vai dentro de `s.atomic(ctx, func(tx *Store) error {...})`, usando `tx.q`. `pgx.ErrNoRows` → `user.ErrNotFound`.
4. **Fake:** implemente o mesmo método em `usertest.Store`.
5. **Teste** em `postgres_test.go` (Postgres real via Testcontainers; `reset(t)` trunca as tabelas): ida e volta, não encontrado e violação de constraint. Tabela nova entra no `TRUNCATE` de `reset`.

## Banco de dev

Banco inconsistente com as migrations: `make down && docker volume rm user-template-go_pgdata && make up` recria vazio (apaga os dados de dev).

## Pronto quando

A migration nova existe, `go test ./internal/user/postgres/` passa e `make check` passa.
