# devo

The fast path from laptop to production.

`devo` scaffolds a new app from a platform template and manages the local
services it runs against (databases, queues, observability) with Docker Compose.
You say which services you want in one small YAML file; devo generates the rest.

```bash
devo init rust my-app
cd my-app
devo add postgres
devo add grafana
devo dev
```

## Requirements

- **Docker** with Compose v2, for `devo dev`, `down` and `logs`
- **Go**, to build devo from source
- **Rust / cargo**, to run the apps that `devo init rust` generates

## Install

Build from source:

```bash
go build -o devo ./cmd/devo
sudo install -m 755 devo /usr/local/bin/devo   # put it on your PATH
devo --version
```

`go install ./cmd/devo` also works if `~/go/bin` is on your `PATH`.
For cutting and publishing binaries, see [docs/RELEASING.md](docs/RELEASING.md).

## Commands

| Command | What it does |
| --- | --- |
| `devo init <template> <name>` | Scaffold a new app from a platform template |
| `devo add <service> [--dir .]` | Enable a service in the current project |
| `devo remove <service> [--dir .]` | Disable a service in the current project |
| `devo services [--dir .]` | List services and whether each is enabled |
| `devo dev [--dir .]` | Start the local stack (`docker compose up --build`) |
| `devo logs [--dir .]` | Follow logs from the local stack |
| `devo down [--dir .]` | Stop the local stack |
| `devo -h`, `--help` | Show help |
| `devo -v`, `--version` | Print version |

`--dir` points at a project directory other than the current one.

Templates available today: `rust`.

## Project config: `devo.yaml`

Every devo project has a `devo.yaml` at its root. The top of the file is a plain
list of services, each `true` or `false`. Below it, an optional block per service
overrides that service's defaults.

```yaml
name: "my-app"

services:
  clickhouse: true
  grafana: true
  loki: false
  tempo: false
  postgres: false
  redis: false
  rabbitmq: false

# Per-service config. Only needed when you want to override defaults.
clickhouse:
  version: "25.3"
  database: app

grafana:
  admin_user: admin
  admin_password: admin

postgres:
  version: "16"
  database: app
  user: app
```

`devo add` and `devo remove` change only the matching line under `services:`.
Your comments, blank lines and override blocks are left alone. `remove` sets the
service to `false` rather than deleting the line.

After a change, devo regenerates `compose.yaml` and the service files under
`platform/`. Don't hand-edit those; edit `devo.yaml` instead.

Mistakes fail loudly: an unknown service, an unknown top-level key, or a setting
a service doesn't have (for example `postgres: {versoin: "16"}`) is an error that
lists the valid options.

Quote versions (`"16"`, not `16`). An unquoted `16.0` is parsed as a number and
becomes `16`.

## Services

All services ship with the binary. Every database and queue defaults to
`app` / `app` for credentials, and ports are published on localhost.

| Service | Image | Ports | Settings (default) |
| --- | --- | --- | --- |
| `clickhouse` | `clickhouse/clickhouse-server` | 8123, 9000 | `version` (25.3), `database`, `user`, `password` (all `app`) |
| `postgres` | `postgres` | 5432 | `version` (17), `database`, `user`, `password` (all `app`) |
| `redis` | `redis` | 6379 | `version` (7) |
| `rabbitmq` | `rabbitmq:<version>-management` | 5672, 15672 (UI) | `version` (4), `user`, `password` (both `app`) |
| `grafana` | `grafana/grafana` | 3000 | `version` (11.6.0), `admin_user`, `admin_password` (both `admin`) |
| `loki` | `grafana/loki` | 3100 | `version` (3.4.2) |
| `tempo` | `grafana/tempo` | 3200, 4317 (OTLP gRPC), 4318 (OTLP HTTP) | `version` (2.7.2) |

**Grafana wiring.** Any enabled service that provides a datasource (ClickHouse,
Postgres, Loki, Tempo) is provisioned into Grafana automatically. The first one
alphabetically becomes the default datasource. Grafana waits for providers that
define a healthcheck before starting.

**Data.** ClickHouse, Postgres and RabbitMQ keep their data in named Docker
volumes, so it survives `devo down`. Loki and Tempo don't persist data.

## The service registry

Which services exist, and how each one is configured, is defined in a YAML
registry, not in Go code. The built-in registry is
[`internal/services/default_services.yaml`](internal/services/default_services.yaml),
embedded in the binary at build time.

Like `devo.yaml`, a registry file starts with a `services:` list of true/false
(the state a **new** project starts in), followed by one config block per service,
named after the service.

```yaml
services:
  redis: false

redis:
  description: Redis cache
  params:                    # tunable settings, with defaults
    version: "7"
  compose:                   # copied verbatim into compose.yaml
    image: "redis:{{version}}"
    ports: ["6379:6379"]
    healthcheck:
      test: ["CMD", "redis-cli", "ping"]
      interval: 5s
      timeout: 3s
      retries: 20
```

### Service block fields

| Field | Purpose |
| --- | --- |
| `description` | Shown by `devo services` |
| `params` | Settings and their defaults. Overridden by a block of the same name in `devo.yaml`. Reference as `"{{param}}"`; `{{project_name}}` is always available |
| `compose` | Compose service definition, copied into `compose.yaml` as is |
| `volumes` | Named volumes to declare in `compose.yaml` |
| `files` | Map of path (relative to the project root) to content, written when the service is enabled |
| `datasource` | A Grafana datasource this service provides |
| `datasources_file` | Marks a service as a datasource consumer (Grafana). All enabled datasources are written to this path |

Quote any value containing `{{...}}` in YAML, otherwise a leading `{` is parsed
as a map. Only known parameter names are substituted, so other `{{...}}` text
(Grafana legend formats, for example) is left untouched.

`name` and `services` are reserved and can't be used as service names.

### Adding your own services

devo layers registry files, lowest precedence first:

1. Built-in defaults (embedded)
2. `~/.config/devo/services.yaml`: yours, on every project
3. `./devo.services.yaml`: this project only

A service in a later layer replaces a same-named one from an earlier layer
entirely (it isn't merged field by field). A later `services:` list overrides
only the keys it mentions. Missing files are skipped.

So to add a service with no Go changes, drop a block like the Redis example above
into `devo.services.yaml`, then run `devo add <name>`.

## Templates

`devo init <template> <name>` copies a template from
`internal/project/embedded_templates/<template>/` into `./<name>`. Files ending
in `.tmpl` have the suffix stripped and `{{project_name}}` replaced with the app
name. devo refuses to overwrite an existing directory, and removes a
half-written one if generation fails.

App names may contain lowercase letters, digits, `-` and `_`, and can't start
with `-`.

## Repository layout

```
cmd/devo/                 entry point (calls cli.Run)
internal/
  cli/                    command parsing, help text, command handlers
  project/
    generate.go           template scaffolding
    config.go             devo.yaml: read, write, toggle a service
    compose.go            compose.yaml + service files from the registry
    embedded_templates/   app templates (embedded in the binary)
  services/
    services.go           registry loader and parameter resolution
    default_services.yaml built-in service registry (embedded)
docs/RELEASING.md         how to cut a release
```

## Development

```bash
go build ./...            # build everything
go test ./...             # run tests
go build -o devo ./cmd/devo
```

`//go:embed` can only reach files at or below the Go file that declares it, so
`embedded_templates/` must stay inside `internal/project/` and
`default_services.yaml` next to `services.go`. Don't put `.go` files inside
`embedded_templates/`; everything there is copied into new projects.