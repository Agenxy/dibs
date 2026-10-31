# Disk layout

### What Dibs writes to disk

- `~/.dibs/`: the data directory: ledger, keys, blobs. Move it with `-dir`.
- `~/.dibs-run/`: one small file per running daemon, so a second one can tell
  it is not alone and `dibs doctor` can report a fleet split across two boards.
  Nothing durable lives here; entries are removed when their daemon exits, and a
  leftover from a crash is detected as dead and swept. It is deliberately NOT in
  the data directory, because the whole point is to see daemons whose data
  directory you do not know about, and deliberately not in `$TMPDIR`, which
  differs between a shell, a launchd job and a sandbox: two daemons with
  different values would miss each other.
