#!/usr/bin/env python3
"""Authoring aid for quality/catalog.json (P21-T03). Not committed.

The helper merges two things that must not drift apart:

- the documents: every ``REQ-*`` row of ``docs/REQUIREMENTS.md`` (description,
  modules, test references) and every ``THR-*`` row of ``docs/THREAT_MODEL.md``
  with its severity;
- the judgement table in ``quality/.authoring/rules-*.tsv``: the risk class, the
  actors and states, which test fills which evidence category, the five
  statements and the written reasons for the categories that do not apply.

The objective fields are never retyped: description, module directories and
``path::Function`` references come from the documents verbatim, so a typo cannot
enter the catalog through the hand of whoever wrote the judgement. A row that
names a test function the document row does not cite is an error.

Usage: python3 quality/.authoring/build.py [--strict]
"""

import argparse
import json
import pathlib
import re
import sys

ROOT = pathlib.Path(__file__).resolve().parents[2]
REQUIREMENTS = ROOT / "docs/REQUIREMENTS.md"
THREAT_MODEL = ROOT / "docs/THREAT_MODEL.md"
SECURITY_AUDIT = ROOT / "docs/SECURITY_AUDIT.md"
BUSINESS_RULES = ROOT / "docs/BUSINESS_RULES.md"
OUTPUT = ROOT / "quality/catalog.json"

ID = re.compile(r"^\*\*([A-Z]+-[A-Z0-9-]+)\*\*$")
REF = re.compile(r"^([\w./-]+)::([A-Za-z_][A-Za-z0-9_]*)$")
FUNCTION = re.compile(r"^func ([A-Za-z_][A-Za-z0-9_]*)\(", re.M)
CATEGORIES = ["positive", "negative", "limit", "authorization", "concurrency", "idempotency", "failure"]
REQUIRED = {
    "Q0": ["positive", "negative", "limit", "authorization", "concurrency", "idempotency"],
    "Q1": ["positive", "negative", "limit"],
    "Q2": ["positive", "negative"],
}
RISKS = ["Q0", "Q1", "Q2"]
STATEMENTS = ["authorization", "concurrency", "idempotency", "security", "privacy"]
SEVERITIES = ["Crítica", "Alta", "Média", "Baixa"]


def clean(text):
    text = text.replace("<br>", " ")
    text = re.sub(r"\*\*(.+?)\*\*", r"\1", text)
    text = re.sub(r"`(.+?)`", r"\1", text)
    text = re.sub(r"\[(.+?)\]\((.+?)\)", r"\1", text)
    return re.sub(r"\s+", " ", text).strip()


def table_rows(path, prefix):
    """The ``id -> (cells, columns)`` rows whose first cell is an id, in document order.

    The documents hold more than one shape of table — the requirements matrix and
    the invariants list of the same file order their columns differently — so the
    header that governs each row is carried along with it and the columns are
    looked up by name, never by position.
    """
    found = {}
    columns = None
    for line in path.read_text(encoding="utf-8").splitlines():
        if not line.startswith("|"):
            continue
        cells = [cell.strip() for cell in line.strip().strip("|").split("|")]
        if not cells:
            continue
        match = ID.match(cells[0])
        if match is None:
            names = [clean(cell) for cell in cells]
            if "ID" in names:
                columns = names
            continue
        if match.group(1).startswith(prefix):
            found[match.group(1)] = (cells, columns)
    return found


def column(columns, *names):
    """The index of the first column with one of these names, or ``None``."""
    if not columns:
        return None
    for name in names:
        if name in columns:
            return columns.index(name)
    return None


def cell(cells, columns, *names, fallback=None):
    index = column(columns, *names)
    if index is None or index >= len(cells):
        return fallback
    return cells[index]


def refs_of(cell):
    """The Go test references of a cell, in the order the document lists them."""
    found = []
    for part in cell.split("<br>"):
        match = REF.match(clean(part))
        if match and match.group(1).endswith(".go"):
            found.append(match.group(1) + "::" + match.group(2))
    return found


def modules_of(cell):
    modules = []
    for name in re.findall(r"`([a-z0-9/_-]+)`", cell):
        path = name if name.startswith("internal/") else "internal/" + name
        if (ROOT / path).is_dir():
            modules.append(path)
    return sorted(set(modules))


def severity_of(cells):
    for cell in cells:
        if clean(cell) in SEVERITIES:
            return clean(cell)
    return ""


def audit_evidence():
    """The per-threat evidence paths recorded by the security audit."""
    blocks = re.findall(r"```json\n(.*?)\n```", SECURITY_AUDIT.read_text(encoding="utf-8"), re.S)
    if not blocks:
        return {}
    data = json.loads(blocks[-1])
    return {threat["id"]: threat.get("evidence", []) for threat in data.get("threats", [])}


def judgement():
    """The curated rows, keyed by catalog id."""
    fields = ["id", "risk", "actors", "states", "positive", "negative", "limit", "authorization",
              "concurrency", "idempotency", "failure", "authorization_text", "concurrency_text",
              "idempotency_text", "security_text", "privacy_text", "not_applicable"]
    rules = {}
    for path in sorted((ROOT / "quality/.authoring").glob("rules-*.tsv")):
        for number, line in enumerate(path.read_text(encoding="utf-8").splitlines(), start=1):
            line = line.strip()
            if not line or line.startswith("#"):
                continue
            parts = [part.strip() for part in line.split(";")]
            # `failure` is the only category no risk class demands, so a row may
            # leave it out; the omission is filled here rather than guessed at
            # from a shifted column.
            if len(parts) == len(fields) - 1:
                parts.insert(fields.index("failure"), "")
            if len(parts) != len(fields):
                raise SystemExit(f"{path.name}:{number}: {len(parts)} fields, want {len(fields)}")
            rule = dict(zip(fields, parts))
            if rule["id"] in rules:
                raise SystemExit(f"{path.name}:{number}: {rule['id']} is declared twice")
            rules[rule["id"]] = rule
    return rules


def split(field):
    return [item.strip() for item in field.split(",") if item.strip()]


def declares(path, name):
    file = ROOT / path
    if not file.is_file():
        return False
    raw = file.read_text(encoding="utf-8", errors="replace")
    if path.endswith(".go"):
        return name in FUNCTION.findall(raw)
    return re.search(r"(^|\W)" + re.escape(name) + r"(\W|$)", raw) is not None


def resolve(qualified, document_refs):
    """Resolve a judgement reference; ``None`` means it does not resolve."""
    if "::" in qualified:
        path, name = qualified.split("::", 1)
        return qualified if declares(path, name) else None
    matches = [ref for ref in document_refs if ref.split("::")[1] == qualified]
    return matches[0] if len(matches) == 1 else None


def build():
    requirements = table_rows(REQUIREMENTS, "REQ-")
    threats = table_rows(THREAT_MODEL, "THR-")
    required_threats = {key: pair for key, pair in threats.items() if severity_of(pair[0]) in ("Crítica", "Alta")}
    curated = judgement()
    evidence = audit_evidence()
    problems = []
    rules = []

    for identifier, (cells, columns) in requirements.items():
        catalog_id = "QUAL-" + identifier
        row = curated.get(catalog_id)
        if row is None:
            problems.append(f"{identifier}: no judgement row ({catalog_id})")
            continue
        description = clean(cell(cells, columns, "Descrição", "Invariante do Negócio", "Ameaça", fallback=cells[1]))
        modules = modules_of(cell(cells, columns, "Módulo", "Módulo Principal", fallback=""))
        document_refs = refs_of(cell(cells, columns, "Teste", fallback=cells[-1]))
        evidence_paths = ["docs/REQUIREMENTS.md"]
        if BUSINESS_RULES.exists():
            evidence_paths.append("docs/BUSINESS_RULES.md")
        rule, trouble = assemble(catalog_id, row, description, "docs/REQUIREMENTS.md", modules,
                                 evidence_paths, document_refs)
        rules.append(rule)
        problems.extend(trouble)

    for identifier, (cells, columns) in required_threats.items():
        catalog_id = "QUAL-" + identifier
        row = curated.get(catalog_id)
        if row is None:
            problems.append(f"{identifier}: no judgement row ({catalog_id})")
            continue
        recorded = evidence.get(identifier, [])
        modules = sorted({"/".join(path.split("/")[:2]) for path in recorded if path.startswith("internal/")})
        modules = [module for module in modules if (ROOT / module).is_dir()] or ["internal/platform"]
        evidence_paths = ["docs/THREAT_MODEL.md"] + [path for path in recorded if (ROOT / path).exists()]
        rule, trouble = assemble(catalog_id, row,
                                 clean(cell(cells, columns, "Ameaça", fallback=cells[1])),
                                 "docs/THREAT_MODEL.md", modules, evidence_paths, [])
        rules.append(rule)
        problems.extend(trouble)

    extras = [catalog_id for catalog_id in curated if catalog_id not in {rule["id"] for rule in rules}]
    for catalog_id in sorted(extras):
        problems.append(f"{catalog_id}: judgement row without a document rule")
    return rules, problems, len(requirements), len(required_threats)


def assemble(catalog_id, row, description, source, modules, evidence_paths, document_refs):
    """One catalog entry plus the problems the judgement has with it."""
    problems = []
    identifier = catalog_id.removeprefix("QUAL-")
    tests = {}
    for category in CATEGORIES:
        resolved = []
        for qualified in split(row[category]):
            reference = resolve(qualified, document_refs)
            if reference is None:
                problems.append(f"{identifier}: `{qualified}` does not resolve in {category}")
                continue
            resolved.append(reference)
        tests[category] = resolved

    reasons = {}
    for item in [piece for piece in row["not_applicable"].split("+") if piece.strip()]:
        if "=" not in item:
            problems.append(f"{identifier}: reason `{item}` is not `category=text`")
            continue
        category, text = item.split("=", 1)
        reasons[category.strip()] = text.strip()

    risk = row["risk"]
    if risk not in RISKS:
        problems.append(f"{identifier}: risk `{risk}` is not one of {RISKS}")
    for category in REQUIRED.get(risk, []):
        if tests[category]:
            if category in reasons:
                problems.append(f"{identifier}: `{category}` has tests and a written reason")
        elif category not in reasons:
            problems.append(f"{identifier}: risk {risk} demands `{category}` with no test and no reason")

    rule = {
        "id": catalog_id,
        "source": source,
        "description": description,
        "risk": risk,
        "modules": modules,
        "actors": split(row["actors"]),
        "states": split(row["states"]),
        "tests": tests,
        "authorization": row["authorization_text"],
        "concurrency": row["concurrency_text"],
        "idempotency": row["idempotency_text"],
        "security": row["security_text"],
        "privacy": row["privacy_text"],
        "evidence": evidence_paths,
        "not_applicable": reasons,
    }
    for statement in STATEMENTS:
        if not rule[statement]:
            problems.append(f"{identifier}: blank `{statement}` statement")
    if not rule["actors"] or not rule["states"]:
        problems.append(f"{identifier}: actors or states are empty")
    if not modules:
        problems.append(f"{identifier}: no module directory resolves")
    if not rule["evidence"]:
        problems.append(f"{identifier}: no evidence path")
    return rule, problems


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--strict", action="store_true", help="any problem stops the catalog from being written")
    arguments = parser.parse_args()

    rules, problems, requirements, threats = build()
    for problem in problems:
        print("problem: " + problem, file=sys.stderr)
    if arguments.strict and problems:
        raise SystemExit(f"{len(problems)} problem(s): the catalog was not written")

    OUTPUT.write_text(json.dumps({"schema_version": 1, "rules": rules}, ensure_ascii=False, indent=2) + "\n",
                      encoding="utf-8")
    print(f"catalog: {len(rules)} rule(s) — {requirements} requirement(s) and {threats} critical/high threat(s)")


if __name__ == "__main__":
    main()
