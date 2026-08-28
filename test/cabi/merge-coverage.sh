#!/bin/sh
# Merge Go coverage profiles.
#
# The C harness and the Go tests both execute the same packages, so their
# profiles name mostly the same blocks. Appending one to the other would list
# those blocks twice and overstate the totals, so each block is emitted once
# with the higher of the two counts: a block is covered if either run reached
# it.
#
# Usage: merge-coverage.sh out.profile in1.profile in2.profile [...]
set -eu

out=$1
shift

awk '
    /^mode:/ { mode = $0; next }
    {
        # "file.go:1.2,3.4 <numstmts> <count>"
        key = $1
        stmts[key] = $2
        if (!(key in count) || $3 + 0 > count[key] + 0) {
            count[key] = $3
        }
        if (!(key in order)) { order[key] = ++n; seq[n] = key }
    }
    END {
        print (mode == "" ? "mode: atomic" : mode)
        for (i = 1; i <= n; i++) {
            k = seq[i]
            print k, stmts[k], count[k]
        }
    }
' "$@" > "$out"
