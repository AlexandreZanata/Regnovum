#!/usr/bin/env bash
# The isolation gate of the test platform (P22-T06), driven by
# `make test-isolation`.
#
# It measures the four claims the task makes, in the order that makes each one
# falsifiable:
#
#   1. the guard of one test refuses what outlives it, and a fixture that leaks
#      on purpose makes a run red — the falsification of the detector;
#   2. the leftovers audit finds what that fixture deliberately leaves on the
#      machine (a directory and a disposable database) — the audit's own
#      positive control, because a comparison that never finds anything is a
#      comparison nobody has watched work;
#   3. the suite runs with a shuffled order and high parallelism, printing the
#      seed so that a red run can be replayed;
#   4. after the run nothing of the run is left: no disposable database, no
#      connection attached to one, no temporary directory of the guard.
#
# It needs PostgreSQL — the same server `make test-unit` already needs — and
# nothing else: no network, no credential, no daemon. It runs the whole suite,
# which is why it is its own target and not part of `make verify`.
set -Eeuo pipefail

REPO_ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT"

GO="${GO:-go}"
AUDIT=("$GO" run ./tools/isolationaudit)
LEAK_FIXTURE="./internal/platform/testguard/testdata/leak/"
GUARD_PACKAGE="./internal/platform/testguard/"
WORK_DIR="$(mktemp -d "${TMPDIR:-/tmp}/isolationaudit-XXXXXX")"
PASSES=0
# The seed of the shuffled run: chosen by the gate, overridable by the operator
# (SEED=... make test-isolation reproduces a failure).
SEED=""

cleanup() {
  status=$?
  # Whatever happened, the machine is left as it was found: the leftovers of the
  # fixture are named in its state file, and that file is the only input the
  # audit accepts.
  if [[ -f "$WORK_DIR/leak-state.json" ]]; then
    "${AUDIT[@]}" clean -state "$WORK_DIR/leak-state.json" >/dev/null 2>&1 || true
  fi
  rm -rf "$WORK_DIR"
  return "$status"
}
trap cleanup EXIT

say() { printf 'isolationaudit: %s\n' "$*"; }
pass() { PASSES=$((PASSES + 1)); printf 'isolationaudit: ok — %s\n' "$*"; }
fail() { printf 'isolationaudit: FAIL — %s\n' "$*" >&2; exit 1; }

for tool in "$GO" mktemp grep; do
  command -v "$tool" >/dev/null 2>&1 || fail "$tool is required"
done

# ---------------------------------------------------------------------------
# The vocabulary: the rules the guard and the audit declare. The gate iterates
# it instead of repeating it in the script.
# ---------------------------------------------------------------------------
RULE_LINES="$WORK_DIR/rules.txt"
"${AUDIT[@]}" rules >"$RULE_LINES" || fail "the tools cannot answer their own vocabulary"

GUARD_RULES=()
AUDIT_RULES=()
while read -r origin rule; do
  [[ -z "${rule:-}" ]] && continue
  case "$origin" in
    guard) GUARD_RULES+=("$rule") ;;
    audit) AUDIT_RULES+=("$rule") ;;
    *) fail "a rule of the vocabulary carries an origin nobody knows: $origin" ;;
  esac
done <"$RULE_LINES"
[[ ${#GUARD_RULES[@]} -ge 5 ]] || fail "the guard declares ${#GUARD_RULES[@]} rules, which is too few to be the whole of it"
[[ ${#AUDIT_RULES[@]} -ge 3 ]] || fail "the audit declares ${#AUDIT_RULES[@]} rules, which is too few to be the whole of it"
pass "the tools declare ${#GUARD_RULES[@]} rules of the guard and ${#AUDIT_RULES[@]} of the audit"

# The baseline is taken before anything runs, and it is the whole point of the
# audit: the fixture below leaves a directory and a database behind, and a
# baseline taken after that would call them part of the machine.
"${AUDIT[@]}" snapshot -out "$WORK_DIR/before.json" || fail "the audit cannot take its baseline snapshot"

# The guard's own package proves every rule it declares: the fixtures of the
# package reproduce each one and refuse a rule nobody exercises. It is the gate
# for the whole vocabulary, so the end-to-end fixture below only has to prove
# that a run of a leaking package is red.
if ! "$GO" test -race -count=1 "$GUARD_PACKAGE" >"$WORK_DIR/guard.txt" 2>&1; then
  tail -40 "$WORK_DIR/guard.txt" >&2
  fail "the guard's own fixtures are red"
fi
pass "the guard's package is green under -race, with every declared rule exercised"

# ---------------------------------------------------------------------------
# 1. A package that leaks on purpose makes a run red, naming every leak.
# ---------------------------------------------------------------------------
if [[ ! -f "./internal/platform/testguard/testdata/leak/leak_test.go" ]]; then
  fail "the leak fixture is missing: without it this gate asks nothing"
fi

if ISOLATION_LEAK_STATE="$WORK_DIR/leak-state.json" "$GO" test -count=1 "$LEAK_FIXTURE" >"$WORK_DIR/leak.txt" 2>&1; then
  fail "the leak fixture passed: the guard did not refuse the leaks it exists to refuse"
fi

# Every rule the fixture produced has to be one the guard declares, and the
# fixture has to produce at least the leaks a single test can hold.
REPORTED=0
while IFS= read -r rule; do
  known=false
  for declared in "${GUARD_RULES[@]}"; do
    [[ "$rule" == "$declared" ]] && known=true && break
  done
  [[ "$known" == true ]] || fail "the fixture reported $rule, which no rule declares"
  REPORTED=$((REPORTED + 1))
done < <(grep -o 'testguard: [a-z-]*:' "$WORK_DIR/leak.txt" | sed 's/testguard: //; s/:$//' | sort -u)
[[ "$REPORTED" -ge 5 ]] || fail "the fixture produced $REPORTED rules, which is fewer than the leaks it holds"
pass "the leaking package went red, producing $REPORTED declared rules by name"

# ---------------------------------------------------------------------------
# 2. The audit finds what the fixture deliberately leaves behind.
# ---------------------------------------------------------------------------
"${AUDIT[@]}" snapshot -out "$WORK_DIR/after-leak.json" || fail "the audit cannot take its snapshot after the fixture"

if "${AUDIT[@]}" compare -before "$WORK_DIR/before.json" -after "$WORK_DIR/after-leak.json" >"$WORK_DIR/compare-leak.txt" 2>&1; then
  fail "the audit found nothing after a fixture left a directory and a database behind"
fi
for rule in leftover-database leftover-temporary-directory; do
  grep -q "isolationaudit: $rule:" "$WORK_DIR/compare-leak.txt" ||
    fail "the audit did not report $rule for the leftovers of the fixture"
done
[[ -f "$WORK_DIR/leak-state.json" ]] || fail "the fixture recorded no leftovers, so nothing can be removed"
"${AUDIT[@]}" clean -state "$WORK_DIR/leak-state.json" >/dev/null || fail "the audit cannot remove what the fixture left"
"${AUDIT[@]}" snapshot -out "$WORK_DIR/after-clean.json" || fail "the audit cannot take its third snapshot"
if ! "${AUDIT[@]}" compare -before "$WORK_DIR/before.json" -after "$WORK_DIR/after-clean.json" >/dev/null; then
  fail "the machine is not what it was after the leftovers of the fixture were removed"
fi
pass "the audit found both leftovers by rule, and the removal brought the machine back"

# ---------------------------------------------------------------------------
# 3. The suite runs with a shuffled order and high parallelism.
# ---------------------------------------------------------------------------
# The seed is chosen here and not left to the tool: with `-shuffle=on` the seed
# of a green run is not printed at all, and a red run whose order nobody knows
# is a red run nobody can replay. Choosing it also means the gate can be asked
# for one (SEED=..., the way a developer reproduces a failure).
SEED="${SEED:-$((RANDOM * 32768 + RANDOM))}"
SHUFFLE_LOG="$WORK_DIR/shuffle.txt"
if ! "$GO" test -count=1 -shuffle="$SEED" -parallel=16 ./... >"$SHUFFLE_LOG" 2>&1; then
  tail -60 "$SHUFFLE_LOG" >&2
  fail "the suite is red shuffled with -parallel=16 (seed $SEED)"
fi
pass "the whole suite is green shuffled with -parallel=16 (seed $SEED)"

# ---------------------------------------------------------------------------
# 4. Nothing of the run is left.
# ---------------------------------------------------------------------------
"${AUDIT[@]}" snapshot -out "$WORK_DIR/after-suite.json" || fail "the audit cannot take its last snapshot"
if ! "${AUDIT[@]}" compare -before "$WORK_DIR/before.json" -after "$WORK_DIR/after-suite.json" >"$WORK_DIR/compare-suite.txt" 2>&1; then
  cat "$WORK_DIR/compare-suite.txt" >&2
  fail "the suite left a database, a connection or a directory behind"
fi
pass "the shuffled run left no database, no connection and no temporary directory behind"

say "PASS ($PASSES measurements; shuffled seed ${SEED:-not chosen})"
