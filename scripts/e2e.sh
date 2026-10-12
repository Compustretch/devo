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
# Under $HOME, not the system temp dir: Colima only shares your home directory
# with containers, so bind mounts from /var/folders/... would arrive empty.
work="$(mktemp -d "$HOME/.devo-e2e.XXXXXX")"
devo="$work/devo"
app="$work/e2eapp"

cleanup() {
  if [ -d "$app" ]; then (cd "$app" && "$devo" down >/dev/null 2>&1) || true; fi
  remove_e2e_volumes
  rm -rf "$work"
}
trap cleanup EXIT

step() { printf '\n==> %s\n' "$*"; }
fail() { printf '\nFAIL: %s\n' "$*" >&2; exit 1; }

# Volumes left by earlier e2e runs (compose names them <project>_<volume>).
remove_e2e_volumes() {
  docker volume ls -q --filter "name=e2eapp_" 2>/dev/null |
    while read -r v; do docker volume rm "$v" >/dev/null 2>&1 || true; done
}

# free_port <port>: if a Docker container already publishes the port, say which
# one (and which devo project it belongs to) and offer to stop it.
free_port() {
  local port=$1 holder dir answer
  holder="$(docker ps --filter "publish=$port" --format '{{.Names}}' | head -n 1)"
  [ -n "$holder" ] || return 0

  dir="$(docker inspect -f '{{index .Config.Labels "com.docker.compose.project.working_dir"}}' "$holder" 2>/dev/null || true)"
  echo "port $port is already in use by container '$holder'${dir:+ (devo project: $dir)}" >&2

  if [ -t 0 ]; then
    read -r -p "Stop that container so the test can run? [y/N] " answer
    if [ "$answer" = y ] || [ "$answer" = Y ]; then
      docker stop "$holder" >/dev/null
      echo "stopped $holder${dir:+; bring it back later with 'devo dev' in $dir}"
      return 0
    fi
  fi
  fail "port $port is taken by '$holder'. Stop it (${dir:+cd $dir && devo down, or }docker stop $holder) and re-run."
}

tpl="$repo/internal/project/embedded_templates/rust"
if [ ! -s "$tpl/Cargo.toml.tmpl" ] && [ ! -s "$tpl/Cargo.toml" ]; then
  echo "template files in $tpl:" >&2
  (cd "$tpl" 2>/dev/null && find . -type f -exec ls -l {} + >&2) || echo "  (directory not found)" >&2
  fail "the repo's Rust template has no Cargo.toml content, so every new project will be empty. Run: bash scripts/install-rust-template.sh"
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

step "checking ports (ClickHouse publishes 8123 and 9000)"
for port in 8123 9000; do free_port "$port"; done
remove_e2e_volumes

step "devo dev"
(cd "$app" && "$devo" dev)

step "cargo test"
if ! (cd "$app" && cargo test -- --nocapture); then
  echo >&2
  echo "--- container logs (last 40 lines each):" >&2
  docker ps -a --filter "label=com.docker.compose.project=e2eapp" --format '{{.Names}}' |
    while read -r c; do
      echo "### $c" >&2
      docker logs --tail 40 "$c" >&2 2>&1 || true
    done
  fail "cargo test failed (output above)"
fi

printf '\nPASS: init, add, dev and the ClickHouse smoke test all worked.\n'