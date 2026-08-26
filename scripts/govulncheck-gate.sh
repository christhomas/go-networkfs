#!/usr/bin/env bash
#
# govulncheck-gate.sh — fail on any vulnerability except the ones we have
# looked at and consciously accepted.
#
# A bare `govulncheck ./...` is all-or-nothing: one finding with no
# available fix turns the check permanently red, and a red check that
# everyone has learned to ignore reports nothing at all. This keeps the
# check meaningful by naming what is accepted and why, so anything new
# still stops the build.
#
# Adding an entry is a decision, not a workaround. Each needs a reason
# that says why the fix is not simply being applied.
set -uo pipefail

# Nothing is accepted.
#
# GO-2026-5051 used to be, on the grounds that hirochachacha/go-smb2 had
# no fix and the maintained fork made things worse: five reachable
# vulnerabilities instead of one, and MPL-2.0 back in the compiled set
# via Kerberos. Backporting the fix to a fork of the original got it
# without either, so the exception is gone rather than merely smaller.
#
# An empty list is the point. Every entry here is a vulnerability this
# project calls and has decided to live with, and the honest number of
# those is zero.
ACCEPTED=()

report=$(mktemp)
trap 'rm -f "$report"' EXIT

govulncheck -format json ./... > "$report" 2>/dev/null
status=$?

# govulncheck exits 3 when it finds something and 0 when it does not;
# anything else means it failed to run, which must not read as "clean".
if [ "$status" -ne 0 ] && [ "$status" -ne 3 ]; then
    echo "govulncheck did not run (exit $status)" >&2
    exit 1
fi

ACCEPTED_LIST="${ACCEPTED[*]}" python3 - "$report" <<'PY'
import json, os, sys

accepted = set(os.environ["ACCEPTED_LIST"].split())

# The output is a stream of pretty-printed JSON objects run together,
# not one per line, so it has to be decoded object by object rather than
# line by line.
def objects(text):
    dec, at = json.JSONDecoder(), 0
    while at < len(text):
        while at < len(text) and text[at] in " \t\r\n":
            at += 1
        if at >= len(text):
            return
        obj, at = dec.raw_decode(text, at)
        yield obj

# A finding whose trace begins at a named function is one this project's
# own code can reach. Findings without that are in code we depend on and
# never call, which govulncheck reports separately and we do not gate on.
called = {}
for obj in objects(open(sys.argv[1]).read()):
    if not isinstance(obj, dict):
        continue
    f = obj.get("finding")
    if not isinstance(f, dict):
        continue
    trace = f.get("trace") or []
    if trace and trace[0].get("function"):
        called.setdefault(f["osv"], trace[0].get("module", "?"))

unexpected = {k: v for k, v in called.items() if k not in accepted}
stale = accepted - set(called)

for osv, module in sorted(called.items()):
    mark = "accepted" if osv in accepted else "NEW"
    print(f"  [{mark}] {osv}  {module}")

if stale:
    # An accepted entry that no longer fires is a note nobody needs, and
    # leaving it there hides the next real finding behind stale text.
    print()
    print("These are accepted but no longer reported — remove them from")
    print("scripts/govulncheck-gate.sh:")
    for osv in sorted(stale):
        print(f"  {osv}")

if unexpected:
    print()
    print(f"{len(unexpected)} vulnerability(ies) this project calls and has not accepted.")
    print("Fix them, or add an entry to scripts/govulncheck-gate.sh saying why not.")
    sys.exit(1)

print()
print("No unaccepted vulnerabilities.")
PY
