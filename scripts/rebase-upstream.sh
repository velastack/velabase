#!/usr/bin/env bash
#
# Track upstream PocketBase: refresh the PR branch and rebuild the release branch.
#
# Two-branch model
# ----------------
#   upstream  -> pocketbase/pocketbase   (fetched --no-tags)
#   origin    -> velastack/velabase
#
#   feat/openworkflow  SOURCE OF TRUTH for the OpenWorkflow feature, and the
#                      branch PR'd against upstream. Tracks upstream **master
#                      (HEAD)**. Source-only: NO ui/dist (it inherits upstream's
#                      committed dist from the base, so it still compiles; the
#                      PR just notes "run npm build to regenerate the admin UI").
#                      Kept clean for review. Force-push only when you actually
#                      want to update the open PR.
#
#   master             RELEASE branch. Tracks the latest upstream **release tag**
#                      (vX.Y.Z) so a velabase release == that PocketBase release
#                      + OpenWorkflow, exactly. Rebuilt each run as:
#                          tag  +  OW source (taken from feat/openworkflow)
#                               +  fork overlay (one commit)
#                               +  rebuilt ui/dist
#                      History here is disposable; it is regenerated every run.
#
# The "fork overlay" is the velabase-only delta that must NOT go upstream:
# the ghupdate self-update target (examples/base/main.go), the release-workflow
# permissions block, and this script. It is stored as ONE commit on master whose
# subject starts with the marker below, so later runs find it by marker; the
# first ("bootstrap") run derives it from the feat/openworkflow<->master tree
# diff (valid while they still share a base and identical OW source).
#
# Because OW lives on feat/openworkflow (HEAD) and master is built on the tag,
# the OW commits are replayed HEAD->tag with `git am --3way`; an upstream change
# to a file OW touches (mainly examples/base/main.go) between tag and HEAD can
# conflict -- the script stops with resolve instructions when it does.
#
# Usage:
#   bash scripts/rebase-upstream.sh            # do it
#   DRY_RUN=1 bash scripts/rebase-upstream.sh  # build + test on temp branches,
#                                              # then discard; never moves refs
#   UPSTREAM_TAG=vX.Y.Z bash scripts/rebase-upstream.sh   # pin the release base
#                                              # instead of the newest tag, for
#                                              # catching up on a release a run
#                                              # skipped (upstream cut two tags
#                                              # between runs). Publish the
#                                              # pinned one, then re-run unpinned.
#
# After a real run:
#   git push origin master --force-with-lease
#   git tag vX.Y.Z && git push origin vX.Y.Z          # drafts the release
#   git push origin feat/openworkflow --force-with-lease   # only when refreshing the PR

set -euo pipefail

# Re-exec from a private copy so the script can freely rewrite scripts/ (incl.
# itself) while running -- checking out the tag deletes scripts/, the overlay
# recreates it, and an in-place file swap under a running shell is unsafe.
if [ "${VELABASE_REEXEC:-}" != "1" ]; then
    _self="$(mktemp -t velabase-rebase.XXXXXX)"
    cat "$0" > "$_self"
    VELABASE_REEXEC=1 exec bash "$_self" "$@"
fi

UPSTREAM_REMOTE="${UPSTREAM_REMOTE:-upstream}"
ORIGIN_REMOTE="${ORIGIN_REMOTE:-origin}"
PR_BRANCH="${PR_BRANCH:-feat/openworkflow}"
REL_BRANCH="${REL_BRANCH:-master}"
OVERLAY_MARKER="velabase-fork-overlay"

die() { echo "error: $*" >&2; exit 1; }

cd "$(git rev-parse --show-toplevel)"

[ -n "$(git status --porcelain --untracked-files=no)" ] && \
    die "working tree has uncommitted (tracked) changes. Commit or stash first."

echo "==> Fetching $UPSTREAM_REMOTE (branches only)"
git fetch "$UPSTREAM_REMOTE" --no-tags --prune
UP="$(git rev-parse "$UPSTREAM_REMOTE/master")"

latest_tag="${UPSTREAM_TAG:-}"
if [ -n "$latest_tag" ]; then
    echo "    UPSTREAM_TAG pinned -> $latest_tag (not necessarily the newest tag)"
else
    latest_tag="$(git ls-remote --tags --refs "$UPSTREAM_REMOTE" 'v[0-9]*' \
        | awk '{sub("refs/tags/","",$2); print $2}' \
        | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | sort -V | tail -n1)"
    [ -n "$latest_tag" ] || die "no vX.Y.Z tags on $UPSTREAM_REMOTE"
fi
TAG_SHA="$(git ls-remote "$UPSTREAM_REMOTE" "refs/tags/${latest_tag}^{}" | awk '{print $1}')"
[ -n "$TAG_SHA" ] || TAG_SHA="$(git ls-remote "$UPSTREAM_REMOTE" "refs/tags/$latest_tag" | awk '{print $1}')"
[ -n "$TAG_SHA" ] || die "cannot resolve $latest_tag"
git cat-file -e "$TAG_SHA" 2>/dev/null || git fetch "$UPSTREAM_REMOTE" "$TAG_SHA" 2>/dev/null || true
git cat-file -e "$TAG_SHA" 2>/dev/null || die "tag commit $TAG_SHA ($latest_tag) not reachable"

echo "    upstream HEAD : $(git rev-parse --short "$UP")  -> $PR_BRANCH base"
echo "    latest tag    : $latest_tag $(git rev-parse --short "$TAG_SHA")  -> $REL_BRANCH base"

old_pr="$(git rev-parse "$PR_BRANCH")"
old_rel="$(git rev-parse "$REL_BRANCH")"

### Phase A: capture the fork overlay from the current release branch ###
echo "==> Capturing fork overlay"
overlay_diff="$(git rev-parse --git-path velabase-overlay.diff)"
overlay_sha="$(git log -1 --format=%H --grep="^${OVERLAY_MARKER}" "$REL_BRANCH" || true)"
if [ -n "$overlay_sha" ]; then
    echo "    from marked commit $(git rev-parse --short "$overlay_sha")"
    git diff "${overlay_sha}~1" "$overlay_sha" -- . ':(exclude)ui/dist' > "$overlay_diff"
else
    echo "    bootstrap: $PR_BRANCH vs $REL_BRANCH tree diff (shared base, identical OW)"
    git diff "$PR_BRANCH" "$REL_BRANCH" -- . ':(exclude)ui/dist' > "$overlay_diff"
fi
[ -s "$overlay_diff" ] || die "fork overlay is empty -- nothing to carry onto $REL_BRANCH"

### Phase B: refresh feat/openworkflow onto upstream HEAD (source-only) ###
echo "==> Refreshing $PR_BRANCH onto upstream HEAD (source-only)"
pr_tip="$PR_BRANCH"
if [ "$(git merge-base "$PR_BRANCH" "$UP")" = "$UP" ]; then
    echo "    already current with HEAD"
else
    pr_mbox="$(git rev-parse --git-path velabase-pr.mbox)"
    git format-patch "$(git merge-base "$PR_BRANCH" "$UP")..$PR_BRANCH" \
        --stdout -- . ':(exclude)ui/dist' > "$pr_mbox"
    git branch -f _vb_pr "$UP"
    git checkout -q _vb_pr
    if ! git am --3way --empty=drop "$pr_mbox"; then
        echo "conflict rebasing $PR_BRANCH onto HEAD; resolve + 'git am --continue', then re-run" >&2
        exit 2
    fi
    rm -f "$pr_mbox"
    pr_tip="_vb_pr"
fi

### Phase C: rebuild master onto the latest tag ###
echo "==> Rebuilding $REL_BRANCH onto $latest_tag"
git branch -f _vb_rel "$TAG_SHA"
git checkout -q _vb_rel

# 1) OW source (authored on HEAD) replayed onto the tag
ow_mbox="$(git rev-parse --git-path velabase-ow.mbox)"
git format-patch "$UP..$pr_tip" --stdout -- . ':(exclude)ui/dist' > "$ow_mbox"
if ! git am --3way --empty=drop "$ow_mbox"; then
    echo "conflict applying OW source onto $latest_tag; resolve + 'git am --continue', then re-run" >&2
    exit 2
fi
rm -f "$ow_mbox"

# 2) fork overlay as a single marked commit
git apply --3way --index "$overlay_diff" || \
    die "fork overlay did not apply onto $latest_tag+OW (resolve, commit, re-run)"
git commit -q -m "$OVERLAY_MARKER: ghupdate self-update target, release perms, tracking script"
rm -f "$overlay_diff"

# 3) regenerate generated artifacts
echo "==> Rebuilding ui/dist + go mod tidy"
npm --prefix ./ui ci
npm --prefix ./ui run build
go mod tidy
git add -- ui/dist go.mod go.sum
git diff --cached --quiet || git commit -q -m "build: regenerate ui/dist for $latest_tag"

# 4) tests (skip the macOS-only fsnotify watcher tests; CI runs the full suite on Linux)
echo "==> Building + testing"
go build ./examples/base && rm -f base
go test -skip 'TestNotifyWatcher' ./...

### Phase D: finalize ###
if [ "${DRY_RUN:-}" = "1" ]; then
    echo
    echo "==> DRY RUN ok -- not moving refs."
    echo "    would set $REL_BRANCH -> $(git rev-parse --short _vb_rel)  ($latest_tag + OW + overlay + dist)"
    echo "    would set $PR_BRANCH  -> $(git rev-parse --short "$pr_tip")  (HEAD + OW, source-only)"
    git checkout -q "$REL_BRANCH"
    git branch -D _vb_rel >/dev/null 2>&1 || true
    [ "$pr_tip" = "_vb_pr" ] && git branch -D _vb_pr >/dev/null 2>&1 || true
    exit 0
fi

rel_bak="backup/${REL_BRANCH//\//-}-pre-$(git rev-parse --short "$old_rel")"
pr_bak="backup/${PR_BRANCH//\//-}-pre-$(git rev-parse --short "$old_pr")"
git branch -f "$rel_bak" "$old_rel"
git branch -f "$pr_bak" "$old_pr"
git branch -f "$REL_BRANCH" _vb_rel
git branch -f "$PR_BRANCH" "$pr_tip"
git checkout -q "$REL_BRANCH"
git branch -D _vb_rel >/dev/null 2>&1 || true
[ "$pr_tip" = "_vb_pr" ] && git branch -D _vb_pr >/dev/null 2>&1 || true

cat <<EOF

==> Done.
    $PR_BRANCH  -> upstream HEAD + OW (source-only)  $(git rev-parse --short "$PR_BRANCH")   [backup: $pr_bak]
    $REL_BRANCH -> $latest_tag + OW + overlay + dist  $(git rev-parse --short "$REL_BRANCH")   [backup: $rel_bak]

Publish the release:
    git range-diff $rel_bak $REL_BRANCH      # confirm only OW/overlay differ from $latest_tag
    git push $ORIGIN_REMOTE $REL_BRANCH --force-with-lease
    git tag $latest_tag && git push $ORIGIN_REMOTE $latest_tag   # drafts the release
    # then publish the draft + set VELABASE_VERSION=$latest_tag in the OW backend CI

Refresh the upstream PR (only when you're ready):
    git push $ORIGIN_REMOTE $PR_BRANCH --force-with-lease

Recover:  git reset --hard $rel_bak   (and/or  git branch -f $PR_BRANCH $pr_bak)
EOF
