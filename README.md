# Devo

**DevEx, Operationalised.**

Devo gives application developers a consistent way to provision the infrastructure their applications depend on. Platform teams define reusable service modules; developers choose what they need; Devo assembles a self-contained local development environment, ready to run.

Less time wiring together development environments. Fewer differences between projects. More time spent building the application itself.

## Why Devo

Every application needs infrastructure. Too often, getting it running locally means piecing together container definitions, initialization scripts, service configuration, and project-specific instructions. Teams solve the same problems repeatedly, and small differences in setup accumulate across repositories.

Devo gives those recurring tasks a common interface.

Platform teams package supported infrastructure into reusable modules with consistent configuration and provisioning conventions. Application developers select the services their projects require without having to reconstruct the underlying setup each time.

The result is a more predictable development workflow, with infrastructure configuration that lives alongside the application and environments that developers can reproduce without depending on a central runtime service.

## Application Runtimes

Devo supports application development across multiple language ecosystems:

- Rust
- Go
- Python
- TypeScript using Bun

The Devo CLI is implemented in Go, but application developers do not need to write their applications in Go. Devo provides a consistent interface for provisioning local infrastructure regardless of the application's language or runtime.

## Infrastructure Modules

Devo provides an extensible catalog of independently selectable infrastructure services. Modules are opt-in: initializing a project does not automatically enable every available service.

The initial catalog includes:

| Module | Purpose | Default endpoint |
|---|---|---|
| PostgreSQL | Relational database | `localhost:5432` |
| Redis | Caching and ephemeral data | `localhost:6379` |
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

- Docker Engine or Docker Desktop with Docker Compose.
- Go 1.24+ to install Devo from source.

Application-specific runtime requirements depend on the application being developed.

### Installation

From the Devo source repository:

```sh
go install ./cmd/devo
```

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
devo dev    # Start configured services and attach logs
devo logs   # Stream infrastructure and application logs
devo down   # Stop and remove local containers
```

## Architecture

Devo separates infrastructure definitions from the applications that consume them.

### Reusable infrastructure modules

Each module packages the configuration and provisioning artifacts required to run a supported service locally. Modules provide a consistent interface for selecting and operating infrastructure without requiring every application team to maintain its own implementation.

### Application-specific environments

Each generated project receives its own environment configuration and the artifacts required by its selected modules.

Depending on the selected services, these artifacts can include:

- `devo.yaml` for environment and module configuration.
- `docker-compose.yml` for local service orchestration.
- Database initialization scripts.
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
