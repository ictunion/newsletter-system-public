-- +goose Up
-- +goose StatementBegin
-- subscription_rules put members on lists by attribute. Every matching
-- enabled rule applies (union), not only the first. 'all' matches every
-- member; 'city' matches when the member's city contains city_contains,
-- case- and accent-insensitively (normalized in Go, not here). Rules apply
-- to members only. Workplace lists are not rules: they come from the
-- member payload's workplace_newsletter_id (source = 'workplace').
CREATE TABLE subscription_rules (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    kind           TEXT NOT NULL CHECK (kind IN ('all', 'city')),
    list_id        UUID NOT NULL REFERENCES newsletter_lists ON DELETE RESTRICT,
    city_contains  TEXT,
    enabled        BOOLEAN NOT NULL DEFAULT true,
    note           TEXT,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT subscription_rules_city_contains_check CHECK (
        (kind = 'all' AND city_contains IS NULL)
        OR (kind = 'city' AND city_contains IS NOT NULL AND city_contains <> '')
    )
);

-- rule_id is set only for source = 'rule'; it goes NULL if the rule is
-- deleted, leaving the row in place.
ALTER TABLE subscriptions
    ADD COLUMN rule_id UUID REFERENCES subscription_rules ON DELETE SET NULL,
    ADD CONSTRAINT subscriptions_rule_id_check CHECK (rule_id IS NULL OR source = 'rule');
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE subscriptions DROP COLUMN rule_id;
DROP TABLE subscription_rules;
-- +goose StatementEnd
