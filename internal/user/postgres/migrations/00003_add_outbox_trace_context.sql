-- +goose Up
-- Contexto W3C (traceparent/tracestate) da requisição que gerou o evento; o relay o reinjeta no Kafka.
ALTER TABLE outbox ADD COLUMN trace_context JSONB;

-- +goose Down
ALTER TABLE outbox DROP COLUMN trace_context;
