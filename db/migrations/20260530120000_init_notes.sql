-- Initial schema: the example `notes` table.
--
-- This is scaffolding to demonstrate the migration → ent → API → UI path.
-- Replace it with your own schema as the project takes shape.

CREATE TABLE notes (
    id UUID PRIMARY KEY,
    title TEXT NOT NULL,
    body TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);
