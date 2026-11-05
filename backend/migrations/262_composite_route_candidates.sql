-- Ordered, explicitly equivalent sources; legacy rows remain single-target.
ALTER TABLE composite_model_routes ADD COLUMN IF NOT EXISTS candidates JSONB;
