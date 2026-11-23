---
name: messaging
description: Receita de mensageria com Kafka neste projeto Go (evento de domínio, outbox na mesma transação, relay, consumer franz-go, teste ponta a ponta). Use ao publicar ou consumir um evento, criar um tópico ou mexer no outbox.
---

# Mensageria (Kafka + Transactional Outbox)

O núcleo nunca vê Kafka. O `Service` registra o evento por um método do `Store`, que grava no **outbox** (por isso fica no `Store`: entra na mesma transação via `InTx`), o `postgres.Relay` (genérico) leva ao Kafka e o `kafka.Consumer` entrega de volta ao `Service`. Entrega **at-least-once**.

Fluxo modelo: `Service.RequestWelcome` → `Store.WelcomeRequested` (outbox) → `Relay.PublishPending` → `kafka.Producer` → `kafka.Consumer.handle` → `Service.SendWelcome` → `Notifier`.

## Publicar um evento

1. **Evento** em `user.go`: struct com tipos do domínio e `OccurredAt`, nomeada como fato (`...Requested`).
2. **Porta:** método novo na interface `Store` (`service.go`), ao lado de `WelcomeRequested`; o fake `usertest.Store` acumula em `Published`.
3. **Caso de uso:** chama `s.Store.<Evento>(ctx, ...)`. Se também escreve no banco, faça tudo dentro de `s.Store.InTx` usando o `tx` recebido: só assim o outbox entra na mesma transação.
4. **Adapter** em `postgres.go`: constante do tópico `<contexto>.<evento-kebab>`, struct de mensagem com tags `json` (o contrato) e `INSERT INTO outbox` com chave = id do agregado (ordem por agregado).
5. **Tópico:** acrescente em `kafka.EnsureTopics`.

## Consumir um evento

6. **Consumer** em `internal/user/kafka`: a **própria** struct de mensagem (contrato duplicado de propósito) e o tópico em `ConsumerOpts`; `handle` faz o parse e chama um método do `Service`. Commit é por `MarkCommitRecords` depois de processar.
7. **Idempotência:** a mesma mensagem pode chegar duas vezes; o método consumidor precisa produzir o mesmo efeito na repetição. Efeito não repetível (cobrança, e-mail real) registra os ids já processados.

## Contrato

O JSON é o mesmo nos templates Java e NestJS. Evolua só **adicionando** campos; para quebrar, crie um tópico novo.

## Testes

- Unitário do caso de uso: `usertest.Store.Published` tem o evento no caminho feliz e fica vazio nos alternativos.
- Relay: `TestOutboxRelay` em `postgres_test.go` (producer fake).
- Ponta a ponta: modelo `TestWelcomeFlow` em `cmd/api/main_test.go` (Postgres + Kafka reais, `OutboxInterval` curto, `usertest.Notifier` com canal e timeout).

## Pronto quando

O teste ponta a ponta prova que o evento sai pelo outbox e chega ao consumidor, `make check` passa e o README lista o tópico.
