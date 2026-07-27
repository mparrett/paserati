#!/usr/bin/env bash
# Report other perf-data writer runs that are queued behind this one.
#
# WHY THIS EXISTS. The timeline and Test262 backfill workflows share the
# `perf-timeline-main` concurrency group with cancel-in-progress: false, which
# correctly protects the ACTIVE writer. It does not queue the rest: GitHub keeps
# at most one PENDING run per group, so a newly queued run evicts the previously
# pending one. Dispatching a batch of N targets therefore lands one snapshot and
# silently drops N-2 — and "silently" is the whole problem. The evicted runs
# report as cancelled, in a list nobody is watching, hours later.
#
# This cannot PREVENT the eviction from inside a workflow: by the time this job
# is executing, the group has already let it through, and any sibling that was
# going to be displaced already has been. Prevention is the dispatcher's job
# (dispatch one, wait, verify, next — see the driver on tools/perf-runbook).
# What this can do is make the loss visible at the moment it happens, in the log
# of the run that survived, naming the targets that need re-dispatching.
#
# Deliberately does not fail the job. This run's snapshot is legitimate and
# publishing it is strictly better than not; failing here would throw away good
# work to protest someone else's mistake.
#
# Usage: perf-writer-queue-check.sh <owner/repo>
#   needs GH_TOKEN in the environment (the workflow's github.token is enough).
set -euo pipefail

repo="${1:?usage: perf-writer-queue-check.sh <owner/repo>}"
self="${GITHUB_RUN_ID:-}"

# Both writers, by workflow file — names drift, paths don't.
siblings="$(gh api "/repos/${repo}/actions/runs?status=queued&per_page=100" \
  --jq '[.workflow_runs[]
         | select(.path == ".github/workflows/perf-timeline.yml"
               or .path == ".github/workflows/perf-test262-backfill.yml")
         | {id, sha: .head_sha[0:12], event, url: .html_url}]' 2>/dev/null || echo '[]')"

# Our own run can appear as queued in the window between dispatch and execution.
n="$(printf '%s' "$siblings" | jq --arg self "$self" '[.[] | select(.id != ($self | tonumber? // -1))] | length')"

if [ "${n:-0}" -eq 0 ]; then
  echo "writer queue: clear (no other perf-data writer runs pending)"
  exit 0
fi

{
  echo "### ⚠️ ${n} other perf-data writer run(s) pending"
  echo
  echo "GitHub keeps only ONE pending run per concurrency group, so all but the"
  echo "last of these will be evicted rather than queued — their snapshots will"
  echo "not be taken, and they will report as cancelled rather than failed."
  echo
  echo "| Run | Target | Event |"
  echo "|---|---|---|"
  printf '%s' "$siblings" | jq -r --arg self "$self" \
    '.[] | select(.id != ($self | tonumber? // -1))
     | "| [\(.id)](\(.url)) | `\(.sha)` | \(.event) |"'
  echo
  echo "Re-dispatch the affected targets one at a time and wait for each to finish."
} | tee -a "${GITHUB_STEP_SUMMARY:-/dev/null}"

echo "::warning::${n} other perf-data writer run(s) are pending; all but one will be evicted, not queued"
