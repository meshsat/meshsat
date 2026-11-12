#!/usr/bin/env python3
"""Bump one image digest pin in a kustomization.yaml (copy of meshsat-hub k8s/scripts/bump-pin.py; here for k8s/demo, MESHSAT-1845).

Usage: bump-pin.py <kustomization.yaml> <image-name-key> <sha256:digest> [provenance]

<image-name-key> is the `- name:` value of the entry in the `images:` block
(NOT the newName). The first `digest:` line after that entry is replaced.

PROVENANCE, and why it is optional (MESHSAT-1047). Without it this script
preserved the trailing comment, which is right for a hand-written one and wrong
for the one that describes the pin itself: the digest moved on every deploy while
its comment stayed frozen, so after the cdfaf7cb deploy kustomization.yaml read

    digest: sha256:da80ceba...  # main 4790250e (2026-09-08)

a digest from that day beside a commit from four days earlier. Anyone reading the
file to learn what is deployed was told the wrong commit.

Given a fourth argument, the trailing comment of the entry BEING BUMPED is
replaced with it. Given none, behaviour is exactly as before. It is optional
because this script is copied into the omoikane daemon repo and has several call
sites there; a new required positional argument would break every one of them.

Only the bumped entry is touched. The other comments in these files are
hand-written and carry information that must survive -- which image is for what,
and in one case why a particular version is pinned at all. Exit codes:
  0  updated (prints "updated <name> <old> -> <new>")
  3  already at that digest (prints "unchanged")
  4  entry not found — refuse rather than guess (a missing entry means the
     manifests don't consume this image; bumping nothing must be loud).

Line-based on purpose: no YAML round-tripper on the runners, and the images
block is machine-edited only here, so the shape is stable. Consumed by the
`bump_k8s_pin` CI jobs in this repo, www, and beta.
"""
import re
import sys


def main() -> int:
    path, name, digest = sys.argv[1], sys.argv[2], sys.argv[3]
    provenance = sys.argv[4] if len(sys.argv) > 4 else ""
    if not re.fullmatch(r"sha256:[0-9a-f]{64}", digest):
        print(f"refusing malformed digest: {digest!r}", file=sys.stderr)
        return 2
    if "\n" in provenance or "\r" in provenance:
        print("refusing multi-line provenance", file=sys.stderr)
        return 2
    lines = open(path).read().splitlines(keepends=True)
    in_entry = False
    for i, line in enumerate(lines):
        if re.match(rf"^\s*- name: {re.escape(name)}\s*(#.*)?$", line):
            in_entry = True
            continue
        if in_entry and re.match(r"^\s*- name: ", line):
            break  # next entry reached without a digest line
        if in_entry:
            m = re.match(r"^(\s*digest: )(sha256:[0-9a-f]{64})(.*)$", line)
            if m:
                old = m.group(2)
                if old == digest:
                    print(f"unchanged {name} {digest}")
                    return 3
                tail = m.group(3)
                if provenance:
                    # Two spaces before the hash, matching what is already in
                    # these files.
                    tail = f"  # {provenance.strip()}"
                lines[i] = f"{m.group(1)}{digest}{tail}\n"
                open(path, "w").write("".join(lines))
                print(f"updated {name} {old} -> {digest}")
                return 0
    print(f"entry not found in {path}: {name}", file=sys.stderr)
    return 4


if __name__ == "__main__":
    sys.exit(main())
