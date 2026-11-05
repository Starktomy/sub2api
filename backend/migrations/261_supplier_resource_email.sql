-- Full email is owner/admin-visible metadata, not a credential.
-- Legacy masked-only rows remain empty until the supplier updates authorization.
ALTER TABLE supplier_resources ADD COLUMN IF NOT EXISTS email TEXT NOT NULL DEFAULT '';
