# Devo

**DevEx, Operationalised.**

Devo gives application developers a consistent way to provision the infrastructure their apps depend on. Platform teams define reusable service modules; developers choose what they need; Devo assembles a self-contained local development environment, ready to run.

Less time wiring together development environments. Fewer differences between projects. More time spent building the application itself.

## Why Devo

Devo provides a registry that maintains clean service boundaries between plaftorm and the applications it enables. The registry holds service definitions: parameters with defaults, a Compose definition, and the files the service writes into the project. `devo.yaml` holds per-application setting, plus per-service parameter overrides. `compose.yaml` and the service files are generated from the two and live in the project repository.

The registry resolves in layers: built-in entries embedded in the binary, then `~/.config/devo/services.yaml`, then `./devo.services.yaml`, each adding to or replacing entries from the layer before it. A change to a definition reaches a project when that project regenerates. Because the user-level layer sits outside the repository, the same `devo.yaml` can generate different output on two machines.

Devo owns registry resolution and generation; the lifecycle is edit and regenerate. `devo init <template> <name>` creates a project, and `devo init <template> .` adopts an existing repository and marks it `adopted: true`. `devo add` and `devo remove` edit `devo.yaml` and regenerate `compose.yaml` and the service files; `devo dev` starts the stack with `docker compose`. Devo's responsibility ends at the generated files. They are ordinary files in the repository, Compose is the only execution target, and no Devo process runs alongside the stack.

## Application Runtimes

Devo supports application development across multiple language ecosystems:

- Rust
- Go
- Python
- TypeScript using Bun

The Devo CLI is implemented in Go, but application developers do not need to write their applications in Go. Devo provides a consistent interface for provisioning local infrastructure regardless of the application's language or runtime.

## Service Catalog

Devo provides an extensible catalog of independently selectable infrastructure services. Modules are opt-in: initializing a project does not automatically enable every available service.

The initial catalog includes:

| Module | Purpose | Default endpoint |
|---|---|---|
| PostgreSQL | Relational database | `localhost:5432` |
| Redis | Caching and ephemeral data | `localhost:6379` |
| RabbitMQ | Message broker | `localhost:5672` (AMQP), `http://localhost:15672` (management UI) |
| ClickHouse | Analytical database | `http://localhost:8123` |
| Grafana | Metrics visualization and dashboards | `http://localhost:3000` |
| OpenTelemetry Collector | Telemetry collection, processing, and export | `localhost:4317` (OTLP/gRPC), `localhost:4318` (OTLP/HTTP) |
| Loki | Log aggregation and querying | `http://localhost:3100` |
| Tempo | Distributed trace storage and querying | `http://localhost:3200` |
| S3-compatible object storage | Object storage for files, artifacts, and application data | Implementation-dependent |

Each module has its own configuration and provisioning requirements. Developers select the services their applications need, and Devo assembles those selections into the generated environment.

Modules can be selected independently or combined to form a complete local development stack:

- **Application backend:** PostgreSQL and Redis.
- **Analytics:** ClickHouse and Grafana.
- **Observability:** OpenTelemetry Collector, Loki, Tempo, and Grafana.
- **Object storage:** An S3-compatible service selected for local development.
- **Full development environment:** Any combination of available modules.

All services are opt-in, including observability components. Selecting Grafana does not automatically require Loki or Tempo, and selecting a telemetry backend does not force every other observability component into the environment.

The catalog is designed to grow alongside platform requirements. Adding a service should mean adding a reusable building block, not rebuilding the development workflow for every application.

## Quick Start

### Prerequisites

- Docker Engine or Docker Desktop with Docker Compose. Devo uses `docker compose` (the v2 plugin), or the standalone `docker-compose` binary if the plugin isn't installed.
  On macOS, [Colima](https://github.com/abiosoft/colima) also works (`brew install colima docker docker-compose`). `devo dev` starts it automatically if it isn't running. Colima shares only your home directory with containers, so keep projects under it, and its default VM (2 CPUs, 2 GB) is small for several services at once; to give it more, run `colima start --cpu 4 --memory 8` once before `devo dev`.
-  On first use, initialize Colima with `colima start` to create the VM and activate its Docker context. After initialization, `devo dev` can start Colima automatically when it is stopped, provided the active Docker context remains `colima`. If you switch contexts, restore it with `docker context use colima`. Colima shares only your home directory with containers by default, so keep projects under it. Its default VM (2 CPUs, 2 GB) may be small for several services; resize an existing VM with `colima stop` followed by `colima start --cpu 4 --memory 8`.
- Go 1.24+ to install Devo from source.

Application-specific runtime requirements depend on the application being developed.

### Installation

From the Devo source repository:

```sh
make install
```

The Makefile injects the version from the latest Git tag or commit. Override it for a release build with `make install VERSION=1.2.3`.

### Initialize an application

Create a project with the infrastructure it needs:

```sh
devo init orders-api --with postgres --with redis
cd orders-api
devo services
devo dev
```

This initializes the project with PostgreSQL and Redis selected. Other available services remain excluded unless explicitly requested.

For an analytics application:

```sh
devo init analytics-api --with clickhouse --with grafana
```

For an application that needs local observability:

```sh
devo init orders-api \
  --with postgres \
  --with otel \
  --with loki \
  --with tempo \
  --with grafana
```

The selected modules define the environment Devo generates. A project that needs only one service can select just that service.

### Add Devo to an existing project

Devo can adopt a repository that already exists. From the project root:

```sh
devo init rust .
devo add clickhouse
devo add -m clickhouse    # also apply platform/clickhouse/init migrations; rolls back with the .down.sql files on failure
```

`devo init <template> .` writes only `devo.yaml` and `compose.yaml` into the current directory. It generates no template files, enables no services, and never overwrites an existing `devo.yaml` or `compose.yaml`; it refuses instead. The language comes from the template argument.

The project name is the directory name, lowercased, so a checkout called `OvenRouter` becomes `ovenrouter`. Names may contain only lowercase letters, digits, `-` and `_`. Pass `--name` to choose one when the directory name doesn't qualify (flags go before the template):

```sh
devo init --name my-repo rust .
```

Passing the directory's own name does the same thing: inside `OvenRouter/`, `devo init rust ovenrouter` adopts the current directory, matching case-insensitively, rather than creating `ovenrouter/ovenrouter`. Any other name still creates a new child directory and refuses if it already exists.

Devo records an adopted project with `adopted: true` in `devo.yaml`. For these projects, the application's own files take priority:

- Services don't write their demo content: seed SQL, example dashboards, or smoke tests.
- Init-script directories such as `platform/clickhouse/init` aren't mounted into the container, so the image never runs the application's own migrations. Apply them with `devo add -m clickhouse`.
- ClickHouse keeps the standard `app`/`app` user and password, so there are credentials to harden before shipping (set `password:` under a `clickhouse:` block in `devo.yaml`). It doesn't get the seeded `app` database; the application's migrations create its schema.
- `devo remove` leaves files at those paths alone.

Projects created with `devo init <template> <name>` are unchanged and keep the full scaffold.

## CLI Reference

### Select modules during initialization

Include or exclude infrastructure modules when creating a project:

```sh
devo init orders-api --with postgres
devo init orders-api --with redis
devo init analytics-api --with clickhouse --with grafana
devo init traced-api --with otel --with tempo
```

### Manage dependencies

Modify infrastructure modules as an application's requirements change:

```sh
devo add redis
devo remove grafana
devo services
```

### Operate the environment

```sh
devo dev    # Start configured services in the background; returns once they're healthy
devo dev -s # Services only: start Compose services, skip scripts/start/ application scripts
devo logs   # Stream infrastructure and application logs
devo down   # Stop and remove local containers
```

### Application lifecycle scripts

The Rust template includes `scripts/start/` and `scripts/stop/` for application
processes that run outside Compose. Pair scripts by filename: for example,
`scripts/start/api.sh` matches `scripts/stop/api.sh`. Devo runs start scripts in
filename order after Compose services are healthy, and runs stop scripts in
reverse order before Compose teardown. Scripts run through `/bin/sh` from the
project root and inherit the environment. Start scripts must return after
launching the application, redirect long-running output to a log file, and
record the PID so the matching stop script can stop that exact process. Hooks
must be idempotent: repeated starts must not launch duplicate processes, and
stops should be safe when a process is already stopped. A failed start triggers
rollback of attempted application scripts and Compose. Projects without scripts
remain supported.

## Configuration

Every project's `devo.yaml` records its name and language, then lists every service as `true` or `false`. Projects adopted with `devo init <template> .` also carry `adopted: true` after the language. Below that, an optional block per service overrides that service's defaults.

```yaml
name: orders-api
language: rust

services:
  clickhouse: false
  grafana: true
  loki: false
  postgres: true
  rabbitmq: false
  ollaya: false
  redis: false
  tempo: false

# Per-service config. Only needed when you want to override defaults.
grafana:
  admin_user: admin
  admin_password: admin

postgres:
  version: "16"
  database: app
  user: app
```

`devo add` and `devo remove` change only the matching line in the services list; comments and override blocks are left alone. Removing a service also removes the files Devo generated for it. A setting that a service doesn't define is rejected with the list of valid options, so typos fail early. Quote version numbers (`"16"`, not `16`).

### Smoke tests

Every service ships with a minimal smoke test for the project's language, currently Rust. In a new project, `devo add` writes it into `tests/` (adopted projects skip it), and with the stack running (`devo dev`) `cargo test` runs it. Where a service has something to write, the test writes and reads it back (ClickHouse rows, Redis keys, Loki log entries, Tempo traces); otherwise it checks that the service is up and speaking its protocol (Grafana, PostgreSQL, RabbitMQ). The tests use only the standard library, so adding a service never edits `Cargo.toml`.

Devo never overwrites a smoke-test file that already exists. When a service is disabled, Devo removes its declared smoke-test file only if no other enabled service declares the same path. This lets developers edit generated tests without Devo overwriting them, while keeping shared test paths available to services that still use them.

Service `files` and datasource provisioning files are generated artifacts. Devo regenerates these files from the enabled service definitions and removes them when their last declaring service is disabled. Files that no enabled service declares are left untouched. Avoid placing hand-maintained content at paths declared by a service's `files` or datasource configuration.

### Service definitions

Modules are defined declaratively in a YAML service registry rather than in code. The built-in catalog is embedded in the Devo binary, and platform teams can extend or override it with their own registry files, lowest precedence first:

1. The built-in catalog
2. `~/.config/devo/services.yaml`: additions available to every project
3. `./devo.services.yaml`: additions for a single project

ike `devo.yaml`, a registry file starts with a `services:` list, then has one block per service. A service block has a `description`, tunable `params` with defaults, and a `compose` definition that is copied into the generated `compose.yaml`. It can also declare `files` to write into the project when the service is enabled, and `smoke` tests per language. `samples` lists the `files` paths that are demo content, which adopted projects skip. An `adopted` block overrides `params` and individual `compose` keys for adopted projects; an empty value, such as `environment: {}`, removes that key. A misspelled or unknown field in a block is rejected. Adding a service to the catalog means adding a block; Devo itself doesn't change.
 

## Architecture

Devo separates infrastructure definitions from the applications that consume them.

### Reusable infrastructure modules

Each module packages the configuration and provisioning artifacts required to run a supported service locally. Modules provide a consistent interface for selecting and operating infrastructure without requiring every application team to maintain its own implementation.

### Application-specific environments

Each generated project receives its own environment configuration and the artifacts required by its selected modules.

Depending on the selected services, these artifacts can include:

- `devo.yaml` for environment and module configuration.
- `compose.yaml` for local service orchestration.
- Database initialization scripts.
- `tests/` with a smoke test for each selected service.
- Service configuration and provisioning files.
- Grafana dashboard and data source provisioning definitions.
- OpenTelemetry Collector pipeline configuration.
- Object storage configuration and initialization artifacts.

Only the selected modules contribute their service-specific artifacts to the generated environment.

### Independent operation

Generated projects carry their own infrastructure configuration and provisioning artifacts. They can be versioned alongside application code and used in individual developer environments or suitable CI/CD workflows without requiring a persistent Devo server or a checkout of the Devo source repository.

Docker and Docker Compose remain the runtime requirements for containerized infrastructure. The Devo CLI provides the interface for generating and managing the environment.

## Design Principles

**Infrastructure as a product.** Platform teams expose supported capabilities through reusable modules rather than requiring every application team to reinvent infrastructure setup.

**Developer self-service.** Developers choose the services their applications require through a consistent CLI.

**Opt-in by default.** Projects include the infrastructure they select, not every service available in the catalog.

**Reproducible environments.** Configuration and provisioning artifacts travel with the application, making infrastructure setup explicit and repeatable.

**Language independence.** The infrastructure workflow remains consistent across Rust, Go, Python, and TypeScript applications running on Bun.

**Loose coupling.** Devo generates and manages application-specific environments without requiring a centralized runtime service.

**Composable infrastructure.** Modules can be combined according to application requirements instead of imposing one standard stack on every project.

## Project Status

Devo establishes a common developer interface for local infrastructure provisioning, starting with relational data, caching, analytics, observability, and object storage.

The initial release focuses on Docker Compose-based environments and an extensible module model. The objective is to make infrastructure easier to consume, environments easier to reproduce, and the everyday development workflow more consistent across application teams.
