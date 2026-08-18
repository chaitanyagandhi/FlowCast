-- Controlled incident experiments and their scores.
--
-- GROUND TRUTH LIVES HERE AND MUST NOT LEAK INTO ANALYSIS.
--
-- simulation_scenarios.root_cause and simulation_runs.ground_truth describe the fault that
-- was deliberately injected. The analysis pipeline reads events and incidents only; it has
-- no reason to read either of these tables, and the evaluation package is the only thing
-- that should. Keeping the two apart is what makes a passing score mean anything.

CREATE TABLE simulation_scenarios (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),

    -- Addressable by name, e.g. "database-pool-exhaustion".
    name        text        NOT NULL UNIQUE CHECK (length(btrim(name)) BETWEEN 1 AND 200),
    -- Grouping for reporting: dependency, database, deployment, queue, and whatever an
    -- agent proposes later. Deliberately not constrained to a fixed list -- the safety
    -- allowlist that matters is over fault actions in scenario_config, enforced in code
    -- before anything is injected, not over this label.
    category    text        NOT NULL CHECK (length(btrim(category)) BETWEEN 1 AND 100),
    description text        NOT NULL DEFAULT '',

    -- GROUND TRUTH: the cause an ideal diagnosis would name.
    root_cause  text        NOT NULL CHECK (length(btrim(root_cause)) > 0),

    -- The fault to inject: target service, action, and parameters. Validated against the
    -- allowlist of typed fault actions before execution.
    scenario_config jsonb   NOT NULL DEFAULT '{}'::jsonb
                            CHECK (jsonb_typeof(scenario_config) = 'object'),

    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX simulation_scenarios_category_idx ON simulation_scenarios (category);


-- One execution of a scenario.
CREATE TABLE simulation_runs (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),

    -- Runs are the experimental record; a scenario with history cannot be deleted out
    -- from under them.
    scenario_id uuid        NOT NULL REFERENCES simulation_scenarios (id) ON DELETE RESTRICT,

    -- The run belongs to the team that started it. Present in its own right rather than
    -- read through incident_id, because a queued run has no incident yet and the
    -- simulations list still has to be team-scoped.
    team_id     uuid        NOT NULL REFERENCES teams (id) ON DELETE CASCADE,

    -- Populated once the generated telemetry has produced an incident. Deleting the
    -- incident keeps the run and its score, minus the link.
    incident_id uuid        REFERENCES incidents (id) ON DELETE SET NULL,

    status      text        NOT NULL DEFAULT 'pending'
                            CHECK (status IN ('pending', 'running', 'analyzing', 'completed', 'failed')),

    started_at   timestamptz,
    completed_at timestamptz,

    -- GROUND TRUTH, snapshotted at execution time: the expected root cause, which events
    -- are genuinely causal, and which were injected as noise. Copied rather than read back
    -- through the scenario so that editing a scenario later cannot rewrite the history of
    -- runs already scored against it.
    ground_truth jsonb      NOT NULL DEFAULT '{}'::jsonb
                            CHECK (jsonb_typeof(ground_truth) = 'object'),

    created_at   timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT simulation_runs_completed_after_start
        CHECK (completed_at IS NULL
               OR (started_at IS NOT NULL AND completed_at >= started_at))
);

CREATE INDEX simulation_runs_scenario_idx     ON simulation_runs (scenario_id);
CREATE INDEX simulation_runs_team_created_idx ON simulation_runs (team_id, created_at DESC);
CREATE INDEX simulation_runs_status_idx       ON simulation_runs (status);


-- How the prediction scored against the ground truth. One row per run: re-scoring a run
-- replaces its result rather than accumulating duplicates.
CREATE TABLE evaluation_results (
    id                uuid    PRIMARY KEY DEFAULT gen_random_uuid(),
    simulation_run_id uuid    NOT NULL UNIQUE REFERENCES simulation_runs (id) ON DELETE CASCADE,

    root_cause_correct boolean NOT NULL,
    -- Where the true cause appeared among the ranked hypotheses; NULL when it did not
    -- appear at all, which is what Top-3 accuracy counts.
    root_cause_rank    integer CHECK (root_cause_rank IS NULL OR root_cause_rank > 0),

    -- Nullable on purpose. Precision is undefined when nothing was predicted, and recall
    -- when there is nothing to find; storing NULL says "not measurable for this run"
    -- rather than quietly reporting a zero that would drag an average down.
    causal_precision real CHECK (causal_precision IS NULL OR (causal_precision BETWEEN 0 AND 1)),
    causal_recall    real CHECK (causal_recall    IS NULL OR (causal_recall    BETWEEN 0 AND 1)),
    noise_accuracy   real CHECK (noise_accuracy   IS NULL OR (noise_accuracy   BETWEEN 0 AND 1)),

    -- Time from analysis start to completed diagnosis.
    diagnosis_latency_ms integer NOT NULL CHECK (diagnosis_latency_ms >= 0),

    created_at timestamptz NOT NULL DEFAULT now()
);
