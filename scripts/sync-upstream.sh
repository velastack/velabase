#!/usr/bin/env bash
#
# Sync velabase with the latest upstream PocketBase release.
#
# velabase versions match PocketBase: velabase vX.Y.Z == PocketBase vX.Y.Z
# + the velastack plugins. When a new upstream release is out this:
#
#   1. bumps github.com/pocketbase/pocketbase to it, and the plugins to their
#      latest release;
#   2. 3-way merges upstream's examples/base/main.go changes (old -> new
#      version) into our main.go;
#   3. runs scripts/check.sh;
#   4. commits and tags vX.Y.Z (does NOT push).
#
# It also tags the pinned version if it was never released (eg. a previous
# run failed before the release), so re-running is always safe. Nothing to do
# when the pinned version is the latest upstream release and is already tagged.
#
# Usage:
#   bash scripts/sync-upstream.sh
#   UPSTREAM_VERSION=vX.Y.Z bash scripts/sync-upstream.sh   # pin the target
#
# In GitHub Actions the new tag is written to $GITHUB_OUTPUT as `tag=vX.Y.Z`
# (empty when there is nothing to release).

set -euo pipefail

export GOWORK=off

PB="github.com/pocketbase/pocketbase"
PLUGINS=(
    github.com/velastack/pocketbase-openworkflow
    github.com/velastack/pocketbase-whatsapp
)

die() { echo "error: $*" >&2; exit 1; }

output() {
    [ -n "${GITHUB_OUTPUT:-}" ] && echo "$1" >> "$GITHUB_OUTPUT"
    return 0
}

cd "$(git rev-parse --show-toplevel)"

[ -z "$(git status --porcelain --untracked-files=no)" ] || \
    die "working tree has uncommitted changes. Commit or stash first."

current="$(go list -m -f '{{.Version}}' "$PB")"
latest="${UPSTREAM_VERSION:-$(go list -m -f '{{.Version}}' "$PB@latest")}"

echo "    pinned : $current"
echo "    latest : $latest"

if [ "$current" != "$latest" ]; then
    newest="$(printf '%s\n%s\n' "${current#v}" "${latest#v}" | sort -V | tail -n1)"
    [ "$newest" = "${latest#v}" ] || die "$latest is older than the pinned $current"

    tmp="$(mktemp -d)"
    trap 'rm -rf "$tmp"' EXIT

    cp "$(go list -m -f '{{.Dir}}' "$PB")/examples/base/main.go" "$tmp/main.go.old"

    echo "==> go get $PB@$latest + plugins@latest"
    go get "$PB@$latest" "${PLUGINS[@]/%/@latest}"
    go mod tidy

    # a plugin requiring a newer PocketBase would silently raise it (MVS)
    resolved="$(go list -m -f '{{.Version}}' "$PB")"
    [ "$resolved" = "$latest" ] || \
        die "the plugins pulled $PB to $resolved, expected $latest"

    # main.go is upstream's examples/base/main.go + the velabase changes,
    # so carry over whatever upstream changed between the two versions
    echo "==> merging upstream examples/base/main.go changes"
    git merge-file -L main.go -L "upstream $current" -L "upstream $latest" \
        main.go "$tmp/main.go.old" "$(go list -m -f '{{.Dir}}' "$PB")/examples/base/main.go" || \
        die "conflict merging upstream examples/base/main.go into main.go (resolve, commit, re-run)"
    gofmt -l main.go | grep -q . && die "merged main.go is not gofmt-ed"

    bash scripts/check.sh

    git add go.mod go.sum main.go
    git commit -q -m "pocketbase $latest" \
        -m "$(go list -m -f '{{.Path}} {{.Version}}' "${PLUGINS[@]}")"
    echo "==> committed $(git rev-parse --short HEAD) pocketbase $latest"
elif git rev-parse -q --verify "refs/tags/$latest" > /dev/null; then
    echo "==> $latest is already released, nothing to do"
    output "tag="
    exit 0
else
    bash scripts/check.sh
fi

git rev-parse -q --verify "refs/tags/$latest" > /dev/null && \
    die "tag $latest already exists on another commit"
git tag "$latest"
echo "==> tagged $latest (push master + the tag to release)"
output "tag=$latest"
