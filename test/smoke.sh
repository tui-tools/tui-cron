#!/bin/bash
# Backend smoke test for tui-cron, run inside a lab guest.
#
# The contract (see tui-tools/tui-lab): this script runs on the guest as the
# unprivileged lab user, escalates with `sudo -n` only, prints a short PASS/FAIL
# table and exits non-zero if anything failed. The binary under test is at
# $TUI_LAB_BIN (default: tui-cron on PATH).
#
# What it proves is that the tool reads the machine's *real* schedulers and
# agrees with the machine's own tooling — not that a fake renders. The lab
# already covers --version and a --demo frame; this covers the backends.
#
# Everything here is read-only. The tool is never asked to write a drop-in,
# replace a crontab, enable a timer or run one now: a suite that changed what a
# machine has scheduled would be a suite nobody could run twice.
#
# Three shapes of machine are asserted, because all three are normal:
#
#   cron present    Ubuntu ships `cron`, Fedora ships `cronie` and calls the
#                   unit `crond`. The tool must find the tables and name the
#                   unit the machine actually uses.
#   cron absent     A minimal image — Omarchy Server is one — has no crontab at
#                   all. That is not a failure: the timers are listed and cron
#                   reports itself absent with a reason.
#   timers only     Every machine with systemd has timers, and on a container
#                   without a user bus the user timers are not read; the tool
#                   must say so rather than report none.
set -uo pipefail

bin="${TUI_LAB_BIN:-tui-cron}"
# TOOL is the manifest name, which is what a compatibility result is keyed on.
TOOL=tui-cron
pass=0
fail=0

# check runs one assertion. It takes a label, a command and a grep pattern the
# command's output must match. Output is captured so a failure can show it.
check() {
  local label="$1" command="$2" pattern="$3" output status
  output=$(eval "$command" 2>&1)
  status=$?
  if [[ $status -eq 0 ]] && grep -qE "$pattern" <<<"$output"; then
    printf 'PASS  %s\n' "$label"
    pass=$((pass + 1))
  else
    printf 'FAIL  %s (exit %d)\n' "$label" "$status"
    sed 's/^/      | /' <<<"$output" | head -12
    fail=$((fail + 1))
  fi
}

# check_absent is the inverse of a grep assertion: the command must succeed and
# its output must NOT contain the pattern. It is what proves a read stayed a
# read, which is a claim about something that did not happen.
check_absent() {
  local label="$1" command="$2" pattern="$3" output status
  output=$(eval "$command" 2>&1)
  status=$?
  if [[ $status -eq 0 ]] && ! grep -qE "$pattern" <<<"$output"; then
    printf 'PASS  %s\n' "$label"
    pass=$((pass + 1))
  else
    printf 'FAIL  %s (exit %d)\n' "$label" "$status"
    sed 's/^/      | /' <<<"$output" | head -12
    fail=$((fail + 1))
  fi
}

# --- compatibility evidence -------------------------------------------------
#
# The manifest's `tested` lists are generated, not claimed: they are rebuilt
# from compat/results.jsonl by tui-kit/tools/compat-sync.py, and this is where
# the lines of that file come from. The versions recorded are the ones the tool
# itself probed, read back out of --check, so they describe the machine that
# really ran the suite rather than what the tester assumed was installed.
#
# tui-cron declares two schedulers and only one of them has a version: cron
# prints none that is portable, so its block declares no version command and it
# is skipped here. There is nothing to claim about a program that will not say
# what it is.
record_compat() {
  local report="$1" outcome="$2" distro today block recorded=0
  block=$(sed -n '/"compat": \[/,/^  \]/p' <<<"$report")
  distro=$(. /etc/os-release && echo "${ID}-${VERSION_ID:-rolling}")
  today=$(date -u +%Y-%m-%d)

  while read -r backend version; do
    [[ -z $backend || -z $version ]] && continue
    local line
    line=$(printf '{"backend":"%s","date":"%s","distro":"%s","result":"%s","suite":"smoke","tool":"%s","version":"%s"}' \
      "$backend" "$today" "$distro" "$outcome" "$TOOL" "$version")
    printf 'compat-result: %s\n' "$line"
    if [[ -n ${TUI_COMPAT_RESULTS:-} ]]; then
      printf '%s\n' "$line" >>"$TUI_COMPAT_RESULTS"
    fi
    recorded=$((recorded + 1))
  done < <(awk '
    /"backend":/ { gsub(/[",]/, ""); b = $2 }
    /"version":/ { gsub(/[",]/, ""); if (b != "") { print b, $2; b = "" } }
  ' <<<"$block")

  if [[ $recorded -eq 0 ]]; then
    echo "      no backend version was probed, so no compatibility result is recorded"
  fi
}

echo "--- tui-cron smoke on $(. /etc/os-release && echo "$PRETTY_NAME")"

if ! command -v systemctl >/dev/null; then
  echo "FAIL  no systemd on this machine, which tui-cron needs for half of what it does"
  exit 1
fi

# Whether this machine has cron at all, decided the way the tool decides it: the
# binary, then the unit, whichever of the two names it goes by here.
if command -v crontab >/dev/null; then
  cron=present
else
  cron=absent
fi
if systemctl show crond.service --property=LoadState 2>/dev/null | grep -q '=loaded'; then
  cron_unit=crond.service
elif systemctl show cron.service --property=LoadState 2>/dev/null | grep -q '=loaded'; then
  cron_unit=cron.service
else
  cron_unit=none
fi
echo "      cron=$cron unit=$cron_unit"

# Whether this user can escalate, which decides nothing about the read path —
# every read tui-cron makes is unprivileged — and is printed so a failure can be
# read in context.
if sudo -n true 2>/dev/null; then
  privileged=yes
else
  privileged=no
fi
echo "      sudo -n=$privileged"

report=$("$bin" --check 2>&1)

# --- the report block ------------------------------------------------------
#
# --report is read-only and unprivileged, so it is smoked without sudo: a user
# who cannot escalate is exactly the one who most needs to be able to file a
# usable bug. What is asserted is that it names the backend this machine is
# being driven through, that it still answers under --demo, and that it keeps
# its privacy promise — the block goes into a public issue, so a home path or
# the host name appearing in it is a bug, not a cosmetic detail.
check "report names the backend" \
  "$bin --report" \
  '^backend: host'

check "report says the run was live" \
  "$bin --report" \
  '^mode: live$'

check "report works in demo mode too" \
  "$bin --demo --report" \
  '^backend: demo$'

check "and says so on the mode line" \
  "$bin --demo --report" \
  '^mode: demo'

# The distro and kernel lines are excluded from the host-name search rather
# than from the promise: they are built from /etc/os-release and from uname's
# release and machine fields, never from its nodename, and on a guest called
# "fedora" or "ubuntu" — which is most of them — the host name is a substring
# of the distribution's own. Everything else in the block is searched.
check "report leaks neither a home path nor the host name" \
  "$bin --report | grep -vE '^(distro|kernel): ' | grep -cE '/home/|$(uname -n)' || true" \
  '^0$'

# The schedulers line is this tool's own half of the block: systemd carries a
# version, cron carries only whether it is here, and the two must agree with
# what this guest really has.
check "report names both schedulers" \
  "$bin --report" \
  '^schedulers: systemd [0-9]+, cron '
case "$cron" in
  present) check "report agrees that cron is here" "$bin --report" 'cron installed' ;;
  absent) check "report agrees that cron is absent" "$bin --report" 'cron absent' ;;
esac

# 1. The read path works at all, as the plain lab user, and names the backend it
#    drove. Reading a timer, a crontab and a journal all take no privileges, so
#    this running unprivileged is itself the assertion that the tool does not
#    escalate to look at things it can already see.
check "check reads the schedulers unprivileged" \
  "$bin --check" \
  '"backend": "host"'

# 2. systemd answered and its timers were read. Every machine with systemd has
#    some — logrotate, systemd-tmpfiles-clean and fstrim are on nearly all of
#    them — so a zero here means the read failed rather than that the machine is
#    unusual.
check "systemd answered" "$bin --check" '"timersAvailable": true'
check "the timers were read" "$bin --check" '"timer": [1-9][0-9]*'

# 3. The timer count agrees with systemd's own. This is the assertion that
#    catches an enumeration that half-worked: below systemd 250 the JSON flag
#    does not exist and the list comes from `list-units` instead, and both paths
#    have to produce the same set.
want=$(systemctl list-units --type=timer --all --plain --no-legend --no-pager 2>/dev/null |
  awk '$1 ~ /\.timer$/' | wc -l)
got=$(sed -n 's/.*"timer": \([0-9]*\).*/\1/p' <<<"$report" | head -1)
if [[ -n $got && $got -eq $want ]]; then
  printf 'PASS  the %s timers match systemctl list-units\n' "$want"
  pass=$((pass + 1))
else
  printf 'FAIL  the tool reports %s timers, systemctl lists %s\n' "${got:-none}" "$want"
  fail=$((fail + 1))
fi

# 4. Every timer that was read carries a schedule, and the ones with an
#    OnCalendar carry the reading of it. A row with a blank schedule column is
#    the one thing this tool must never show.
check "the schedules were read" "$bin --check" '"Schedule": "[^"]+"'
check "a schedule was read back in English" "$bin --check" '"Explain": "[A-Z][^"]+"'

# 5. cron: present or absent, coherently. Both are normal machines and the two
#    fields must agree with each other and with the machine.
case "$cron" in
  present)
    check "cron is reported as installed" "$bin --check" '"cronInstalled": true'
    if [[ $cron_unit != none ]]; then
      check "the cron unit is named" "$bin --check" "\"cronUnit\": \"$cron_unit\""
      if [[ $(systemctl is-active "$cron_unit" 2>/dev/null) == active ]]; then
        check "cron is reported as running" "$bin --check" '"cronRunning": true'
      fi
    fi

    # /etc/crontab and /etc/cron.d are world readable everywhere, so whatever
    # job lines they carry must have been read — and the count has to be the
    # machine's own, not merely present. Asserting only that the field exists
    # passed on a machine whose tables were never opened, which is what this
    # suite was doing before Fedora had a cron at all.
    #
    # A cron job line begins with a schedule field: a digit, a `*` or an `@`.
    # Everything else in these files is a comment or a SHELL=/PATH=/MAILTO=
    # assignment, which is why a stock Fedora /etc/crontab contributes nothing
    # and its /etc/cron.d/0hourly contributes exactly one. Files whose name
    # carries a dot are skipped, because run-parts naming is what cron itself
    # applies to /etc/cron.d and the tool applies the same rule.
    want_crond=0
    for table in /etc/crontab /etc/cron.d/*; do
      [[ -f $table ]] || continue
      [[ $table == /etc/cron.d/* && $(basename "$table") == *.* ]] && continue
      want_crond=$((want_crond + $(grep -cE '^[[:space:]]*[0-9*@]' "$table" || true)))
    done
    got_crond=$(sed -n 's/.*"cron.d": \([0-9]*\).*/\1/p' <<<"$report" | head -1)
    if [[ -n $got_crond && $got_crond -eq $want_crond ]]; then
      printf 'PASS  /etc/crontab and /etc/cron.d parse to %s jobs, matching their job lines\n' "$want_crond"
      pass=$((pass + 1))
    else
      printf 'FAIL  the tool reports %s cron.d jobs, the tables carry %s job lines\n' \
        "${got_crond:-none}" "$want_crond"
      fail=$((fail + 1))
    fi

    # This account's own table, read back through cron's interface rather than
    # by opening /var/spool. On a fresh guest there is none, and cronie answers
    # `crontab -l` with "no crontab for <user>" on *stderr and exit 1* — which
    # the backend has to read as an empty table rather than as a failed read.
    # The assertion is that the count matches whatever `crontab -l` really
    # holds, which is 0 here, and that the rest of the report survived it.
    want_crontab=$(crontab -l 2>/dev/null | grep -cE '^[[:space:]]*[0-9*@]' || true)
    got_crontab=$(sed -n 's/.*"crontab": \([0-9]*\).*/\1/p' <<<"$report" | head -1)
    if [[ -n $got_crontab && $got_crontab -eq ${want_crontab:-0} ]]; then
      printf 'PASS  this account crontab parses to %s jobs, matching `crontab -l`\n' "${want_crontab:-0}"
      pass=$((pass + 1))
    else
      printf 'FAIL  the tool reports %s crontab jobs, `crontab -l` holds %s\n' \
        "${got_crontab:-none}" "${want_crontab:-none}"
      fail=$((fail + 1))
    fi

    # cron's log is read as the plain lab user through journalctl, with no
    # escalation. On a just-booted machine cronie has logged only its own
    # STARTUP and INFO lines and no job has run yet, so the right answer is
    # "no line for this job" — the one answer this must never be is the
    # detail the backend stamps on every job when the read itself failed.
    check_absent "cron's log was read without escalating" \
      "$bin --check" \
      "cron's log could not be read"
    ;;
  absent)
    # No crontab binary: the tool must say so with a reason, and must not
    # report cron as running.
    check "cron is reported as absent" "$bin --check" '"cronInstalled": false'
    check "a reason is given" "$bin --check" '"cronDetail": ".+"'
    check "cron is not reported as running" "$bin --check" '"cronRunning": false'
    check "no cron job was invented" "$bin --check" '"cron.d": 0'

    # A machine with no cron can still carry run-parts scripts, and Omarchy
    # Server does: /etc/cron.hourly/snapper, installed by the image, walked by
    # nothing. The tool listed none of them until this suite counted, because
    # the load bailed out before reading the directories. They must be listed —
    # and each one must say it does not run, which is the only reason listing
    # it is worth anything on a machine like this.
    if [[ $(find /etc/cron.hourly /etc/cron.daily /etc/cron.weekly \
      /etc/cron.monthly -maxdepth 1 -type f ! -name '.*' 2>/dev/null | wc -l) -gt 0 ]]; then
      check "the run-parts scripts are listed anyway" \
        "$bin --check" '"anacron-dir": [1-9][0-9]*'
      check "and each says nothing runs it" \
        "$bin --check" 'installed but nothing runs it'
      # The report is pretty-printed, so "this job is anacron-dir AND active"
      # spans lines and no single grep can express it. awk carries the kind
      # from each job's own object: "ID" opens one, "Kind" labels it, and
      # "Active" is read only while that label is the one being looked for.
      active_dirs=$(awk '
        /"ID":/          { kind = "" }
        /"Kind":/        { kind = $2 }
        /"Active": true/ { if (kind ~ /anacron-dir/) n++ }
        END              { print n + 0 }
      ' <<<"$report")
      if [[ $active_dirs -eq 0 ]]; then
        printf 'PASS  none of them is reported as active\n'
        pass=$((pass + 1))
      else
        printf 'FAIL  %s run-parts scripts are reported as active on a machine with no cron\n' \
          "$active_dirs"
        fail=$((fail + 1))
      fi
    fi
    ;;
esac

# 5b. The run-parts directories, which are a cron machine's other half and are
#     read by listing rather than by parsing. The count is the machine's own:
#     Fedora's cronie brings /etc/cron.hourly/0anacron and nothing else,
#     Debian's cron populates all four, and a machine without cron has none of
#     the directories at all — so this is one assertion for all three shapes.
#     Dot-prefixed entries are excluded, which is the rule the tool applies.
want_anacron=0
for dir in /etc/cron.hourly /etc/cron.daily /etc/cron.weekly /etc/cron.monthly; do
  [[ -d $dir ]] || continue
  want_anacron=$((want_anacron + $(find "$dir" -maxdepth 1 -type f ! -name '.*' 2>/dev/null | wc -l)))
done
got_anacron=$(sed -n 's/.*"anacron-dir": \([0-9]*\).*/\1/p' <<<"$report" | head -1)
if [[ -n $got_anacron && $got_anacron -eq $want_anacron ]]; then
  printf 'PASS  the cron.* directories parse to %s run-parts scripts, matching the files in them\n' "$want_anacron"
  pass=$((pass + 1))
else
  printf 'FAIL  the tool reports %s run-parts scripts, the directories hold %s\n' \
    "${got_anacron:-none}" "$want_anacron"
  fail=$((fail + 1))
fi

# 6. The counts add up: every kind is reported, and their sum is the total. A
#    script asserting on one of them needs the others to be there to compare.
#
#    The sum is added up in awk rather than piped to `bc`. bc is not a given:
#    Ubuntu ships it, and neither Fedora Cloud Base nor Omarchy Server has it,
#    so the original `paste -sd+ | bc` printed "command not found" and made this
#    assertion fail on two of the lab's three machines for a reason that had
#    nothing to do with the tool. awk is in every base image there is.
total=$(sed -n 's/.*"jobs": \([0-9]*\).*/\1/p' <<<"$report" | head -1)
sum=$(sed -n '/"counts": {/,/}/p' <<<"$report" |
  sed -n 's/.*: \([0-9]*\),*$/\1/p' | awk '{n += $1} END {print n + 0}')
if [[ -n $total && $total -eq ${sum:-0} ]]; then
  printf 'PASS  the %s jobs are accounted for across the five kinds\n' "$total"
  pass=$((pass + 1))
else
  printf 'FAIL  jobs=%s but the kinds sum to %s\n' "${total:-none}" "${sum:-none}"
  fail=$((fail + 1))
fi

# 7. A failing job and a timer with no Persistent are findings, not errors: the
#    tool reports them and still exits 0. A machine whose backup has been broken
#    for a month must not make this suite fail.
check "the findings are counted" "$bin --check" '"failedCount": [0-9]+'
check "the Persistent warnings are counted" "$bin --check" '"persistentWarnings": [0-9]+'

# 8. --check must never change anything. The drop-in this tool writes must not
#    exist because of a read, and no crontab may have been touched.
before_state=$(systemctl list-timers --all --no-legend --no-pager 2>/dev/null | wc -l)
before_dropins=$(find /etc/systemd/system -name '90-tui-cron.conf' 2>/dev/null | wc -l)
before_crontab=$(crontab -l 2>/dev/null | md5sum)
$bin --check >/dev/null 2>&1
after_state=$(systemctl list-timers --all --no-legend --no-pager 2>/dev/null | wc -l)
after_dropins=$(find /etc/systemd/system -name '90-tui-cron.conf' 2>/dev/null | wc -l)
after_crontab=$(crontab -l 2>/dev/null | md5sum)
if [[ "$before_state" == "$after_state" && "$before_dropins" == "$after_dropins" &&
  "$before_crontab" == "$after_crontab" ]]; then
  printf 'PASS  --check left both schedulers untouched\n'
  pass=$((pass + 1))
else
  printf 'FAIL  --check changed something (timers %s→%s, drop-ins %s→%s, crontab %s)\n' \
    "$before_state" "$after_state" "$before_dropins" "$after_dropins" \
    "$([[ $before_crontab == "$after_crontab" ]] && echo same || echo changed)"
  fail=$((fail + 1))
fi

# 9. And it prints no mutation: --check reports the read path, and a command
#    line in its output would mean it had built one.
check_absent "--check builds no command" \
  "$bin --check" \
  'systemctl (enable|disable|start|stop|restart|daemon-reload)|install -m 6|crontab /'

if [[ $fail -eq 0 ]]; then
  record_compat "$report" pass
else
  record_compat "$report" fail
fi

echo "--- tui-cron: $pass passed, $fail failed"
[[ $fail -eq 0 ]]
