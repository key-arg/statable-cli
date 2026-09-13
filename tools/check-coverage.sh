#!/usr/bin/env bash
# Compare the coverage table against the specification the server is serving
# right now.
#
# The unit tests check the table against the code. This checks the table
# against reality: an endpoint added to the API is invisible to everything else
# in this repository until someone runs this.
set -euo pipefail

SPEC="${1:-https://statable.com/api/v1/openapi.yaml}"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

curl -fsSL "$SPEC" -o "$tmp/openapi.yaml"

python3 - "$tmp/openapi.yaml" > "$tmp/live.txt" <<'PY'
import sys, re
text = open(sys.argv[1]).read()
out, path, in_paths = [], None, False
for ln in text.splitlines():
    if re.match(r'^paths:\s*$', ln):
        in_paths = True
        continue
    if in_paths and re.match(r'^\S', ln):
        break
    if not in_paths:
        continue
    m = re.match(r'^  (/\S*):\s*$', ln)
    if m:
        path = m.group(1)
        continue
    m = re.match(r'^    (get|post|put|patch|delete):\s*$', ln)
    if m and path:
        out.append(f"{m.group(1).upper()} {path}")
print("\n".join(sorted(set(out))))
PY

grep -oE '\{"(GET|POST|PUT|PATCH|DELETE)", "[^"]+"' internal/meta/coverage.go \
  | sed 's/{"//; s/", "/ /; s/"$//' | sort -u > "$tmp/table.txt"

missing="$(comm -23 "$tmp/live.txt" "$tmp/table.txt" || true)"
extra="$(comm -13 "$tmp/live.txt" "$tmp/table.txt" || true)"

status=0
if [ -n "$missing" ]; then
  echo "The API serves endpoints the coverage table does not mention:"
  echo "$missing" | sed 's/^/  /'
  echo
  echo "Add each to internal/meta/coverage.go with a command, or a reason there is none."
  status=1
fi
if [ -n "$extra" ]; then
  echo "The coverage table mentions endpoints the API no longer serves:"
  echo "$extra" | sed 's/^/  /'
  status=1
fi
if [ "$status" -eq 0 ]; then
  echo "Coverage table matches the live specification: $(wc -l < "$tmp/live.txt" | tr -d ' ') endpoints."
fi
exit "$status"
