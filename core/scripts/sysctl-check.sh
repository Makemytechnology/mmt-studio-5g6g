#!/usr/bin/env bash
# scripts/sysctl-check.sh — verify kernel tuning for sacore-web.
#
# Compares every sysctl in scripts/sysctl/99-sacore.conf against the
# live /proc/sys/... value and reports:
#
#   OK       — live value >= recommended (for single-value fields)
#               or matches / exceeds every slot (for triples)
#   LOW      — live value is below the recommendation; sacore will
#               run but can stall under burst
#   OVERRIDE — live value is higher than the recommendation (operator
#               tuned up further — no problem, just reporting)
#   MISSING  — the sysctl key doesn't exist on this kernel (old kernel
#               or module not loaded; for sctp.* this usually means
#               the sctp module isn't loaded)
#
# Returns non-zero exit if any LOW is found so CI / health checks can
# gate on it.

set -u

CONF="${1:-/etc/sysctl.d/99-sacore.conf}"
if [ ! -r "$CONF" ]; then
    CONF="$(dirname "$0")/sysctl/99-sacore.conf"
fi
if [ ! -r "$CONF" ]; then
    echo "sysctl-check: can't find 99-sacore.conf (tried $1 and scripts/sysctl/)" >&2
    exit 2
fi

color_ok()       { printf '\033[1;32m%s\033[0m' "$1"; }
color_low()      { printf '\033[1;31m%s\033[0m' "$1"; }
color_override() { printf '\033[1;34m%s\033[0m' "$1"; }
color_miss()     { printf '\033[1;33m%s\033[0m' "$1"; }
if [ ! -t 1 ] || [ -n "${NO_COLOR:-}" ]; then
    color_ok()       { printf '%s' "$1"; }
    color_low()      { printf '%s' "$1"; }
    color_override() { printf '%s' "$1"; }
    color_miss()     { printf '%s' "$1"; }
fi

fail=0
printf '%-46s  %-24s  %s\n' "sysctl" "live / expected" "status"
printf '%-46s  %-24s  %s\n' "------" "---------------" "------"

while IFS= read -r line; do
    # strip comments + blanks
    line="${line%%#*}"
    line="${line## }"
    line="${line%% }"
    [ -z "$line" ] && continue
    key="${line%% =*}"
    val="${line#*= }"
    # strip any surrounding whitespace
    key="$(echo "$key" | tr -d '[:space:]')"
    val="$(echo "$val" | sed 's/^[[:space:]]*//;s/[[:space:]]*$//')"

    path="/proc/sys/$(echo "$key" | tr '.' '/')"
    if [ ! -r "$path" ]; then
        printf '%-46s  %-24s  %s\n' "$key" "n/a" "$(color_miss 'MISSING')"
        continue
    fi
    live="$(cat "$path" | tr -s '[:space:]' ' ' | sed 's/^ //;s/ $//')"

    # Multi-value (triples like sctp_rmem / sctp_mem) — compare each slot.
    if echo "$val" | grep -q ' '; then
        low=0
        set -- $val
        i=1
        for expected; do
            live_slot="$(echo "$live" | awk -v n=$i '{print $n}')"
            if [ -z "$live_slot" ] || [ "$live_slot" -lt "$expected" ]; then
                low=1
            fi
            i=$((i+1))
        done
        if [ "$low" -eq 1 ]; then
            fail=1
            printf '%-46s  %-24s  %s\n' "$key" "$live | want $val" "$(color_low 'LOW')"
        else
            printf '%-46s  %-24s  %s\n' "$key" "$live" "$(color_ok 'OK')"
        fi
        continue
    fi

    # Single integer
    if [ "$live" -lt "$val" ] 2>/dev/null; then
        fail=1
        printf '%-46s  %-24s  %s\n' "$key" "$live < $val" "$(color_low 'LOW')"
    elif [ "$live" -gt "$val" ] 2>/dev/null; then
        printf '%-46s  %-24s  %s\n' "$key" "$live > $val" "$(color_override 'OVERRIDE')"
    else
        printf '%-46s  %-24s  %s\n' "$key" "$live" "$(color_ok 'OK')"
    fi
done < "$CONF"

echo ""
if [ "$fail" -eq 1 ]; then
    echo "One or more sysctl values are below recommended — run:"
    echo "    sudo cp scripts/sysctl/99-sacore.conf /etc/sysctl.d/"
    echo "    sudo sysctl --system"
    echo "or apply manually per the README."
    exit 1
fi
echo "All checks passed."
