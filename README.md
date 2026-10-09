# Devo

Devo generates self-contained application development environments.

## Requirements

- Go 1.24+
- Docker Engine or Docker Desktop with Docker Compose

## Install the CLI

```sh
go install ./cmd/devo
```

## Create an application

```sh
devo init orders-api
cd orders-api
devo services
devo dev
```

By default, Devo provisions ClickHouse and Grafana.

Open Grafana at http://localhost:3000 (admin/admin).
ClickHouse HTTP is available at http://localhost:8123.

## Select services

```sh
devo init orders-api --with clickhouse
devo init orders-api --with grafana
devo init orders-api --without grafana
```

## Manage services

```sh
devo add grafana
devo remove grafana
devo services
devo dev
devo logs
devo down
```

Each generated application contains its own devo.yaml, Compose file,
ClickHouse initialization SQL, and Grafana provisioning. The generated
project does not depend on the Devo source repository at runtime.
