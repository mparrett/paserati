#!/usr/bin/env bash
# Compare two macro-test262 record sets (same-runner A/B) → markdown delta table.
#
# Usage: scripts/macro-test262-compare.sh <base.jsonl> <head.jsonl>
#
# Δ% is on raw summed execution time (ns_per_op). Both halves ran on one runner,
# so the raw delta is the honest signal — no anchor normalization needed.
set -euo pipefail

base="${1:?usage: macro-test262-compare.sh <base.jsonl> <head.jsonl>}"
head="${2:?usage: macro-test262-compare.sh <base.jsonl> <head.jsonl>}"

jq -rn \
  --argjson b "$(jq -s '.' "$base")" \
  --argjson h "$(jq -s '.' "$head")" '
  ($b | map({(.name): .}) | add) as $bb |
  ($h | map({(.name): .}) | add) as $hh |
  ([ ($b[].name), ($h[].name) ] | unique) as $names |
  def ms($e): if $e then (($e.ns_per_op / 1e6) * 10 | round / 10 | tostring) else "—" end;
  "| Series | Base (ms) | Head (ms) | Δ% |",
  "|---|---:|---:|---:|",
  ( $names[] |
    $bb[.] as $be | $hh[.] as $he |
    ( if ($be and $he) then (($he.ns_per_op - $be.ns_per_op) / $be.ns_per_op * 100) else null end ) as $d |
    "| `test262.\(.)` | \(ms($be)) | \(ms($he)) | \(if $d == null then "—" elif $d >= 0 then "+\($d * 100 | round / 100)" else "\($d * 100 | round / 100)" end) |"
  )
'
