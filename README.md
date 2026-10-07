# Cellar Run Control

Cellar Run Control is a local Go HTTP service for coordinating a small
production cellar. It keeps formula approval, vessel availability, active
fermentation runs, gravity observations, conditioning, completion metrics, and
vessel cleaning in one deterministic process.

This repository is a healthy initialization baseline. It has no intentional
defects and no automated test suite. Tests are intentionally deferred to a
later engineering task; the production workflow checks in this repository
remain available for immediate build and behavior verification.

## Requirements

- Go 1.23 or later
- A writable local data directory
- No database, network service, or third-party module

## Build And Run

Build every package:

```bash
go build ./...
```

Start the service:

```bash
go run ./cmd/fermentctl serve
```

The default address is `127.0.0.1:8088`. The default snapshot directory is
`./data`.

Environment variables:

- `CELLAR_CONTROL_ADDR`: HTTP listen address.
- `CELLAR_CONTROL_DATA_DIR`: directory containing `state.json`.

Example:

```bash
CELLAR_CONTROL_ADDR=127.0.0.1:9099 \
CELLAR_CONTROL_DATA_DIR=/tmp/cellar-control \
go run ./cmd/fermentctl serve
```

## Directory Structure

```text
cmd/fermentctl                 service and workflow-check executable
internal/clock                 time boundary
internal/config                environment configuration
internal/domain                entities, validation, transitions, metrics
internal/httpapi               HTTP routes, JSON decoding, errors
internal/service               use-case orchestration
internal/storage               revisioned snapshot persistence
internal/workflowcheck         real HTTP production checks
```

## Public API

Health:

- `GET /healthz`

Formula workflow:

- `POST /v1/formulas`
- `GET /v1/formulas`
- `GET /v1/formulas/{id}`
- `PUT /v1/formulas/{id}/steps`
- `POST /v1/formulas/{id}/approve`
- `POST /v1/formulas/{id}/retire`

Vessel workflow:

- `POST /v1/vessels`
- `GET /v1/vessels`
- `GET /v1/vessels/{id}`
- `POST /v1/vessels/{id}/finish-cleaning`
- `POST /v1/vessels/{id}/retire`

Run workflow:

- `POST /v1/runs`
- `GET /v1/runs`
- `GET /v1/runs/{id}`
- `POST /v1/runs/{id}/reserve`
- `POST /v1/runs/{id}/start`
- `POST /v1/runs/{id}/observations`
- `POST /v1/runs/{id}/conditioning`
- `POST /v1/runs/{id}/complete`
- `POST /v1/runs/{id}/abort`

Runtime config workflow:

- `GET /v1/runtime-config`
- `GET /v1/runtime-config/versions`
- `POST /v1/runtime-config/publish`
- `POST /v1/runtime-config/rollback`

Writes accept an optional `X-Expected-Version` header. Errors use a stable
shape:

```json
{
  "error": {
    "code": "revision_conflict",
    "detail": "the aggregate version does not match X-Expected-Version",
    "field": ""
  }
}
```

## Runtime Config Publishing

The request timeout, log level, and in-flight request limit are runtime
parameters that supervisors can republish without restarting the service.
Parameters are published as a complete set:

```json
{
  "config": {
    "request_timeout_ms": 1500,
    "log_level": "debug",
    "max_in_flight_requests": 8
  },
  "published_by": "night-lead"
}
```

`POST /v1/runtime-config/publish` validates the set and commits it as the next
version (`201`). `GET /v1/runtime-config` returns the currently effective
version and the rollback target; `GET /v1/runtime-config/versions` lists the
committed history. `POST /v1/runtime-config/rollback` commits the previous
version's parameters as a new version, so history stays append-only. Publishes
accept `X-Expected-Version`; a second supervisor publishing against a stale
version receives `revision_conflict` instead of overwriting the winner.

Guarantees:

- Each request pins one committed version at arrival and observes only that
  version for its whole lifetime. Responses carry the pinned version in the
  `X-Runtime-Config-Version` header.
- A publish becomes effective only after it is durably committed
  (temporary file, fsync, atomic rename). A mid-publish disk failure returns
  `storage_error` and leaves the previously confirmed version in place.
- A restart always recovers the last confirmed version; a failed publish is
  never recovered.

The pinned version is enforced per request: the in-flight limit rejects with
`429 request_limit_exceeded`, an exceeded request timeout fails with
`503 request_timeout`, and request logging follows the pinned log level
(`debug`/`info` log every request, `warn` logs 4xx/5xx, `error` logs 5xx).

## Workflow Checks

Run each production workflow check separately:

```bash
go run ./cmd/fermentctl check formula-approval
go run ./cmd/fermentctl check vessel-run-lifecycle
go run ./cmd/fermentctl check observations-completion
go run ./cmd/fermentctl check runtime-config-publishing
```

Each check creates a temporary snapshot directory, starts the real HTTP
handler on a loopback listener, issues JSON requests, verifies success and
failure behavior, and removes its temporary data.

## Persistence And Recovery

The service stores a complete revisioned snapshot at
`<data-directory>/state.json`. A write creates a temporary file in the same
directory, commits the encoded bytes durably, closes the handle, and renames
the result over the previous snapshot. The in-memory snapshot is replaced only
after that operation succeeds.

On startup, an existing snapshot is decoded and normalized. Unknown or
conflicting map indexes are not silently invented; invalid JSON or a negative
revision stops the process before HTTP serving begins.

Runtime configuration lives in `<data-directory>/runtime-config.json` and
follows the same commit discipline: the append-only version history is encoded
to a temporary file, committed durably, and atomically renamed over the
previous file. The in-memory state changes only after persistence succeeds, so
a failed publish never replaces the confirmed version. On startup the service
loads the committed history; a missing file is initialized with the built-in
default version, and a corrupt or inconsistent history stops the process.

## Repair-Ready Boundaries

The baseline intentionally keeps several independent behavior surfaces visible
for later engineering evaluation:

- formula name normalization and duplicate detection;
- target gravity and process-step range validation;
- aggregate version conflict checks;
- atomic vessel reservation and release;
- run state transition guards;
- observation sequence and time ordering;
- gravity movement and range checks;
- metric calculation and rounding;
- durable snapshot replacement and startup normalization.

These are production mechanisms, not injected defects. A later task can add
automated tests or request a focused repair without changing the public API.

## Test Boundary

This stage does not create unit tests, integration tests, test fixtures, or an
E2E suite. The later test task owns systematic coverage of validation limits,
state transitions, concurrency, persistence failures, and numeric edge cases.

## Scope

The service does not control physical equipment, authenticate remote users,
send notifications, expose a browser interface, or replicate data across
processes. It is intended for a trusted local network or an internal adapter.
