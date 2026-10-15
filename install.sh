#!/usr/bin/env bash
# Install (or update) the Temporal Engineering Manager in a Crafting org: the
# temporal-em template, the pinned temporal-em sandbox, and the
# temporal-engineering-manager agent.
#
# Usage: ./install.sh [--shared] [--org ORG] [--folder FOLDER]
#   --shared   install the agent as org-shared (default: personal)
#   --org      Crafting org name (default: the cs CLI default org)
#   --folder   Crafting folder (default: the cs CLI default folder)
set -euo pipefail

cd "$(dirname "$0")"

SHARED=()
CS_ARGS=()
while [[ $# -gt 0 ]]; do
  case "$1" in
    --shared) SHARED=(--shared) ;;
    --org) CS_ARGS+=(-O "$2"); shift ;;
    --folder) CS_ARGS+=(--folder "$2"); shift ;;
    -h|--help) sed -n '2,9p' "$0"; exit 0 ;;
    *) echo "unknown argument: $1" >&2; exit 1 ;;
  esac
  shift
done

command -v cs >/dev/null || { echo "the Crafting CLI (cs) is required" >&2; exit 1; }

# bash 3.2 (macOS) treats an empty array as unbound under set -u.
csx() { cs ${CS_ARGS[@]+"${CS_ARGS[@]}"} "$@"; }
shared() { echo ${SHARED[@]+"${SHARED[@]}"}; }

NAME=temporal-em
AGENT=temporal-engineering-manager

for agent in sample-software-engineer sample-qa-engineer; do
  csx llm agent show "$agent" --shared >/dev/null 2>&1 || csx llm agent show "$agent" >/dev/null 2>&1 \
    || echo "warning: agent $agent not found; every delivery runs it" >&2
done
csx template show sample-boutique >/dev/null 2>&1 \
  || echo "warning: template sample-boutique not found; it is the default delivery target" >&2

if csx template show "$NAME" >/dev/null 2>&1; then
  csx template update "$NAME" "templates/$NAME.yaml"
else
  csx template create "$NAME" "templates/$NAME.yaml" \
    --description "Temporal server and DeliverChange worker for the $AGENT agent"
fi

state() { csx sandbox list 2>/dev/null | awk -v n="$NAME" '$1 == n { print $2 }'; }
origin() { csx sandbox list 2>/dev/null | awk -v n="$NAME" '$1 == n { print ($3 == "Yes" ? $4 : $3) }'; }

if csx sandbox show "$NAME" >/dev/null 2>&1; then
  if [[ "$(origin)" != "$NAME" ]]; then
    echo "sandbox $NAME exists but was not created from the $NAME template; remove or rename it, then re-run" >&2
    exit 1
  fi
  [[ "$(state)" == Suspended ]] && csx sandbox resume "$NAME"
  # A previous run may have timed out while the sandbox was still being created.
  for _ in $(seq 120); do [[ "$(state)" == Ready ]] && break; sleep 10; done
  [[ "$(state)" == Ready ]] || { echo "sandbox $NAME is not ready: $(state)" >&2; exit 1; }
  echo "sandbox $NAME exists; updating its code and restarting the worker"
  csx exec -W "$NAME/dev" -u 1000 -- bash -lc \
    'cd ~/multiagent-temporal && git pull --ff-only && orchestrator/bin/setup'
  csx restart worker -W "$NAME/dev"
else
  csx sandbox create "$NAME" -t "$NAME" --wait-timeout 20m
fi
# Pinning without expiry needs an org admin; fall back to the default expiry.
csx sandbox pin "$NAME" --expiry never >/dev/null 2>&1 \
  || csx sandbox pin "$NAME" >/dev/null 2>&1 \
  || echo "note: could not pin $NAME; a running delivery still keeps it awake" >&2

# shellcheck disable=SC2046
if csx llm agent show "$AGENT" $(shared) >/dev/null 2>&1; then
  csx llm agent update "$AGENT" "agents/$AGENT.yaml" $(shared)
else
  csx llm agent create "$AGENT" "agents/$AGENT.yaml" $(shared)
fi

scope=personal
[[ ${#SHARED[@]} -gt 0 ]] && scope=org-shared
echo
echo "Installed the $NAME template, the pinned $NAME sandbox, and the $AGENT agent ($scope)."
echo "Temporal UI: the temporal-ui endpoint of $NAME (cs sandbox show $NAME)."
