# Cellar Run Control

Cellar Run Control is a local Go HTTP service for coordinating a small
production cellar. It keeps formula approval, vessel availability, active
fermentation runs, gravity observations, conditioning, completion metrics, and
vessel cleaning in one deterministic process.

Operational parameters shared by the day and night shifts — per-request
timeout, log level, and request body limit — can be published at runtime by a
shift supervisor, inspected, and rolled back to the previous confirmed
edition, without restarting the service.

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
internal/httpapi               HTTP routes, request-level config pinning, errors
internal/logging               runtime-switchable leveled logger
internal/runtimeconfig         versioned runtime parameters and rollback history
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

Runtime configuration workflow:

- `GET /v1/runtime-config`
- `POST /v1/runtime-config/publish`
- `POST /v1/runtime-config/rollback`

`GET /v1/runtime-config` returns the active edition and, when present, the
edition immediately preceding it:

```json
{
  "current": {
    "revision": 2,
    "request_timeout_seconds": 10,
    "max_request_body_bytes": 1048576,
    "log_level": "debug",
    "note": "night shift",
    "published_at": "2026-10-07T22:10:00Z"
  },
  "previous": {
    "revision": 1,
    "request_timeout_seconds": 20,
    "max_request_body_bytes": 1048576,
    "log_level": "info",
    "note": "built-in defaults",
    "published_at": "2026-10-07T20:00:00Z"
  }
}
```

Publish a complete edition (optionally pinning the expected current revision
with `X-Expected-Version` to guard against concurrent supervisors):

```bash
curl -sS -X POST http://127.0.0.1:8088/v1/runtime-config/publish \
  -H 'X-Expected-Version: 1' \
  -H 'Content-Type: application/json' \
  -d '{
    "request_timeout_seconds": 10,
    "max_request_body_bytes": 1048576,
    "log_level": "debug",
    "note": "night shift"
  }'
```

Field bounds are 1–3600 seconds, 1024–16 MiB, and log levels `debug`,
`info`, `warn`, `error`. Roll back to the previous confirmed edition with an
optional body and an optional revision pin:

```bash
curl -sS -X POST http://127.0.0.1:8088/v1/runtime-config/rollback \
  -H 'X-Expected-Version: 2' \
  -H 'Content-Type: application/json' \
  -d '{"note": "restore day parameters"}'
```

Every response carries `X-Runtime-Config-Revision`. A request sees exactly
the edition active when it started: its timeout, body limit, and log level do
not change even if a publish commits while the request is running.

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

## Workflow Checks

Run each production workflow check separately:

```bash
go run ./cmd/fermentctl check formula-approval
go run ./cmd/fermentctl check vessel-run-lifecycle
go run ./cmd/fermentctl check observations-completion
go run ./cmd/fermentctl check runtime-config-publish
```

Each check creates a temporary snapshot directory, starts the real HTTP
handler on a loopback listener, issues JSON requests, verifies success and
failure behavior, and removes its temporary data.

## Persistence And Recovery

The service stores a complete revisioned snapshot at
`<data-directory>/state.json` and the operational parameter document at
`<data-directory>/runtime-config.json`. Either write creates a temporary file
in the same directory, commits the encoded bytes durably, closes the handle,
and renames the result over the previous file. The in-memory state is replaced
only after that operation succeeds.

A failed configuration publication cannot overwrite the previous edition:
validation, stale revision checks, encoding, and disk errors all occur before
the atomic rename. The runtime configuration document always contains the
confirmed current edition and, when one exists, the immediately preceding
edition. On startup the document and its revision chain are decoded and
validated; an invalid document stops the process before HTTP serving begins
rather than serving unverified parameters.

On startup, an existing snapshot is decoded and normalized. Unknown or
conflicting map indexes are not silently invented; invalid JSON or a negative
revision stops the process before HTTP serving begins.

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
