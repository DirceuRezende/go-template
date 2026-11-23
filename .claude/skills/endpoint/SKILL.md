---
name: endpoint
description: Receita para criar ou alterar um endpoint HTTP neste projeto Go (rota Gin, request/response, status, autorização, testes de acesso). Use ao criar uma rota, um handler ou uma regra de acesso HTTP.
---

# Criar um endpoint

O handler só traduz HTTP para um método do `user.Service`; o caso de uso vem antes (skill `use-case`). Tudo fica em `internal/user/httpapi/httpapi.go`.

## Passos

1. **Rota** em `New`: pública em `api`, protegida em `private` com o middleware de acesso:
   - dono ou admin: `selfOrAdmin` (compara o `sub` do JWT com `:id`);
   - só admin: `requireRole(user.RoleAdmin)`.
2. **Request:** struct com tags `json` e `binding` (go-playground/validator), lida com `bind(c, c.ShouldBindJSON(&req))` ou `ShouldBindQuery`. Id de rota: `pathID(c)`.
3. **Response:** struct própria com tags `json` (como `userResponse`), montada de tipos do domínio. Nunca serialize `user.User` direto (tem o hash).
4. **Status:**
   | Caso | Resposta |
   |---|---|
   | criar | `201` + `Location` |
   | ler/atualizar | `200`; `ErrNotFound` → `404` |
   | remover | `204` |
   | assíncrono (outbox/Kafka) | `202` |
   | resultado esperado (`ErrEmailTaken`...) | `problem(c, 409, ...)` no handler |
   | entrada inválida | `400` problem+json (binding, `pathID`, `*ValidationError`) |
   Erros do `Service` vão para `fail(c, err)`; sentinela nova ganha um `case` lá.
5. **Testes** em `httpapi_test.go` (Store em memória + JWT real, helpers `signup`, `admin`, `login`, `bearer()`):
   - acesso: anônimo `401`, outro usuário `403`, o próprio e o admin o sucesso, id inexistente `404`;
   - comportamento: payload, cada status da tabela que a rota devolve e o que o JSON **não** contém.
6. **Contrato:** a rota existe igual nos templates Java e NestJS; atualize `api.http` e a tabela de endpoints do README.

## Pronto quando

Cada status possível tem teste, as quatro situações de acesso estão cobertas e `make check` passa.
