-- +goose Up
-- +goose StatementBegin
-- subscribers is anyone who receives a newsletter. A member is a subscriber
-- with a member_number; non-members (e.g. event invitees added in the
-- Listmonk UI) have none and carry no member-only fields (city, lang). name
-- is what goes to the provider, as orca sends it. The member's workplace is not stored here: it is input only, and
-- the source = 'workplace' subscription row records it.
CREATE TABLE subscribers (
    id                       UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email                    TEXT NOT NULL,
    name                     TEXT NOT NULL,
    member_number            INTEGER UNIQUE,
    city                     TEXT,
    lang                     TEXT,
    listmonk_subscriber_id   INTEGER UNIQUE,
    created_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT subscribers_member_fields_check CHECK (
        (member_number IS NOT NULL AND lang IS NOT NULL)
        OR (member_number IS NULL AND city IS NULL AND lang IS NULL)
    )
);

CREATE UNIQUE INDEX subscribers_lower_email_idx ON subscribers (lower(email));

-- subscriptions has one row per subscriber and list. Rows are never deleted,
-- only re-statused (except by the subscriber's own deletion cascading), so
-- the provider push knows what to remove. unsubscribed is the person's own
-- opt-out; removed is an admin removal or a rule/workplace that no longer
-- applies. source records how the row was first created. listmonk_status is
-- the last seen Listmonk per-list status. rule_id is added in 00004_rules.
CREATE TABLE subscriptions (
    subscriber_id    UUID NOT NULL REFERENCES subscribers ON DELETE CASCADE,
    list_id          UUID NOT NULL REFERENCES newsletter_lists ON DELETE RESTRICT,
    status           TEXT NOT NULL CHECK (status IN ('subscribed', 'unsubscribed', 'removed')),
    source           TEXT NOT NULL CHECK (source IN ('rule', 'workplace', 'manual', 'self')),
    listmonk_status  TEXT CHECK (listmonk_status IN ('unconfirmed', 'confirmed', 'unsubscribed')),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (subscriber_id, list_id)
);

-- Per-list counts and the per-list subscribers view.
CREATE INDEX subscriptions_list_id_status_idx ON subscriptions (list_id, status);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE subscriptions;
DROP TABLE subscribers;
-- +goose StatementEnd
