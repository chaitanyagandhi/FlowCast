-- Extensions and shared helpers every later migration builds on.
--
-- The docker compose init script creates these extensions too, so a local stack is ready
-- before the backend starts. Repeating them here means a database provisioned some other
-- way -- CI, a managed instance -- ends up equally correct.

-- Embedding storage and similarity search for related-incident retrieval.
CREATE EXTENSION IF NOT EXISTS vector;

-- gen_random_uuid() for UUID primary keys.
CREATE EXTENSION IF NOT EXISTS pgcrypto;

-- Keeps updated_at honest: application code cannot forget to set it, and a manual UPDATE
-- run against the database by hand still records the change.
CREATE OR REPLACE FUNCTION set_updated_at() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$;
