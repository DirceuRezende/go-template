# user-template — Hexagonal com Go 1.27

Template de referência com um agregado `User`, autenticação JWT + refresh token e mensageria com Kafka (Transactional Outbox). Mesmo contrato HTTP, mesmo schema e mesmo evento dos templates Java (Spring) e NestJS; a implementação segue o jeito Go.

```bash
make up      # Postgres 17 (5432) + Kafka (9092) + Grafana LGTM (OTLP 4317/4318, UI 3000)
make run     # APP_ENV=dev: segredo JWT de dev + admin admin@example.com / admin12345, porta 8080
make check   # lint (inclui a regra de arquitetura) + todos os testes (precisa do Docker)
```

`api.http` tem o fluxo completo (cadastro → login → refresh → welcome → logout).

## Endpoints

| Método | Rota | Resposta | Quem |
|---|---|---|---|
| POST | `/api/users` | 201 + `Location` / 409 email duplicado | público |
| GET | `/api/users?page=0&size=20` | 200 paginado (size 1..100) | ADMIN |
| GET | `/api/users/{id}` | 200 / 404 | ADMIN ou o próprio |
| PUT | `/api/users/{id}` | 200 / 404 (altera só o nome) | ADMIN ou o próprio |
| DELETE | `/api/users/{id}` | 204 / 404 (soft delete, derruba as sessões) | ADMIN ou o próprio |
| POST | `/api/users/{id}/welcome` | 202 / 404 (via Kafka; inativo = 404) | ADMIN ou o próprio |
| POST | `/api/auth/login` | 200 par de tokens / 401 | público |
| POST | `/api/auth/refresh` | 200 par novo / 401 | público |
| POST | `/api/auth/logout` | 204 | público |

Erros: `application/problem+json` (RFC 9457). 401/403 dos middlewares e 404 vêm sem corpo, como no Spring.

## Estrutura

```
cmd/api/                 main.go: config (env), wiring manual, HTTP + relay + consumer, shutdown gracioso
internal/user/           ← núcleo: só stdlib (depguard)
├── user.go              User, Role, PasswordHash (mascarado), erros, WelcomeRequested
├── service.go           Service (casos de uso) + interfaces que ele consome (Store, Hasher, ...)
├── usertest/            fakes em memória (como net/http/httptest)
├── httpapi/             Gin: rotas, binding, JWT middleware, problem+json
├── postgres/            Store com pgx + SQL à mão, outbox, Relay, migrations (goose, embed)
├── kafka/               franz-go: Producer, Consumer, criação de tópico
├── security/            bcrypt + JWT HS256
└── notify/              Notifier por log (troque por e-mail real)
```

### Decisões

| Decisão | Por quê |
|---|---|
| Pacote por feature, adapters em subpacotes | O compilador impede ciclo: adapter importa `user`, nunca o contrário. Sem `ports/in`/`ports/out`. |
| Interfaces declaradas no consumidor | Idiomático em Go; o `Service` pede só o que usa, a implementação satisfaz implicitamente. |
| Um `Service` em vez de uma struct por caso de uso | Métodos coesos sobre as mesmas dependências; "port in" é a própria API do `Service`. |
| Erros sentinela + `*ValidationError` | Resultado esperado (email duplicado, não encontrado) é erro tipado; a borda traduz em `fail()`. |
| `Store` único com `InTx` | Um banco só. `InTx` dá atomicidade explícita; o outbox é método do `Store` para entrar na mesma transação. |
| `pgx` + SQL à mão | SQL explícito, sem ORM; o schema é o mesmo dos outros templates. |
| Wiring manual em `main.go` | Sem framework de DI: o grafo inteiro cabe numa função legível. |
| `uuid` da stdlib | Go 1.27 trouxe o pacote `uuid`; uma dependência a menos. |

## Configuração

| Variável | Default |
|---|---|
| `APP_ENV` | vazio (`dev` liga segredo e admin de dev) |
| `HTTP_ADDR` | `:8080` |
| `DATABASE_URL` | `postgres://users:users@localhost:5432/users?sslmode=disable` |
| `KAFKA_BOOTSTRAP_SERVERS` | `localhost:9092` |
| `JWT_SECRET` | obrigatório fora de dev, ≥ 32 bytes |
| `ACCESS_TTL` / `REFRESH_TTL` | `15m` / `168h` |
| `OUTBOX_POLL_INTERVAL` | `1s` |
| `OTEL_SERVICE_NAME` | `user-template` |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `http://localhost:4318` (OTLP http) |
| `OTEL_TRACES_EXPORTER` / `OTEL_METRICS_EXPORTER` / `OTEL_LOGS_EXPORTER` | `otlp` (`none` desliga; `console` imprime) |

## Observabilidade (OpenTelemetry)

`cmd/api/otel.go` registra traces, métricas e logs (slog → OTel, além do stderr); os exporters seguem as env `OTEL_*` padrão. Instrumentado: HTTP (`otelgin`), Postgres (`otelpgx`, inclusive stats do pool) e Kafka (`kotel`). Com `make up`, abra o Grafana em http://localhost:3000 → Explore (Tempo, Loki, Prometheus).

O trace atravessa o outbox: `Store.WelcomeRequested` grava o `traceparent` da requisição em `outbox.trace_context`, o relay o devolve ao contexto do produce e o `kotel` o leva no header do record até o consumer. O pedido de welcome e o envio aparecem num trace só. A coluna `trace_context` não existe no schema dos templates Java/NestJS.

**Limites conhecidos:** o polling do relay gera um trace por SELECT do outbox (1/s); o log no stderr não mostra trace_id (o que vai ao OTel tem).

## Segurança

- **Login** devolve `accessToken` (JWT HS256, 15 min, `sub` = id, claim `roles`) e `refreshToken` (32 bytes aleatórios, 7 dias, guardado só como SHA-256).
- **Refresh** rotaciona: cada refresh token vale uma vez (é apagado mesmo se expirou). **Logout** revoga o token enviado. Remover um usuário revoga todos os dele; inativo não loga nem renova.
- Login falha sempre com o mesmo 401 (usuário inexistente, senha errada, email malformado, inativo).

**Limites conhecidos** (marcados com `shortcut:` no código): access token vale até expirar após logout; timing diferente entre usuário inexistente e senha errada; sem detecção de reuso de refresh token; sem rate limit; sem limpeza de tokens expirados.

## Mensageria (Kafka + Transactional Outbox)

```
POST /api/users/{id}/welcome
  └─ Service.RequestWelcome ─► Store.WelcomeRequested ─► tabela outbox (na transação) → 202
postgres.Relay (ticker 1s) ─► outbox pendente (FOR UPDATE SKIP LOCKED) ─► kafka.Producer ─► user.welcome-requested
kafka.Consumer ─► Service.SendWelcome ─► Notifier (log)
```

- Tópico `user.welcome-requested`, 3 partições, chave = userId, JSON `{userId, email, name, occurredAt}`.
- Outbox: gravar no banco e publicar no Kafka não têm transação comum; o evento é gravado junto e o relay só marca `published_at` depois do ack. Entrega **at-least-once**: o consumidor tolera duplicata.
- Consumer commita só o que já processou (`AutoCommitMarks` + `MarkCommitRecords`).

Ver as mensagens:

```bash
docker compose exec kafka /opt/kafka/bin/kafka-console-consumer.sh \
  --bootstrap-server localhost:9092 --topic user.welcome-requested --from-beginning
```

**Limites conhecidos:** sem retry nem DLQ (mensagem com erro é logada e pulada), sem idempotência no consumidor, sem limpeza do outbox.

## Testes

| Nível | Onde | Infra |
|---|---|---|
| Domínio e casos de uso | `internal/user/*_test.go` | fakes (`usertest`) |
| HTTP + segurança (paridade com o `SecurityTest` do Java) | `internal/user/httpapi` | fakes + JWT real |
| SQL, transação, relay | `internal/user/postgres` | Postgres (Testcontainers) |
| Ponta a ponta HTTP → outbox → Kafka → consumer | `cmd/api` | Postgres + Kafka (Testcontainers) |

`make test-short` pula os que usam containers. O e2e usa `confluentinc/confluent-local` porque o módulo Kafka do testcontainers-go só suporta imagens Confluent.

## Pontos em aberto para um projeto real

- Lock otimista (coluna de versão) quando houver updates concorrentes no mesmo usuário.
- Segunda feature: novo pacote em `internal/<feature>` com a mesma forma, e uma regra `depguard` para o núcleo dela.
- Health check não está no template.
