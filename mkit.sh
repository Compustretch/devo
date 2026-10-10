#!/usr/bin/env bash
set -euo pipefail

# Create directory structure
mkdir -p internal/project/embedded_templates/rust/.github/workflows
mkdir -p internal/project/embedded_templates/rust/src

# Create files
touch internal/project/generate.go
touch internal/project/generate_test.go
touch internal/project/embedded_templates/rust/.gitignore
touch internal/project/embedded_templates/rust/.github/workflows/ci.yml
touch internal/project/embedded_templates/rust/Cargo.toml.tmpl
touch internal/project/embedded_templates/rust/Dockerfile
touch internal/project/embedded_templates/rust/README.md.tmpl
touch internal/project/embedded_templates/rust/devo.yaml.tmpl
touch internal/project/embedded_templates/rust/src/main.rs

echo "Directory structure created successfully."
