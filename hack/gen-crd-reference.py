#!/usr/bin/env python3
"""Generate docs/app-crd-reference.md from the App CRD.

The CRD's openAPIV3Schema (built from the +kubebuilder godoc on
api/v1alpha1/*_types.go) is the single source of truth for the field-level
spec. This renders it to Markdown so the same descriptions/defaults/validation
users get from `kubectl explain` are readable in-repo — WITHOUT a hand-written
copy that would drift.

    hack/gen-crd-reference.py            # (re)write docs/app-crd-reference.md
    hack/gen-crd-reference.py --check    # exit 1 if the doc is stale (CI/pre-commit)

Kept in lockstep with the CRD by `just manifests` (folded in after sync-crd)
and enforced by the `crd-docs` pre-commit hook, exactly like helm-snapshots.
"""
import difflib
import sys
from pathlib import Path

import yaml

REPO = Path(__file__).resolve().parent.parent
CRD = REPO / "config/crd/baker.toggle-corp.com_apps.yaml"
DOC = REPO / "docs/app-crd-reference.md"

# Boilerplate Kubernetes object fields present on every CRD; skip so the doc is
# just the App-specific surface.
SKIP_TOP = {"apiVersion", "kind", "metadata"}


def fmt_desc(desc):
    """Collapse a multi-paragraph description into indented Markdown lines."""
    if not desc:
        return []
    # controller-gen joins godoc with real newlines; keep paragraph breaks,
    # flatten single wraps into spaces so it reflows in Markdown.
    out, para = [], []
    for line in desc.strip().splitlines():
        if line.strip():
            para.append(line.strip())
        else:
            if para:
                out.append(" ".join(para))
                para = []
    if para:
        out.append(" ".join(para))
    return out


def type_of(schema):
    t = schema.get("type", "")
    if t == "array":
        items = schema.get("items", {})
        return f"array<{type_of(items)}>" if items else "array"
    if t == "object" and "additionalProperties" in schema:
        ap = schema["additionalProperties"]
        vt = type_of(ap) if isinstance(ap, dict) else "string"
        return f"map<string,{vt}>"
    return t or "object"


def constraints(schema, required):
    """One-line facts a reader needs: required, default, bounds, enum, CEL."""
    bits = []
    bits.append("**required**" if required else "optional")
    if "default" in schema:
        bits.append(f"default `{schema['default']!r}`".replace("'", ""))
    for k, label in (
        ("minimum", "min"),
        ("maximum", "max"),
        ("minLength", "minLen"),
        ("maxLength", "maxLen"),
        ("minItems", "minItems"),
        ("maxItems", "maxItems"),
    ):
        if k in schema:
            bits.append(f"{label} `{schema[k]}`")
    if "enum" in schema:
        bits.append("enum " + ", ".join(f"`{e}`" for e in schema["enum"]))
    if "pattern" in schema:
        bits.append(f"pattern `{schema['pattern']}`")
    if "format" in schema:
        bits.append(f"format `{schema['format']}`")
    return bits


def cel_rules(schema):
    return [
        (v.get("rule", ""), v.get("message", ""))
        for v in schema.get("x-kubernetes-validations", [])
    ]


def walk(name, schema, path, required_set, depth, lines):
    """Emit one field (path `name`) and recurse into children."""
    indent = "  " * depth
    fq = f"{path}.{name}" if path else name
    required = name in required_set
    meta = ", ".join([f"`{type_of(schema)}`", *constraints(schema, required)])
    lines.append(f"{indent}- **`{fq}`** — {meta}")
    for rule, msg in cel_rules(schema):
        suffix = f" — {msg}" if msg else ""
        lines.append(f"{indent}  - _CEL_: `{rule}`{suffix}")
    for d in fmt_desc(schema.get("description")):
        lines.append(f"{indent}  {d}")

    # Recurse: object properties, then array-of-object item properties.
    child = schema
    if schema.get("type") == "array" and isinstance(schema.get("items"), dict):
        child = schema["items"]
    props = child.get("properties")
    if props:
        req = set(child.get("required", []))
        for key in sorted(props):
            walk(key, props[key], fq, req, depth + 1, lines)


def render(crd):
    ver = crd["spec"]["versions"][0]
    root = ver["schema"]["openAPIV3Schema"]
    names = crd["spec"]["names"]
    short = ", ".join(names.get("shortNames", [])) or "—"

    lines = [
        "# App CRD reference",
        "",
        "<!-- GENERATED from config/crd/baker.toggle-corp.com_apps.yaml by",
        "     hack/gen-crd-reference.py — DO NOT EDIT. Run `just manifests`",
        "     (or `just crd-docs`) after changing api/v1alpha1/*_types.go. -->",
        "",
        f"`{crd['metadata']['name']}` — kind `{names['kind']}`, "
        f"group `{crd['spec']['group']}`, version `{ver['name']}`, "
        f"shortNames: `{short}`.",
        "",
        "This is generated from the CRD schema (the +kubebuilder godoc on the Go",
        "types), so it matches `kubectl explain app.<field>` exactly and cannot",
        "drift. Defaults shown as `default` are CRD-level; a field documented as",
        "falling back to an operator/chart config has no CRD default (the operator",
        "resolves it at runtime).",
        "",
    ]
    for top in ("spec", "status"):
        node = root.get("properties", {}).get(top)
        if not node:
            continue
        lines += [f"## `.{top}`", ""]
        for d in fmt_desc(node.get("description")):
            lines.append(d)
        if node.get("description"):
            lines.append("")
        req = set(node.get("required", []))
        for key in sorted(node.get("properties", {})):
            if top == "spec" and key in SKIP_TOP:
                continue
            walk(key, node["properties"][key], top, req, 0, lines)
        lines.append("")
    return "\n".join(lines).rstrip() + "\n"


def main():
    check = "--check" in sys.argv[1:]
    crd = yaml.safe_load(CRD.read_text())
    new = render(crd)
    old = DOC.read_text() if DOC.exists() else ""
    if check:
        if new != old:
            sys.stderr.write(
                "docs/app-crd-reference.md is stale — run `just crd-docs` and "
                "re-stage it.\n"
            )
            sys.stderr.writelines(
                difflib.unified_diff(
                    old.splitlines(True),
                    new.splitlines(True),
                    "docs/app-crd-reference.md (on disk)",
                    "docs/app-crd-reference.md (regenerated)",
                )
            )
            return 1
        return 0
    DOC.write_text(new)
    return 0


if __name__ == "__main__":
    sys.exit(main())
