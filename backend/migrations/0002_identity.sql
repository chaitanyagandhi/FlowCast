-- Teams, users, and provider integrations.
--
-- The team is FlowCast's tenant boundary. Every user, integration, incident, and analysis
-- hangs off a team, and every protected query filters on team_id.

CREATE TABLE teams (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    name       text        NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 200),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TRIGGER teams_set_updated_at
    BEFORE UPDATE ON teams
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();


CREATE TABLE users (
    id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    team_id       uuid        NOT NULL REFERENCES teams (id) ON DELETE CASCADE,
    email         text        NOT NULL CHECK (length(email) BETWEEN 3 AND 320),
    password_hash text        NOT NULL,
    name          text        NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 200),
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

-- Email identifies a login globally, not per team, so a person cannot register the same
-- address twice by picking a different team. Case-insensitive: nobody expects
-- Ada@example.com and ada@example.com to be separate accounts.
CREATE UNIQUE INDEX users_email_lower_key ON users (lower(email));

-- Listing a team's members, and the FK's own delete-cascade check.
CREATE INDEX users_team_id_idx ON users (team_id);

CREATE TRIGGER users_set_updated_at
    BEFORE UPDATE ON users
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();


CREATE TABLE integrations (
    id             uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    team_id        uuid        NOT NULL REFERENCES teams (id) ON DELETE CASCADE,
    provider       text        NOT NULL CHECK (provider IN ('pagerduty', 'datadog', 'slack')),
    webhook_secret text        NOT NULL CHECK (length(webhook_secret) >= 16),
    config         jsonb       NOT NULL DEFAULT '{}'::jsonb,
    enabled        boolean     NOT NULL DEFAULT true,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);

-- One integration per provider per team. This is what lets the API address an integration
-- as /api/v1/integrations/{provider} rather than by opaque id.
CREATE UNIQUE INDEX integrations_team_provider_key ON integrations (team_id, provider);

CREATE TRIGGER integrations_set_updated_at
    BEFORE UPDATE ON integrations
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
