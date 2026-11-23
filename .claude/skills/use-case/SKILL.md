---
name: use-case
description: Receita para criar ou alterar um caso de uso neste projeto Go hexagonal (método no user.Service, interface consumida, teste com fakes do usertest). Use ao criar uma regra de negócio, um caso de uso ou uma nova dependência do núcleo.
---

# Criar um caso de uso

Um caso de uso é um método de `*user.Service` (`internal/user/service.go`). Modelos vivos: `Delete` (escrita atômica com `InTx`), `User` (leitura), `Register` (resultado esperado como erro sentinela), `RequestWelcome` (publica evento).

## Passos

1. **Assinatura:** `func (s *Service) <Verbo>(ctx context.Context, ...) (<Resultado>, error)`.
   - Entrada: parâmetros simples; uma struct só quando passar de ~3 campos.
   - Saída: tipos do domínio (`User`, `Page`, `Tokens`). "Não achou" é `ErrNotFound`; resultado de negócio esperado é outro sentinela (`ErrEmailTaken`); entrada inválida é `*ValidationError` (via `invalid(...)`).
2. **Regras:** invariantes ficam no domínio (`user.go`: `New`, `Rename`, `NormalizeEmail`...). Política que precisa da entrada crua fica no caso de uso (ex.: tamanho mínimo da senha antes do hash). Tempo vem de `s.now()`.
3. **Dependência nova do mundo externo:** declare uma interface pequena em `service.go`, falando em tipos do domínio, e um campo exportado no `Service`. Implemente num subpacote (skills `persistence` e `messaging`), acrescente um fake em `usertest/usertest.go` e ligue a implementação em `cmd/api/main.go` (`newApp`).
4. **Atomicidade:** mais de uma escrita no banco → envolva em `s.Store.InTx(ctx, func(tx Store) error { ... })` e use `tx` lá dentro.
5. **Teste** em `internal/user/service_test.go` (pacote `user_test`), só com `usertest` (`setup(t)` devolve service, store e clock): caminho feliz e cada saída alternativa (não encontrado, inativo, sentinela, `*ValidationError`).

## Pronto quando

Cada saída do método tem teste, `go test -short ./internal/user/` passa e `make lint` continua limpo (o `depguard` pega import de infra no núcleo). Para expor por HTTP, siga a skill `endpoint`.
