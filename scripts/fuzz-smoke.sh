#!/usr/bin/env bash
# Run every Fuzz target briefly (seed corpus + N generated inputs).
set -euo pipefail
N="${FUZZ_SMOKE_N:-1000x}"
files=()
while IFS= read -r f; do files+=("$f"); done < <(grep -rl --include='*_test.go' '^func Fuzz' . || true)
for f in "${files[@]}"; do
  d=$(dirname "$f")
  for t in $(grep -ho '^func Fuzz[A-Za-z0-9_]*' "$f" | sed 's/^func //'); do
    echo "fuzz-smoke: $d $t"
    go test "$d" -run '^$' -fuzz "^${t}\$" -fuzztime="$N"
  done
done
