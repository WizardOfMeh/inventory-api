-- +goose Up
CREATE TABLE nodes (
    id   SERIAL PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    ip   INET NOT NULL
);

CREATE TABLE vms (
    id      SERIAL PRIMARY KEY,
    node_id INTEGER NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    name    TEXT NOT NULL,
    ram     INTEGER NOT NULL CHECK (ram > 0),
    status  TEXT NOT NULL CHECK (status IN ('running', 'stopped', 'paused'))
);

-- Supports the LEFT JOIN in NodeInventory.
CREATE INDEX idx_vms_node_id ON vms (node_id);

-- Composite indexes matching the whitelisted ORDER BY clauses.
-- These become essential once pagination moves from OFFSET to keyset.
CREATE INDEX idx_vms_ram_id_desc ON vms (ram DESC, id DESC);
CREATE INDEX idx_vms_name_id ON vms (name, id);

-- +goose Down
DROP TABLE vms;
DROP TABLE nodes;
