# Real case for 1.0: a user's cron job, moved to a systemd timer

Point 5 of the family's [stability bar](https://github.com/tui-tools/tui-kit/blob/main/docs/stability.md):
the tool has driven a real machine end to end for its main purpose, and what
that surfaced was fixed or filed.

## The machines, plainly

A lab virtual machine, not a production server: Fedora 44 Cloud Edition with
cronie and systemd 259, booted by [tui-lab](https://github.com/tui-tools/tui-lab)
under KVM on the project's own server. The tool ran as the ordinary user `lab`
over SSH, escalating with `sudo -n`. The build was `main` on 2026-11-18, then
`main` with [#36](https://github.com/tui-tools/tui-cron/pull/36).

The same build also ran read-only on the project's own server, a Fedora 44
machine in daily use: `tui-cron --check` exited 0 and listed its timers,
user timers, `cron.d` and anacron jobs with cron running. Nothing was changed
on that machine.

## What was done, end to end

1. `3`, `a`: a line for `lab`'s own crontab, `*/2 * * * *` with a redirection.
   The form read the schedule as `Every 2 minutes`, and the dialog showed the
   whole table as a diff. This is where the bug below was found.
2. `t` on that line refused, correctly: the command used the shell (`>>`),
   and a systemd service has none.
3. `e`: the command changed to `/usr/bin/systemd-cat -t disk-usage
   /usr/bin/df -h /`, previewed as a one-line diff of the table.
4. `t`: the timer and the service, staged and checked with `systemd-analyze
   verify` before the question. `OnCalendar=*-*-* *:00/2:00`,
   `Persistent=true`, and `User=lab` on the service, since the line came from
   `lab`'s table. The dialog said the cron line stays and the timer is not
   enabled, so the job would not run twice.
5. `2`, `E` on the new timer: `systemctl enable`. `s`: `systemctl start`.
   `systemctl list-timers` then showed it, last run 14 seconds earlier, and
   `journalctl -t disk-usage` had the `df` output; the service ended with
   `Result=success` as `User=lab`.
6. `3`, `d` on the original line: the table rewritten without it, through
   `crontab` run as `lab`.

At 18:10:00 the journal had the `df` output twice, one from cron and one from
the timer, because the cron line was removed after the timer was armed. That
is the double run the dialog in step 4 warns about.

## What it surfaced

- Step 1, before the fix: "Add a line to lab's crontab", with the diff on
  `/var/spool/cron/lab`, ran `sudo -n crontab <file>`. `crontab` picks the
  table from the user it runs as, so the line went into root's crontab, to
  run as root, and `lab`'s stayed empty. Fixed in
  [#36](https://github.com/tui-tools/tui-cron/pull/36): your own table is
  changed by `crontab` run as you. Checked again on the same machine: the
  dialog showed `$ crontab /tmp/tui-cron-…/crontab` and the line landed in
  `/var/spool/cron/lab`.
- `--check` named the count of timers missing `Persistent=true`
  `persistentWarnings`, beside `needPersistent`; renamed to
  `needPersistentCount` for the freeze, matching `failed` and `failedCount`.

## What it did not cover

Another account's table and `/etc/cron.d` (which need root), the timer
schedule drop-in (`e` on a vendor timer) and deleting a timer this tool wrote
were not exercised on this machine.

## Second run: tui-kit v0.5.0, on all three families

tui-kit v0.5.0 changed how a command runs as root: it is looked up only in the
system directories and must be owned by root, it starts from a minimal
environment, and as root the tool refuses configuration files root does not
own and `TUI_<TOOL>_*` variables carried in from sudo, su, doas or pkexec. So
the work above was repeated on 2026-11-18 with a static build of this branch's
head, `06c020e` (main with tui-kit v0.5.0 and Go 1.27.4 merged in),
installed root-owned as `/usr/local/bin/tui-cron`, on fresh tui-lab machines on
the project's own server: Ubuntu 24.04.5 (apt), Fedora 44 (dnf) and Omarchy
Server 4.0.1 (pacman), lab VMs again, not production hosts.

- The smoke test passed on all three as the ordinary user `lab`, who
  escalates with `sudo -n`, and again as root (`sudo env TUI_LAB_BIN=<binary> bash smoke.sh`):
  26/26 on Ubuntu and Fedora, 27/27 on Omarchy Server, as user and as root. On Omarchy Server the escalation and every command it runs were
  found through `/usr/sbin`, which the new kit trusts; nothing was refused.
- The main change was driven through the TUI in tmux once per family as
  `lab` and once as root, and checked on the machine afterwards:
  a line added to the account's own crontab through `a` on Ubuntu and Fedora,
  found by `crontab -l` as that account (`lab`, then root, which since #36
  is not the same table). Omarchy Server has no cron; there a timer was
  created with `c` as `lab` and as root and read back with `systemctl cat`.
- tui-update then upgraded all three machines (see its report), Omarchy
  Server to 4.0.2. The smoke test passed again on the upgraded machines, and
  the new versions it observed are the 2026-11-18 lines in
  `compat/results.jsonl` (systemd 262 on Omarchy Server 4.0.2). The smoke test run as root on a machine where another account also has a crontab counts both tables while `crontab -l` shows root's alone, and fails that one check; with the other table removed it passed. That is the test assuming it runs as an ordinary user, not the tool.
- The contract change: `--check` carries `needPersistentCount` and no
  `persistentWarnings`, on all three, as user and as root.

New finding, filed as #38: on a machine with no cron, `a` opens the crontab
form and `enter` does nothing, silently.
