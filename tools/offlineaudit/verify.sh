#!/usr/bin/env bash
# The offline and reproducible gate of the test platform (P22-T08), driven by
# `make test-offline`.
#
# It measures the four claims the task makes, in the order that makes each one
# falsifiable:
#
#   1. the manifest of the tree is a function of the tree: two runs of it answer
#      the same bytes, and the reader accepts it (the task's own validation,
#      "duas execuções produzem o mesmo manifesto de ferramentas");
#   2. the preload installs from the approved lockfiles and, with egress denied,
#      the caches answer them — `go mod verify` and the npm dry run over the
#      locked tree are the two steps that fail if a single dependency is
#      missing;
#   3. egress is denied *and observed*: a child process that tries to reach the
#      registry is refused by rule, the refusal names the host, and the refused
#      run files no artifact at all — the control against a gate that would pass
#      by never trying;
#   4. the PR and integration gates run offline, green, with nothing attempted,
#      and each files a manifest and a declaration in the evidence format of
#      P22-T07 — which the format's own reader accepts, and which it refuses
#      when the declaration is tampered with.
#
# It needs PostgreSQL — the same server `make test-unit` already needs — and
# Node with the two lockfiles of the tree. It installs from the lockfiles and
# runs two suites, which is why it is its own target and not part of
# `make verify`.
set -Eeuo pipefail

REPO_ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT"

GO="${GO:-go}"
NPM="${NPM:-npm}"
WORK_DIR="$(mktemp -d "${TMPDIR:-/tmp}/offlineaudit-XXXXXX")"
BIN="$WORK_DIR/offlineaudit"
ARTIFACTS="$WORK_DIR/artifacts"
PASSES=0
# The addresses the control tries. They are names a machine with a network
# answers in milliseconds and a hermetic one cannot answer at all.
CONTROL_URLS=("https://registry.npmjs.org/left-pad" "https://proxy.golang.org/github.com/rivo/uniseg/@v/list")

cleanup() {
  status=$?
  rm -rf "$WORK_DIR"
  return "$status"
}
trap cleanup EXIT

say() { printf 'offlineaudit: %s\n' "$*"; }
pass() { PASSES=$((PASSES + 1)); printf 'offlineaudit: ok — %s\n' "$*"; }
fail() { printf 'offlineaudit: FAIL — %s\n' "$*" >&2; exit 1; }

for tool in "$GO" "$NPM" mktemp cmp python3; do
  command -v "$tool" >/dev/null 2>&1 || fail "$tool is required"
done

# ---------------------------------------------------------------------------
# Measurement 1 — the vocabulary. The gate iterates what the tool declares
# instead of repeating it in the script, so a rule removed from the tool cannot
# keep passing here.
DECLARED="$("$GO" run ./tools/offlineaudit rules | python3 -c 'import json,sys; print(" ".join(json.load(sys.stdin)["rules"]))')"
WANT="manifest-non-canonical tool-drift image-unpinned image-unregistered missing-source unlocked-install egress-attempt"
[[ "$DECLARED" == "$WANT" ]] || fail "the vocabulary is $DECLARED, and the task declares $WANT"
pass "the vocabulary is declared and complete: $DECLARED"

"$GO" build -o "$BIN" ./tools/offlineaudit || fail "the tool does not build"

# ---------------------------------------------------------------------------
# Measurement 2 — the manifest is a function of the tree.
mkdir -p "$ARTIFACTS"
"$BIN" manifest -root . -out "$WORK_DIR/first" >/dev/null || fail "the manifest of the tree was refused"
"$BIN" manifest -root . -out "$WORK_DIR/second" >/dev/null || fail "the second manifest was refused"
cmp -s "$WORK_DIR/first/manifest.json" "$WORK_DIR/second/manifest.json" ||
  fail "two manifests of the same tree differ"
"$BIN" check -in "$WORK_DIR/first/manifest.json" >/dev/null || fail "the reader refuses the manifest of the tree"
python3 - "$WORK_DIR/first/manifest.json" <<'PY' || exit 1
import json, sys

manifest = json.load(open(sys.argv[1]))
tools = {tool["name"]: tool for tool in manifest["tools"]}
for name in ("go", "node", "npm"):
    if name not in tools or not tools[name]["measured"]:
        sys.exit(f"the manifest does not measure {name}")
if tools["go"]["declared"] != tools["go"]["measured"]:
    sys.exit("the Go toolchain of the machine disagrees with go.mod, and the manifest was accepted")
production = [image for image in manifest["images"] if image["requirement"] == "digest"]
development = [image for image in manifest["images"] if image["requirement"] == "registration"]
if not production or not development:
    sys.exit("the manifest does not register both the deployed and the development images")
for image in production:
    if not image["digest"]:
        sys.exit(f"{image['reference']} is deployed without a digest")
for image in development:
    if not image["digest"]:
        sys.exit(f"{image['reference']} is run by the development compose and no digest is registered")
if not manifest["sources"] or not manifest["rules"]:
    sys.exit("the manifest digests no source or declares no rule")
PY
pass "two runs of the tree answer the same manifest, with the pins, the image digests and the lockfiles"

# ---------------------------------------------------------------------------
# Measurement 3 — the preload installs from the lockfiles and the caches answer
# them with egress denied.
"$BIN" preload -root . -manifest "$WORK_DIR/preload.manifest.json" ||
  fail "the preload failed"
cmp -s "$WORK_DIR/preload.manifest.json" "$WORK_DIR/first/manifest.json" ||
  fail "the manifest of the preload is not the manifest of the tree"
pass "the preload filled the caches from the lockfiles and the offline steps were answered by them"

# ---------------------------------------------------------------------------
# Measurement 4 — egress is denied and observed. The control is a child process
# of the run, and the refusal has to name what it asked for.
for url in "${CONTROL_URLS[@]}"; do
  control_out="$ARTIFACTS/control-$(basename "$url")"
  mkdir -p "$control_out"
  status=0
  "$BIN" run -root . -out "$control_out" -name "control for $url" -- "$BIN" probe -url "$url" \
    2>"$WORK_DIR/control.err" || status=$?
  [[ "$status" -ne 0 ]] || fail "the run of the control against $url was accepted"
  grep -q 'egress-attempt' "$WORK_DIR/control.err" ||
    fail "the control against $url was refused without naming the rule: $(cat "$WORK_DIR/control.err")"
  host="$(printf '%s' "$url" | python3 -c 'import sys,urllib.parse; print(urllib.parse.urlsplit(sys.stdin.read()).hostname)')"
  grep -q "$host" "$WORK_DIR/control.err" ||
    fail "the refusal does not name the host of $url: $(cat "$WORK_DIR/control.err")"
  if [[ -n "$(ls -A "$control_out" 2>/dev/null)" ]]; then
    fail "the run that reached out for $url filed an artifact anyway"
  fi
  say "the control against $host was refused by rule and filed nothing"
done
pass "a run that reaches out is refused by egress-attempt, naming the host, and files nothing"

# ---------------------------------------------------------------------------
# Measurement 5 — the gates run offline. The rules each suite proves come from
# the quality registry and not from this script: an offline run that cited
# nothing would be refused by the evidence format.
rules_for() {
  python3 - "$1" <<'PY'
import json, sys

target = sys.argv[1]
registry = json.load(open("quality/evidence.json"))
suites = {suite["id"]: suite for suite in registry["suites"]}
rules = sorted({rule for entry in registry["evidence"]
                if suites[entry["suite"]]["command"] == target
                for rule in entry["rules"]})
print(",".join(rules))
PY
}

# offline_gate runs one Make target offline. The target travels as the command
# the registry declares (`make <alvo>`) because that is the key its rules are
# looked up by, and the word split is deliberate: the line this script runs is
# the one the registry names.
offline_gate() {
  local command="$1"
  local rules
  rules="$(rules_for "$command")"
  [[ -n "$rules" ]] || fail "the quality registry declares no rule for $command"
  say "running $command offline with the rules $rules"
  # shellcheck disable=SC2086
  "$BIN" run -root . -out "$ARTIFACTS" -name "$command" -kind go-test -rules "$rules" -- $command ||
    fail "$command is red offline"
  local slug
  slug="$(python3 -c 'import re,sys; print(re.sub(r"[^a-z0-9]+","-",sys.argv[1].lower()).strip("-"))' "$command")"
  [[ -s "$ARTIFACTS/$slug.declaration.json" ]] || fail "$command filed no declaration"
  [[ -s "$ARTIFACTS/$slug.manifest.json" ]] || fail "$command filed no manifest"
  cmp -s "$ARTIFACTS/$slug.manifest.json" "$WORK_DIR/first/manifest.json" ||
    fail "the manifest of $command is not the manifest of the tree"
  pass "$command ran offline with nothing attempted and filed its declaration and manifest"
}

offline_gate "make test-unit"
offline_gate "make test-integration"

# ---------------------------------------------------------------------------
# Measurement 6 — the declaration is parseable evidence, and the format judges
# it. A tampered declaration is refused, which is what separates a record from a
# file this script wrote.
declare_offline() {
  local declaration="$1" document="$2"
  "$GO" run ./tools/evidence declare -in "$declaration" -out "$document" \
    -commit "$(git rev-parse HEAD)" -seed "${ARENA_TEST_SEED:-0}" -tool "go=$("$GO" version)" ||
    fail "the evidence format refuses the declaration $declaration"
  "$GO" run ./tools/evidence check -in "$document" >/dev/null ||
    fail "the evidence format refuses the document it was given from $declaration"
}

for declaration in "$ARTIFACTS"/*.declaration.json; do
  document="$declaration.document.json"
  declare_offline "$declaration" "$document"
done
tampered="$WORK_DIR/tampered.declaration.json"
python3 - "$ARTIFACTS" "$tampered" <<'PY'
import json, pathlib, sys

source = sorted(pathlib.Path(sys.argv[1]).glob("*.declaration.json"))[0]
declaration = json.loads(source.read_text())
declaration["status"] = "ok"  # a result the format does not know
pathlib.Path(sys.argv[2]).write_text(json.dumps(declaration))
PY
if "$GO" run ./tools/evidence declare -in "$tampered" -out "$WORK_DIR/tampered.document.json" \
    -commit "$(git rev-parse HEAD)" -seed "${ARENA_TEST_SEED:-0}" -tool "go=$("$GO" version)" 2>"$WORK_DIR/tampered.err"; then
  fail "the evidence format accepted a declaration with a result it does not know"
fi
grep -q 'unknown-action' "$WORK_DIR/tampered.err" ||
  fail "the tampered declaration was refused without naming the rule: $(cat "$WORK_DIR/tampered.err")"
pass "every offline run files evidence the format accepts, and it refuses a tampered one"

printf 'offlineaudit: PASS — %d measurement(s), seed %s\n' "$PASSES" "${ARENA_TEST_SEED:-unset}"
