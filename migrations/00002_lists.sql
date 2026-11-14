-- +goose Up
-- +goose StatementBegin
-- newsletter_lists is our own list entity. Provider columns are nullable so a
-- list can exist without Listmonk. visibility is set by admins only; the
-- Listmonk sync creates new lists as 'private' and never copies Listmonk's
-- own public/private type. description is copied from Listmonk by the list
-- sync ('' when Listmonk has none). deleted_at is a soft delete, set only when an
-- admin approves a list_removed pending change.
CREATE TABLE newsletter_lists (
    id                         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name                       TEXT NOT NULL,
    description                TEXT NOT NULL DEFAULT '',
    visibility                 TEXT NOT NULL DEFAULT 'private' CHECK (visibility IN ('private', 'members', 'public')),
    listmonk_id                INTEGER UNIQUE,
    listmonk_subscriber_count  INTEGER,
    synced_at                  TIMESTAMPTZ,
    deleted_at                 TIMESTAMPTZ,
    created_at                 TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at                 TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE newsletter_lists;
-- +goose StatementEnd
