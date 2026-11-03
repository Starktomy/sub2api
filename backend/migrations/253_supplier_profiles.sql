-- Portal membership only. No supply eligibility, credentials or payment authority.
CREATE TABLE IF NOT EXISTS supplier_profiles (
 id BIGSERIAL PRIMARY KEY,
 user_id BIGINT NOT NULL UNIQUE REFERENCES users(id) ON DELETE CASCADE,
 display_name VARCHAR(100) NOT NULL CHECK (char_length(btrim(display_name)) BETWEEN 1 AND 100),
 status VARCHAR(16) NOT NULL DEFAULT 'active' CHECK (status = 'active'),
 verified_email VARCHAR(320) NOT NULL,
 email_verified_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
 version BIGINT NOT NULL DEFAULT 1 CHECK (version > 0),
 created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
 updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
