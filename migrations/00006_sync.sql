-- +goose Up
-- +goose StatementBegin
-- sync_runs has one row per scheduled or triggered sync run; the dashboard
-- reads the last one per kind. Rows left 'running' by a crash are marked
-- 'failed' at startup (FailStaleRuns).
CREATE TABLE sync_runs (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    kind              TEXT NOT NULL CHECK (kind IN ('listmonk_lists', 'listmonk_subscriptions', 'orca_members')),
    status            TEXT NOT NULL DEFAULT 'running' CHECK (status IN ('running', 'succeeded', 'failed')),
    started_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at       TIMESTAMPTZ,
    items_fetched     INTEGER NOT NULL DEFAULT 0,
    changes_detected  INTEGER NOT NULL DEFAULT 0,
    error             TEXT
);

CREATE INDEX sync_runs_kind_started_at_idx ON sync_runs (kind, started_at DESC);
-- At most one running run per kind: starting a second one fails, so the
-- scheduler and a manual trigger never overlap.
CREATE UNIQUE INDEX sync_runs_running_idx ON sync_runs (kind) WHERE status = 'running';

-- pending_changes is drift found by the orca reconciliation or the list
-- sync, waiting for an admin decision. payload is the orca snapshot or field
-- diff applied on approval. member_number is NULL for list_removed and
-- list_id is NULL for member_* kinds, so the pending unique index treats
-- NULLs as equal: re-detecting the same change updates payload instead of
-- adding a row. decided_by is the deciding admin's Keycloak sub.
CREATE TABLE pending_changes (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    kind           TEXT NOT NULL CHECK (kind IN ('member_new', 'member_updated', 'member_removed',
                                                 'subscription_add', 'subscription_remove', 'list_removed')),
    member_number  INTEGER,
    list_id        UUID REFERENCES newsletter_lists ON DELETE RESTRICT,
    payload        JSONB NOT NULL,
    status         TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'rejected')),
    detected_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    decided_at     TIMESTAMPTZ,
    decided_by     TEXT,
    CONSTRAINT pending_changes_decided_check CHECK (
        (status = 'pending') = (decided_at IS NULL AND decided_by IS NULL)
    )
);

CREATE UNIQUE INDEX pending_changes_pending_idx ON pending_changes (kind, member_number, list_id)
    NULLS NOT DISTINCT WHERE status = 'pending';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE pending_changes;
DROP TABLE sync_runs;
-- +goose StatementEnd
