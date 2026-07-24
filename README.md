# MiniSandbox

MiniSandbox is an all-Go sandbox runtime scaffold based on the architecture
described in [`docs/all-go-agent-sandbox-runtime-design.md`](docs/all-go-agent-sandbox-runtime-design.md).

The repository starts with three process boundaries:

- `sandboxd`: host-side lifecycle control plane.
- `sandbox-init`: minimal PID 1 for each sandbox container.
- `runnerd`: in-container command execution data plane.

The current revision is an initialization scaffold. It provides compilable
entry points, package boundaries, protocol models, health endpoints, OpenAPI
stubs, and build configuration. Docker reconciliation, SQLite persistence, and
command execution intentionally return `not implemented` until their respective
milestones are completed.

The detailed, review-sized implementation sequence for the first development
stage is in
[`docs/phase-1-docker-lifecycle-development-plan.md`](docs/phase-1-docker-lifecycle-development-plan.md).

## Quick start

```bash
go test ./...
go run ./cmd/sandboxd
```

The default health endpoint is `http://127.0.0.1:8080/healthz`.

## Build Linux artifacts

```bash
make build
```

`make build` first creates static `runnerd` and `sandbox-init` binaries, embeds
them into `sandboxd`, and writes the final binaries to `bin/`.

## Repository layout

```text
cmd/        process entry points
api/        public OpenAPI contracts
internal/   control plane, runtime adapters, runner, and persistence
pkg/        stable wire protocol types
sdk/go/     Go client SDK
tests/      contract, integration, and security test suites
configs/    example configuration
docs/       architecture and design documents
```

The module path is intentionally local (`minisandbox`) while the repository has
no configured remote. Change it before publishing:

```bash
go mod edit -module example.com/your-org/mini-sandbox
```
