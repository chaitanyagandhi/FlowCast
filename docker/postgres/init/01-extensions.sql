-- Enable the extensions FlowCast depends on.
--
-- This runs once, when the postgres data volume is first created. The migration runner
-- also creates these with IF NOT EXISTS, so a database provisioned some other way still
-- ends up correct; this script just means `docker compose up` yields a ready database.

-- pgvector: embedding storage and similarity search for related-incident retrieval.
CREATE EXTENSION IF NOT EXISTS vector;

-- gen_random_uuid() for UUID primary keys.
CREATE EXTENSION IF NOT EXISTS pgcrypto;
