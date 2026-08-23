-- Webhooks: outbound event notifications for repository and experiment
-- activity, delivered through a PG-queued worker (see webhook_deliveries).

CREATE TABLE IF NOT EXISTS webhooks (
    id           BIGSERIAL PRIMARY KEY,
    namespace_id BIGINT NOT NULL REFERENCES namespaces (id) ON DELETE CASCADE,
    -- NULL means "every repository in the namespace".
    repo_id      BIGINT REFERENCES repositories (id) ON DELETE CASCADE,
    url          TEXT NOT NULL,
    secret       TEXT NOT NULL,
    events       TEXT[] NOT NULL DEFAULT '{}',
    active       BOOLEAN NOT NULL DEFAULT TRUE,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_webhooks_namespace ON webhooks (namespace_id);
CREATE INDEX IF NOT EXISTS idx_webhooks_repo ON webhooks (repo_id) WHERE repo_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS webhook_deliveries (
    id              BIGSERIAL PRIMARY KEY,
    webhook_id      BIGINT NOT NULL REFERENCES webhooks (id) ON DELETE CASCADE,
    event           TEXT NOT NULL,
    payload         JSONB NOT NULL,
    status          TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'success', 'failed')),
    attempts        INT NOT NULL DEFAULT 0,
    last_attempt_at TIMESTAMPTZ,
    response_status INT,
    -- Truncated to a few KB so a chatty endpoint cannot bloat this table.
    response_body   TEXT NOT NULL DEFAULT '',
    -- The queue's lease/backoff clock: a claim pushes this forward so a
    -- crashed worker's in-flight row becomes claimable again on its own
    -- (see internal/webhooks), and a failed attempt pushes it forward by the
    -- exponential backoff interval before the next retry.
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_webhook_deliveries_pending
    ON webhook_deliveries (next_attempt_at, id) WHERE status = 'pending';
CREATE INDEX IF NOT EXISTS idx_webhook_deliveries_webhook
    ON webhook_deliveries (webhook_id, created_at DESC);
