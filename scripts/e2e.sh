#!/usr/bin/env bash
# End-to-end check of devo, built from this repo's source (not whatever `devo`
# is on your PATH):
#
#   build devo -> scaffold a fresh Rust app -> add ClickHouse -> start the
#   stack -> run the generated smoke test -> stop the stack
#
# Needs Go, Rust (cargo) and a running Docker. Everything happens in a temp
# directory that is removed afterwards. ClickHouse publishes port 8123, so stop
# any other devo stack that uses ClickHouse first (`devo down` in that project).
set -euo pipefail

repo="$(cd "$(dirname "$0")/.." && pwd)"
work="$(mktemp -d)"
devo="$work/devo"
app="$work/e2eapp"

cleanup() {
  if [ -d "$app" ]; then (cd "$app" && "$devo" down >/dev/null 2>&1) || true; fi
  rm -rf "$work"
}
trap cleanup EXIT

step() { printf '\n==> %s\n' "$*"; }
fail() { printf '\nFAIL: %s\n' "$*" >&2; exit 1; }

tpl="$repo/internal/project/embedded_templates/rust"
if [ ! -s "$tpl/Cargo.toml.tmpl" ] && [ ! -s "$tpl/Cargo.toml" ]; then
  echo "template files in $tpl:" >&2
  (cd "$tpl" 2>/dev/null && find . -type f -exec ls -l {} + >&2) || echo "  (directory not found)" >&2
  fail "the repo's Rust template has no Cargo.toml content, so every new project will be empty. Run scripts/install-rust-template.sh"
fi

step "building devo from $repo"
(cd "$repo" && go build -o "$devo" ./cmd/devo)

step "devo init rust e2eapp"
(cd "$work" && "$devo" init rust e2eapp)

# The scaffold must be a real project, not a set of empty files.
bad=0
for f in Cargo.toml src/main.rs devo.yaml compose.yaml; do
  if [ ! -e "$app/$f" ]; then echo "scaffold is MISSING $f" >&2; bad=1
  elif [ ! -s "$app/$f" ]; then echo "scaffold has an EMPTY $f" >&2; bad=1
  fi
done
if [ "$bad" = 1 ]; then
  echo "--- generated project:" >&2;       (cd "$app" && find . -type f -exec ls -l {} + >&2)
  echo "--- repo template ($tpl):" >&2;    (cd "$tpl" && find . -type f -exec ls -l {} + >&2)
  fail "scaffold is incomplete (see above)"
fi

step "devo add clickhouse"
(cd "$app" && "$devo" add clickhouse)
[ -s "$app/tests/clickhouse_smoke.rs" ] || fail "no smoke test was generated for clickhouse"

step "devo dev"
(cd "$app" && "$devo" dev)

step "cargo test"
(cd "$app" && cargo test -- --nocapture)

printf '\nPASS: init, add, dev and the ClickHouse smoke test all worked.\n'