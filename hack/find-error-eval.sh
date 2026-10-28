#!/usr/bin/env bash
# Runs Find error on the five broken services in test/eval/failures.yaml and
# prints each answer next to the real cause, for a person to grade. Phase 5
# is done when at least four name the real cause.
#
#   kubectl apply -f test/eval/failures.yaml   # then wait a minute or two
#   clusterview --ai-provider=anthropic &       # or openai, with a key
#   hack/find-error-eval.sh [http://127.0.0.1:8080]
set -euo pipefail

url=${1:-http://127.0.0.1:8080}
for name in orders-api report-builder storefront checkout search-api; do
  want=$(kubectl -n cv-eval get deployment "$name" -o jsonpath='{.metadata.annotations.clusterview\.io/eval-cause}')
  echo "━━ $name"
  echo "Real cause: $want"
  curl -sS -X POST "$url/api/v1/diagnose" \
    -H 'X-Clusterview-Action: 1' -H 'Content-Type: application/json' \
    -d "{\"serviceId\":\"Deployment/cv-eval/$name\",\"fresh\":true}" |
    jq -r 'if .error then "ERROR: \(.error)" else "Find error (\(.confidence)): \(.summary)\n  \(.cause)\n  Fix: \(.fix[0] // "-")\n  Sent \(.sent | length) characters, \(.redactions) redacted, \(.model)" end'
  echo
done
