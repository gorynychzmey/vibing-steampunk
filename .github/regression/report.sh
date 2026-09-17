#!/usr/bin/env bash
# Measure a working tree, then render the difference between two measurements.
#
# The workflow runs this twice against the same checkout — once on the PR head,
# once after `git checkout -f main` — so the script must not depend on anything
# inside the tree it is measuring. The workflow copies it out of the tree first;
# keep it self-contained (bash + go + benchstat, nothing else).
#
#   report.sh measure <outdir>
#   report.sh render  <before-dir> <after-dir>   > body.md
set -uo pipefail

# Packages that actually carry benchmarks. Benchmarking ./... would spend most
# of the run building packages with nothing to measure.
BENCH_PKGS=(./pkg/cache/ ./pkg/abaplint/)
BENCH_COUNT=6

measure() {
  local out="$1"
  mkdir -p "$out"

  # Binary size. A patch that quietly adds 4MB to the shipped CLI is worth a
  # line in the PR, and this is the cheapest possible way to see it.
  if go build -trimpath -o "$out/vsp" ./cmd/vsp 2>"$out/build.err"; then
    stat -c%s "$out/vsp" > "$out/size"
    rm -f "$out/vsp"
  else
    echo "" > "$out/size"
  fi

  # Coverage over pkg/ only: cmd/ is wiring and internal/mcp is handlers whose
  # coverage moves with whatever tool was last registered, which makes the
  # total too noisy to compare across a branch.
  if go test -coverprofile="$out/cover.out" -covermode=atomic ./pkg/... \
       > "$out/test.log" 2>&1; then
    echo pass > "$out/test.status"
  else
    echo fail > "$out/test.status"
  fi
  if [ -s "$out/cover.out" ]; then
    go tool cover -func="$out/cover.out" 2>/dev/null \
      | awk '/^total:/ {gsub(/%/,"",$3); print $3}' > "$out/coverage"
    # Per-package totals, for the "which package moved" part of the report.
    go tool cover -func="$out/cover.out" 2>/dev/null \
      | awk -F'\t+' '!/^total:/ {
          split($1, p, ":"); path = p[1];
          sub(/\/[^/]*$/, "", path);
          gsub(/%/, "", $NF);
          # cover -func gives per-function percentages; weight them equally per
          # function, which is enough to point at a package, not to grade it.
          sum[path] += $NF + 0; n[path]++;
        }
        END { for (k in sum) printf "%s %.1f\n", k, sum[k]/n[k] }' \
      | sort > "$out/coverage.pkgs"
  else
    echo "" > "$out/coverage"
    : > "$out/coverage.pkgs"
  fi

  # Benchmarks. -run=^$ so no test bodies execute; failures here must not sink
  # the report, so the exit code is swallowed and an empty file is a valid
  # result that render/ handles.
  go test -run='^$' -bench=. -benchmem -count="$BENCH_COUNT" "${BENCH_PKGS[@]}" \
    > "$out/bench.txt" 2>&1 || true
}

# Format a delta with an explicit sign, or an em dash when either side is blank.
delta() {
  local before="$1" after="$2" unit="${3:-}"
  if [ -z "$before" ] || [ -z "$after" ]; then echo "—"; return; fi
  awk -v b="$before" -v a="$after" -v u="$unit" 'BEGIN {
    d = a - b;
    if (d == 0) { print "±0" u; exit }
    printf "%+.1f%s\n", d, u;
  }'
}

render() {
  local before="$1" after="$2"
  local b_size a_size b_cov a_cov b_status a_status
  b_size=$(cat "$before/size" 2>/dev/null || echo "")
  a_size=$(cat "$after/size"  2>/dev/null || echo "")
  b_cov=$(cat "$before/coverage" 2>/dev/null || echo "")
  a_cov=$(cat "$after/coverage"  2>/dev/null || echo "")
  b_status=$(cat "$before/test.status" 2>/dev/null || echo unknown)
  a_status=$(cat "$after/test.status"  2>/dev/null || echo unknown)

  # The find-comment step matches on this heading. Changing it orphans every
  # comment this workflow has already posted.
  echo "## Regression test results"
  echo
  echo "| | \`main\` | this branch | Δ |"
  echo "|---|---|---|---|"

  printf '| `go test ./pkg/...` | %s | %s | |\n' \
    "$([ "$b_status" = pass ] && echo 'pass' || echo '**fail**')" \
    "$([ "$a_status" = pass ] && echo 'pass' || echo '**fail**')"

  if [ -n "$b_cov" ] || [ -n "$a_cov" ]; then
    printf '| coverage | %s | %s | %s |\n' \
      "${b_cov:-—}${b_cov:+%}" "${a_cov:-—}${a_cov:+%}" \
      "$(delta "$b_cov" "$a_cov" "pp")"
  fi

  if [ -n "$b_size" ] || [ -n "$a_size" ]; then
    local b_mb a_mb
    b_mb=$([ -n "$b_size" ] && awk -v s="$b_size" 'BEGIN{printf "%.2f", s/1048576}' || echo "")
    a_mb=$([ -n "$a_size" ] && awk -v s="$a_size" 'BEGIN{printf "%.2f", s/1048576}' || echo "")
    printf '| `vsp` binary | %s | %s | %s |\n' \
      "${b_mb:-—}${b_mb:+ MB}" "${a_mb:-—}${a_mb:+ MB}" \
      "$(delta "$b_mb" "$a_mb" " MB")"
  fi
  echo

  # Only name packages whose average moved by more than a rounding artefact.
  if [ -s "$before/coverage.pkgs" ] && [ -s "$after/coverage.pkgs" ]; then
    local moved
    moved=$(join -j1 "$before/coverage.pkgs" "$after/coverage.pkgs" 2>/dev/null \
      | awk '{ d = $3 - $2; if (d > 1 || d < -1) printf "| `%s` | %.1f%% | %.1f%% | %+.1fpp |\n", $1, $2, $3, d }')
    if [ -n "$moved" ]; then
      echo "<details><summary>Coverage by package (moved &gt;1pp)</summary>"
      echo
      echo "| package | \`main\` | this branch | Δ |"
      echo "|---|---|---|---|"
      echo "$moved"
      echo
      echo "</details>"
      echo
    fi
  fi

  if [ "$a_status" = fail ]; then
    echo "<details><summary>Failing test output (this branch)</summary>"
    echo
    echo '```'
    grep -E '^(---|\s+---|FAIL|ok\s+.*FAIL|panic:)' "$after/test.log" 2>/dev/null | head -60
    echo '```'
    echo
    echo "</details>"
    echo
  fi

  if [ -s "$before/bench.txt" ] && [ -s "$after/bench.txt" ] && command -v benchstat >/dev/null; then
    echo "<details><summary>Benchmarks (benchstat, n=$BENCH_COUNT)</summary>"
    echo
    echo '```'
    benchstat "$before/bench.txt" "$after/bench.txt" 2>&1 | head -80
    echo '```'
    echo
    echo "</details>"
    echo
  fi

  printf -- '<!-- commit %s -->\n' "${GITHUB_SHA:-unknown}"
}

case "${1:-}" in
  measure) shift; measure "$@" ;;
  render)  shift; render  "$@" ;;
  *) echo "usage: report.sh measure <outdir> | render <before> <after>" >&2; exit 2 ;;
esac
