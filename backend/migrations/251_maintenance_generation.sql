-- Independent durable leadership foundation only. This does not enable any
-- worker, fence provider calls, or publish a scheduler namespace.
CREATE TABLE IF NOT EXISTS maintenance_generation (
    singleton_id SMALLINT PRIMARY KEY CHECK (singleton_id = 1),
    generation BIGINT NOT NULL CHECK (generation >= 0),
    owner VARCHAR(64),
    active_scheduler_generation BIGINT,
    CHECK ((generation = 0 AND owner IS NULL) OR
           (generation > 0 AND owner ~ '^[0-9a-f]{64}$' AND owner IS NOT NULL)),
    CHECK (active_scheduler_generation IS NULL OR
           (active_scheduler_generation > 0 AND active_scheduler_generation <= generation))
);

-- Seed only in the forward migration. Runtime must fail closed if this row or
-- schema is missing; deleting/resetting it is not a recovery mechanism.
INSERT INTO maintenance_generation (singleton_id, generation, owner, active_scheduler_generation)
VALUES (1, 0, NULL, NULL)
ON CONFLICT (singleton_id) DO NOTHING;
