-- PostgreSQL is the sole authority for immutable scheduler publication identity.
CREATE TABLE scheduler_publication (
    singleton_id smallint PRIMARY KEY CHECK (singleton_id = 1),
    generation bigint NOT NULL CHECK (generation > 0),
    publication_id text NOT NULL CHECK (publication_id ~ '^[0-9a-f]{64}$'),
    manifest_digest text NOT NULL CHECK (manifest_digest ~ '^[0-9a-f]{64}$')
);

-- State transitions and GC authorization are SQL-only and generation-guarded.
-- Retired identities are tombstones: they can never be activated again.
CREATE TABLE scheduler_publication_lifecycle (
    publication_id text PRIMARY KEY CHECK (publication_id ~ '^[0-9a-f]{64}$'),
    generation bigint NOT NULL CHECK (generation > 0),
    state text NOT NULL CHECK (state IN ('staging','active','obsolete','retired')),
    changed_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX scheduler_publication_lifecycle_cleanup ON scheduler_publication_lifecycle(state,changed_at);
