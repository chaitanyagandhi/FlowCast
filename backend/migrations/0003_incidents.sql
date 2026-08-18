-- Incidents, their normalized events, and everything the AI pipeline produces about them.
--
-- Tenancy: only incidents carry team_id. Events, analyses, hypotheses, and postmortems are
-- reached through their incident, so a team-scoped query joins back to incidents rather
-- than trusting a denormalized copy that could drift. Every child cascades on delete.

CREATE TABLE incidents (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    team_id     uuid        NOT NULL REFERENCES teams (id) ON DELETE CASCADE,

    -- Identifier from the originating system, e.g. a PagerDuty incident id. Absent for
    -- incidents created by hand.
    external_id text        CHECK (external_id IS NULL OR length(btrim(external_id)) > 0),

    title       text        NOT NULL CHECK (length(btrim(title)) BETWEEN 1 AND 500),
    description text        NOT NULL DEFAULT '',
    severity    text        NOT NULL CHECK (severity IN ('P1', 'P2', 'P3', 'P4')),
    status      text        NOT NULL DEFAULT 'open'
                            CHECK (status IN ('open', 'processing', 'analysis_ready', 'resolved')),
    source      text        NOT NULL
                            CHECK (source IN ('manual', 'pagerduty', 'datadog', 'slack', 'simulation')),

    started_at  timestamptz NOT NULL,
    resolved_at timestamptz,
    -- Named explicitly: a check spanning two columns becomes a table-level constraint,
    -- and the auto-generated name for one of those is just "incidents_check".
    CONSTRAINT incidents_resolved_after_start
        CHECK (resolved_at IS NULL OR resolved_at >= started_at),

    -- Embedding of title + summary + predicted root cause, used for similar-incident
    -- retrieval. The width is fixed here because a pgvector index requires it, and must
    -- match FLOWCAST_EMBEDDING_DIMENSIONS; the backend verifies this at startup.
    embedding   vector(1536),

    metadata    jsonb       NOT NULL DEFAULT '{}'::jsonb
                            CHECK (jsonb_typeof(metadata) = 'object'),

    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

-- One incident per external id per team. Partial, so the many manually created incidents
-- with no external id do not collide.
CREATE UNIQUE INDEX incidents_team_external_id_key
    ON incidents (team_id, external_id)
    WHERE external_id IS NOT NULL;

-- The dashboard lists a team's incidents newest first, usually filtered by status.
CREATE INDEX incidents_team_started_at_idx ON incidents (team_id, started_at DESC);
CREATE INDEX incidents_team_status_idx     ON incidents (team_id, status);

-- Similarity search. HNSW rather than IVFFlat because it needs no training data, which
-- matters when the table starts empty. Cosine distance suits normalized embeddings.
CREATE INDEX incidents_embedding_idx
    ON incidents USING hnsw (embedding vector_cosine_ops);

CREATE TRIGGER incidents_set_updated_at
    BEFORE UPDATE ON incidents
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();


-- Normalized telemetry. Every provider adapter writes this shape; the untouched provider
-- payload is kept alongside it in raw_payload so nothing is lost in normalization.
CREATE TABLE events (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    incident_id uuid        NOT NULL REFERENCES incidents (id) ON DELETE CASCADE,

    source      text        NOT NULL
                            CHECK (source IN ('manual', 'pagerduty', 'datadog', 'slack', 'simulation')),
    event_type  text        NOT NULL CHECK (length(btrim(event_type)) BETWEEN 1 AND 100),
    title       text        NOT NULL CHECK (length(btrim(title)) BETWEEN 1 AND 500),
    description text        NOT NULL DEFAULT '',
    service     text        NOT NULL DEFAULT '',

    occurred_at timestamptz NOT NULL,
    raw_payload jsonb       NOT NULL DEFAULT '{}'::jsonb,

    -- Written by the AI classification stage, not by ingestion. UNKNOWN until classified.
    classification text     NOT NULL DEFAULT 'UNKNOWN'
                            CHECK (classification IN (
                                'ROOT_CAUSE', 'CONTRIBUTING_FACTOR', 'SYMPTOM',
                                'MITIGATION', 'RECOVERY', 'NOISE', 'UNKNOWN')),
    -- Position in the reconstructed causal chain; NULL when the event is not on it.
    causal_rank integer     CHECK (causal_rank IS NULL OR causal_rank > 0),

    created_at  timestamptz NOT NULL DEFAULT now()
);

-- The incident timeline: every event for one incident, in the order it happened.
CREATE INDEX events_incident_occurred_at_idx ON events (incident_id, occurred_at);
-- Pulling out just the causal chain, or just the noise, for the timeline view.
CREATE INDEX events_incident_classification_idx ON events (incident_id, classification);


-- One row per analysis run. Rows are immutable: re-analysing an incident inserts a new
-- row, which is what makes prompt and model comparisons possible later.
CREATE TABLE incident_analyses (
    id                   uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    incident_id          uuid        NOT NULL REFERENCES incidents (id) ON DELETE CASCADE,

    predicted_root_cause text        NOT NULL,
    confidence           real        NOT NULL CHECK (confidence >= 0 AND confidence <= 1),
    -- A short summary only. Hidden chain-of-thought is never stored.
    reasoning_summary    text        NOT NULL DEFAULT '',

    model                text        NOT NULL CHECK (length(btrim(model)) > 0),
    prompt_version       text        NOT NULL CHECK (length(btrim(prompt_version)) > 0),

    created_at           timestamptz NOT NULL DEFAULT now()
);

-- Fetching the most recent analysis for an incident.
CREATE INDEX incident_analyses_incident_created_at_idx
    ON incident_analyses (incident_id, created_at DESC);


-- Ranked alternatives from one analysis run, at most three.
CREATE TABLE root_cause_hypotheses (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    analysis_id uuid        NOT NULL REFERENCES incident_analyses (id) ON DELETE CASCADE,

    rank        integer     NOT NULL CHECK (rank BETWEEN 1 AND 3),
    cause       text        NOT NULL CHECK (length(btrim(cause)) > 0),
    confidence  real        NOT NULL CHECK (confidence >= 0 AND confidence <= 1),

    -- Events supporting this hypothesis. Verified against the events table before the
    -- analysis is accepted, so the model cannot cite evidence that does not exist. Not a
    -- foreign key: an array keeps the ordered citation list the model produced intact.
    evidence_event_ids uuid[] NOT NULL DEFAULT '{}',

    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX root_cause_hypotheses_analysis_rank_key
    ON root_cause_hypotheses (analysis_id, rank);


-- The generated postmortem, one per incident, editable afterwards by a human.
CREATE TABLE postmortems (
    id                   uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    incident_id          uuid        NOT NULL UNIQUE REFERENCES incidents (id) ON DELETE CASCADE,

    executive_summary    text        NOT NULL DEFAULT '',
    impact               text        NOT NULL DEFAULT '',
    root_cause           text        NOT NULL DEFAULT '',
    timeline_md          text        NOT NULL DEFAULT '',

    -- JSON arrays rather than text[]: action items carry structure (owner, description),
    -- and keeping all three the same shape avoids a second serialization path.
    contributing_factors jsonb       NOT NULL DEFAULT '[]'::jsonb
                                     CHECK (jsonb_typeof(contributing_factors) = 'array'),
    action_items         jsonb       NOT NULL DEFAULT '[]'::jsonb
                                     CHECK (jsonb_typeof(action_items) = 'array'),
    uncertainties        jsonb       NOT NULL DEFAULT '[]'::jsonb
                                     CHECK (jsonb_typeof(uncertainties) = 'array'),

    generated_at         timestamptz NOT NULL DEFAULT now(),
    updated_at           timestamptz NOT NULL DEFAULT now()
);

CREATE TRIGGER postmortems_set_updated_at
    BEFORE UPDATE ON postmortems
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
