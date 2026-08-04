-- Synthetic dataset for pagination and sorting benchmarks.
-- Not a goose migration: this is deliberately kept out of the versioned
-- schema so production never runs it. Apply with `make seed`.

BEGIN;

TRUNCATE vms, nodes RESTART IDENTITY CASCADE;

-- 20 nodes on a /24, named pve-01 .. pve-20
INSERT INTO nodes (name, ip)
SELECT
    'pve-' || lpad(g::text, 2, '0'),
    ('10.42.0.' || (10 + g))::inet
FROM generate_series(1, 20) AS g;

-- 500k VMs spread across those nodes.
--   ram    : 512 MB .. 64 GB, skewed towards smaller guests so the
--            distribution is not uniform (uniform data hides bad plans)
--   status : ~70% running, ~25% stopped, ~5% paused
INSERT INTO vms (node_id, name, ram, status)
SELECT
    1 + (g % 20),
    'vm-' || lpad(g::text, 7, '0'),
    512 * (1 + floor(power(random(), 2) * 127))::int,
    CASE
        WHEN random() < 0.70 THEN 'running'
        WHEN random() < 0.95 THEN 'stopped'
        ELSE 'paused'
    END
FROM generate_series(1, 500000) AS g;

COMMIT;

-- Statistics must be fresh, otherwise the planner works from defaults and
-- the EXPLAIN numbers in the README are meaningless.
ANALYZE nodes;
ANALYZE vms;

\echo ''
\echo 'Seed complete:'
SELECT
    (SELECT count(*) FROM nodes) AS nodes,
    (SELECT count(*) FROM vms)   AS vms,
    pg_size_pretty(pg_total_relation_size('vms')) AS vms_size;
