#!/usr/bin/env bash
#
# Verify a velabase build: vet, build, run the plugins' own test suites against
# the pinned PocketBase, then boot the binary and smoke test it.
#
# Used by scripts/sync-upstream.sh (before tagging) and by CI.
#
# Usage: bash scripts/check.sh

set -euo pipefail

export GOWORK=off

PLUGINS=(
    github.com/velastack/pocketbase-openworkflow
    github.com/velastack/pocketbase-whatsapp
)

die() { echo "error: $*" >&2; exit 1; }

cd "$(git rev-parse --show-toplevel)"

tmp="$(mktemp -d)"
pid=""
cleanup() {
    [ -n "$pid" ] && kill "$pid" 2>/dev/null || true
    rm -rf "$tmp"
}
trap cleanup EXIT

echo "==> go vet + build"
go vet ./...

# build with the same -X flag as the release (.goreleaser.yaml): the linker
# silently ignores -X for a symbol that doesn't exist, so a stale module path
# there would ship binaries without a version and break `pocketbase update`
version_sym="$(grep -o -- '-X [^ ]*\.Version=' .goreleaser.yaml | sed -e 's/^-X //' -e 's/=$//')"
[ -n "$version_sym" ] || die "no -X <pkg>.Version= ldflag found in .goreleaser.yaml"
go build -ldflags "-X $version_sym=0.0.0-check" -o "$tmp/pocketbase" .
"$tmp/pocketbase" --version | grep -q '0\.0\.0-check' || \
    die "the .goreleaser.yaml version ldflag ($version_sym) does not set the binary version"

# `pocketbase update` must pull velabase releases, never upstream's
grep -q 'plugins/update"' main.go && \
    die "main.go registers upstream's update plugin (it would self-update to plain PocketBase)"
grep -A3 'ghupdate.MustRegister' main.go | tr -d '[:space:]' | grep -q 'Owner:"velastack",Repo:"velabase"' || \
    die "main.go must register ghupdate with Owner velastack / Repo velabase"

# go test on the plugin packages runs them with this module's build list,
# i.e. against the PocketBase version pinned here (not the one in their go.mod)
echo "==> plugin tests against $(go list -m -f '{{.Path}} {{.Version}}' ganigeorgiev.com/pocketbase/pocketbase)"
go test "${PLUGINS[@]/%//...}"

echo "==> smoke test"
port="${SMOKE_PORT:-8097}"
base="http://127.0.0.1:$port"
"$tmp/pocketbase" serve --dir "$tmp/pb_data" --http "127.0.0.1:$port" > "$tmp/serve.log" 2>&1 &
pid=$!

for _ in $(seq 1 30); do
    curl -sf "$base/api/health" > /dev/null && break
    sleep 0.5
done
curl -sf "$base/api/health" > /dev/null || { cat "$tmp/serve.log" >&2; die "server did not become healthy"; }

ext="$(curl -sf "$base/_/extensions.js")"
grep -q '#/workflows' <<< "$ext" || die "openworkflow UI extension missing from /_/extensions.js"
grep -q 'settings/whatsapp' <<< "$ext" || die "whatsapp UI extension missing from /_/extensions.js"
grep -q '#/whatsapp"' <<< "$ext" || die "whatsapp Inbox page missing from /_/extensions.js"

code="$(curl -s -o /dev/null -w '%{http_code}' "$base/api/whatsapp/inbox")"
[ "$code" = "401" ] || die "expected 401 from the unauthenticated WhatsApp inbox API, got $code"

code="$(curl -s -o /dev/null -w '%{http_code}' -X POST -d '{}' "$base/api/whatsapp/webhook")"
[ "$code" = "401" ] || die "expected 401 from an unsigned WhatsApp webhook, got $code"

code="$(curl -s -o /dev/null -w '%{http_code}' "$base/api/ow/v1/default/runs")"
[ "$code" = "401" ] || die "expected 401 from the unauthenticated OpenWorkflow API, got $code"

curl -sf "$base/api/collections/users/auth-methods" | grep -q '"whatsapp"' || \
    die "whatsapp missing from the users auth-methods"

echo "==> ok"
