-- +goose Up
-- +goose StatementBegin
-- provider_jobs is the retry queue for pushing state to the provider. A job
-- stores no PII and no list IDs: the worker reads current state from the DB
-- when it runs, so push_subscriber means "make the provider match our DB for
-- this subscriber". delete_subscriber carries only the provider ID, since
-- the subscriber row is already gone. A row is deleted on success; dead
-- means retries are exhausted and an admin must retry it.
CREATE TABLE provider_jobs (
    id                      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    kind                    TEXT NOT NULL CHECK (kind IN ('push_subscriber', 'delete_subscriber')),
    subscriber_id           UUID REFERENCES subscribers ON DELETE CASCADE,
    listmonk_subscriber_id  INTEGER,
    status                  TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'dead')),
    attempt_count           INTEGER NOT NULL DEFAULT 0,
    next_attempt_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_error              TEXT,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT provider_jobs_target_check CHECK (
        (kind = 'push_subscriber' AND subscriber_id IS NOT NULL)
        OR (kind = 'delete_subscriber' AND listmonk_subscriber_id IS NOT NULL)
    )
);

-- Enqueueing a push twice is a no-op (ON CONFLICT DO NOTHING).
CREATE UNIQUE INDEX provider_jobs_pending_push_idx ON provider_jobs (subscriber_id)
    WHERE kind = 'push_subscriber' AND status = 'pending';
CREATE INDEX provider_jobs_next_attempt_at_idx ON provider_jobs (next_attempt_at)
    WHERE status = 'pending';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE provider_jobs;
-- +goose StatementEnd
