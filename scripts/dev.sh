#!/bin/sh
# Runs the app for development with reloading on file changes. The Next.js
# dev server hot reloads the frontend and proxies /api to the Go server,
# which is rebuilt and restarted whenever a backend file changes.
set -eu
repository_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$repository_dir"

api_address=${APP_LISTEN_ADDR:-127.0.0.1:8080}
web_port=${WEB_PORT:-3000}
binary=.build/server-dev
stamp=.build/dev-stamp
mkdir -p .build
touch "$stamp"

# Each background job gets its own process group, so stopping a job also
# stops the processes it started, such as node under pnpm.
set -m
server_pid=
web_pid=

stop_server() {
  if [ -n "$server_pid" ]; then
    kill -TERM -- "-$server_pid" 2>/dev/null || true
    wait "$server_pid" 2>/dev/null || true
    server_pid=
  fi
}

start_server() {
  APP_LISTEN_ADDR=$api_address "./$binary" &
  server_pid=$!
}

cleanup() {
  trap - EXIT INT TERM
  stop_server
  if [ -n "$web_pid" ]; then
    kill -TERM -- "-$web_pid" 2>/dev/null || true
    wait "$web_pid" 2>/dev/null || true
  fi
}
trap cleanup EXIT
trap 'exit 130' INT TERM

# The stamp is touched after sqlc writes its code, so generated files do not
# count as a change, and before compiling, so a save during the build does.
build() {
  (cd backend && sqlc generate) || return 1
  touch "$stamp"
  ./scripts/go.sh build -o "../$binary" ./cmd/server
}

# A directory changes when a file in it is added, removed, or renamed.
backend_changed() {
  [ -n "$(find backend \( -type d -o -name '*.go' -o -name '*.sql' -o -name 'go.mod' -o -name 'go.sum' -o -name 'sqlc.yaml' \) \
    -newer "$stamp" -print | head -n 1)" ]
}

if build; then
  start_server
else
  echo "dev: the Go build failed; save a backend file to try again" >&2
fi

(cd web && APP_API_URL="http://$api_address" exec pnpm exec next dev --port "$web_port") &
web_pid=$!
echo "dev: open http://localhost:$web_port"

while kill -0 "$web_pid" 2>/dev/null; do
  sleep 1
  if backend_changed; then
    echo "dev: backend changed, rebuilding the Go server"
    if build; then
      stop_server
      start_server
    else
      echo "dev: the Go build failed; the previous server keeps running" >&2
    fi
  fi
done
echo "dev: the Next.js dev server stopped" >&2
exit 1
