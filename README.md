# inventory-api

A small Go service that exposes a homelab inventory — physical nodes and the
VMs running on them — over a JSON HTTP API.

It exists because I wanted a backend I could actually operate rather than
demo: it runs against a real PostgreSQL instance, ships as a 20 MB distroless
image, and is deployed to a k3s cluster where the database lives on a
different host. Most of the decisions below came from watching it fail and
fixing it, not from a tutorial.

## Stack

Go 1.26 · PostgreSQL 18 · Docker · Kubernetes (k3s) · goose · pgx

No web framework. Go 1.22 added method-and-pattern routing to `net/http`
(`GET /api/v1/nodes/{id}/inventory`), which covers everything this service
needs — a router dependency would have been weight without benefit.

## Architecture

```
                    ┌──────────────────────────────┐
   client ────────► │  inventory-api (2 replicas)  │
                    │  k3s · distroless · non-root │
                    └──────────────┬───────────────┘
                                   │ 10.42.0.19:5432
                    ┌──────────────▼───────────────┐
                    │   PostgreSQL 18 (LXC host)   │
                    │   500k rows, composite idx   │
                    └──────────────────────────────┘
```

The database deliberately sits **outside** the cluster. Running stateful
Postgres on a single-node k3s with `local-path` storage would add StatefulSet,
PVC and storage-class complexity while giving up nothing in return — the data
would still be pinned to one node with no replication. An external database is
also what the production shape looks like, where it would be RDS or Cloud SQL.

## Quick start

```bash
cp .env.example .env      # fill in DATABASE_URL and API_TOKEN
docker compose up --build
```

That brings up Postgres, Redis and the API. To load the schema and a
500k-row dataset against your own database:

```bash
export DATABASE_URL="postgres://user:pass@host:5432/homelab?sslmode=disable"
make migrate-up
make seed
```

`make help` lists the rest.

## API

All `/api/v1/*` routes require `Authorization: Bearer <token>`.
The probe endpoints do not — see [Probes](#probes-and-the-liveness-trap).

| Method | Path | Description |
|---|---|---|
| GET | `/api/v1/vms` | List VMs, paginated |
| GET | `/api/v1/nodes/{id}/inventory` | One node with its VMs |
| GET | `/healthz` | Liveness — process is alive |
| GET | `/readyz` | Readiness — database is reachable |

**Listing VMs.** Query parameters: `limit` (default 20, capped at 100),
`sort` (`id_asc`, `ram_asc`, `ram_desc`, `name_asc`), `cursor`.

```bash
curl -H "Authorization: Bearer $TOKEN" \
  'localhost:8080/api/v1/vms?limit=3&sort=ram_desc'
```

```json
{
  "items": [ { "id": 499898, "node_id": 19, "name": "vm-0499898", "ram": 65024, "status": "running" } ],
  "limit": 3,
  "count": 3,
  "next_cursor": "eyJzIjoicmFtX2Rlc2MiLCJrIjoiNjUwMjQiLCJpIjo0OTk1MzN9"
}
```

Pass `next_cursor` back as `?cursor=` to get the following page. When the
response omits `next_cursor`, there is nothing more to fetch.

## Design notes

### Keyset pagination instead of OFFSET

This is the main thing the project is about.

`OFFSET` looks like it skips rows. It does not — Postgres walks the index and
discards every skipped entry, so the cost grows linearly with page depth. On
500,000 rows, measured with `EXPLAIN (ANALYZE, BUFFERS)`:

| | `OFFSET 490000` | Keyset |
|---|---|---|
| Execution time | **129.402 ms** | **0.060 ms** |
| Rows read | 490,020 | 20 |
| Buffers | 272,557 | 10 |

Roughly **2000× faster**, and the keyset cost stays flat however deep the
client pages. Both queries use the same index; the difference is entirely in
how they enter it:

```sql
-- OFFSET: reads 490,020 index entries, returns 20
ORDER BY ram DESC, id DESC LIMIT 20 OFFSET 490000

-- Keyset: one index seek, reads exactly 20
WHERE (ram, id) < ($1, $2) ORDER BY ram DESC, id DESC LIMIT 20
```

Full query plans are in [`docs/pagination-benchmark.txt`](docs/pagination-benchmark.txt).

Three details that make it correct rather than just fast:

**Every ordering ends in `id`.** `ram` is not unique — 500k rows share about
128 distinct values. Without a tiebreaker the ordering is not total, and rows
sharing a value shuffle between pages: some are returned twice, others never.
The composite indexes match the sort clauses exactly, so the row-value
comparison is a single seek rather than a filter.

**Cursors are opaque.** They are base64 of a small JSON object, and clients
are expected to echo them back rather than construct them. That keeps the
encoding free to change — adding a column to the key, switching to a different
tiebreaker — without breaking every consumer.

**Cursors are bound to their sort order.** Resuming a `ram_desc` scan with a
`name_asc` cursor is rejected with 400 instead of silently returning garbage:

```json
{"error": "cursor belongs to sort \"ram_desc\", not \"name_asc\""}
```

The trade-off is real: keyset cannot jump to an arbitrary page number, only
move forward from a known position. For an API that is the right shape anyway,
and it is why the response carries a cursor instead of a total page count.

### Probes and the liveness trap

`/healthz` and `/readyz` look similar and mean opposite things.

- **`/readyz` checks the database.** A replica that cannot reach Postgres
  should not receive traffic, so it drops out of the Service endpoints.
- **`/healthz` deliberately does not.** Liveness answers "should this process
  be killed and restarted", and a dead database is not a reason to restart a
  healthy process. Wiring liveness to an external dependency turns an outage
  into a CrashLoopBackOff on top of the outage.

Verified by stopping Postgres with the service running:

```
NAME                             READY   STATUS    RESTARTS
inventory-api-8496986df6-fzmgm   0/1     Running   0
inventory-api-8496986df6-xb6xv   0/1     Running   0
```

Both replicas went `NotReady` within seconds, the endpoint slice flipped to
`ready: false, serving: false`, and **restarts stayed at zero**. Starting
Postgres brought them back to `1/1`, still with no restarts.

`readinessProbe.failureThreshold: 3` softens the other edge of this: a brief
database blip would otherwise empty every endpoint at once and take the whole
service down rather than degrading it.

### Startup retries

`sql.Open` does not connect — it only builds a pool, so the first real check
is a ping. Failing fast on that ping is wrong in a container: the database has
its own lifecycle, and "not ready yet" is normal during a deploy.

Startup retries with exponential backoff (500 ms → 5 s, 20 s total budget):

```
WARN database not ready, retrying  attempt=1 retry_in=500ms
WARN database not ready, retrying  attempt=2 retry_in=1s
WARN database not ready, retrying  attempt=3 retry_in=2s
INFO database connected
```

The budget is bounded on purpose. Retrying forever would turn a typo in the
DSN into a pod that starts indefinitely and never fails — worse than an honest
crash. The loop also watches the signal context, so SIGTERM during startup
exits immediately instead of sitting out the remaining budget.

### Container and deployment

**distroless, not alpine.** The binary is static (`CGO_ENABLED=0`), so the
runtime needs nothing but ca-certificates and tzdata. The result is 20 MB with
no shell and no package manager. Losing `kubectl exec` for debugging is a real
cost, paid because `readOnlyRootFilesystem` makes a shell nearly useless
anyway and ephemeral containers (`kubectl debug --target=api`) cover the case.

**No CPU limit, memory limit only.** CPU is compressible — a limit causes
throttling that surfaces as unexplained latency spikes. Memory is not, so it
is capped. Requests are set for both.

**Graceful shutdown is sequenced with the cluster.** Removing a pod from
Service endpoints and stopping its traffic are not synchronous, so a 5 s
`preStop.sleep` runs before the process starts refusing connections.
`terminationGracePeriodSeconds: 30` is longer than that sleep plus the 10 s
shutdown timeout — otherwise the kubelet would SIGKILL mid-shutdown.

BuildKit cache mounts on the module and build caches keep rebuilds at ~2 s
against ~19 s cold, without the caches ending up in the image.

### Security

- Config entirely from the environment; the process refuses to start without
  `DATABASE_URL` and `API_TOKEN`. No credentials in the repository, and
  `k8s/secret.example.yaml` is a template — the real Secret is created out of
  band.
- Token comparison uses `subtle.ConstantTimeCompare`. A plain `!=` returns
  early on the first mismatched byte, which leaks the token through response
  timing.
- Sort orders are a closed whitelist mapped from user input, so no part of the
  `ORDER BY` clause is ever attacker-controlled.
- Container runs as UID 65532 with `runAsNonRoot`, `readOnlyRootFilesystem`,
  all capabilities dropped, `seccompProfile: RuntimeDefault`, and
  `automountServiceAccountToken: false`.

## Deployment

```bash
kubectl create secret generic inventory-api-secret \
  --from-literal=DATABASE_URL="postgres://..." \
  --from-literal=API_TOKEN="$(openssl rand -hex 32)"

make docker            # build, tagged with the git SHA
make k3s-import        # load into containerd
make deploy            # apply manifests, wait for rollout
```

Images are tagged by commit SHA, never `latest` — `latest` with
`imagePullPolicy: IfNotPresent` silently keeps a stale image running forever.
`maxUnavailable: 0` means a rollout brings up the new pod and waits for it to
be ready before removing the old one.

Two things that cost me time and are worth writing down:

`k3s ctr images import` needs `-n=k8s.io`. Without the namespace flag the
image lands where the kubelet cannot see it, and the pod fails with
`ErrImagePull` and no useful explanation.

k3s defaults its pod network to `10.42.0.0/16`. My LAN is `10.42.0.0/24`, so
`cni0` came up holding the gateway address and the node lost its own network.
Installing with `--cluster-cidr=10.244.0.0/16 --service-cidr=10.245.0.0/16`
fixes it. Worth checking before the first deploy rather than after.

## Known limitations

Kept honest rather than quiet:

- **`/nodes/{id}/inventory` is unpaginated.** A node with 25,000 VMs returns
  all of them in one response. It needs the same cursor treatment as the list
  endpoint, or should return a count with a link.
- **Auth is a single static bearer token.** Enough to keep the endpoints from
  being open; not a real authentication scheme. No per-client identity, no
  rotation, no scopes.
- **Redis is in the compose file but unused.** Cache-aside for node inventory
  is the obvious next step.
- **No OpenAPI specification** — the table above is the contract.
- **Test coverage is limited to cursor encoding and validation.** The database
  layer needs integration tests with testcontainers; the handlers need
  `httptest` coverage of the status-code paths.
- **`sslmode=disable`** is acceptable on a trusted LAN segment and would not
  be in production.

## Repository layout

```
cmd/api/              entrypoint: wiring, server lifecycle, shutdown
internal/config/      environment configuration and validation
internal/store/       models, SQL, cursor encoding
internal/httpapi/     handlers, middleware, routing
migrations/           goose migrations
testdata/seed.sql     500k-row dataset for benchmarking
k8s/                  manifests
docs/                 benchmark output
```

## License

MIT
