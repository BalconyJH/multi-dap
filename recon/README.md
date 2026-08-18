# M0 Reconnaissance Harness

This directory answers the nine M0 blockers of `docs/architecture.md` §12 with recorded,
reproducible evidence gathered against real MULTI hardware sessions.

## One hardware owner at a time

The debug probe and the target board are a single shared resource. No two probes, and no
probe and a MULTI GUI, may hold the debug probe simultaneously unless a probe is explicitly
testing coexistence (for example M0-1's "warm" run). Probe *authoring* is parallelizable;
probe *execution* is strictly serial. Run one probe, confirm its evidence file exists and its
`fatal` field is `null`, confirm no `mpythonrun.exe`, `850eserv2.exe`, or `multi.exe` process
is left holding the target, and only then start the next probe.

## `config.local.json` is required and gitignored

Every probe and the host runner read board-specific settings from `recon/config.local.json`.
That file is gitignored because it contains real board paths, connection strings, and target
symbol names — none of which may appear in a committed file. Copy `recon/config.example.json`
to `recon/config.local.json` and fill in real values before running anything that touches
hardware.

## Probes never print

Probe scripts run under `mpythonrun.exe`, MULTI's embedded Python 2.7 interpreter, and its
standard output is written with `WriteConsole`. Redirecting that handle to a pipe or a file
raises a modal Windows error dialog instead of failing cleanly. Probes therefore never write to
stdout: every result goes to a JSON evidence file via `reconlib.Recorder`, and the host runner
launches `mpythonrun.exe` with a brand new console window and never redirects its standard
handles.

## Running a probe

```
python recon/run.py <probe_stem> [-- extra args]
```

For example:

```
python recon/run.py p00_env
```

This launches `recon/probes/<probe_stem>.py` under `mpythonrun.exe`, waits for it to exit
(subject to `--timeout`, default 600s), and prints a step-by-step summary read back from
`recon/out/<probe_stem>.json`.

## `recon/out/` is not committed

`recon/out/` holds the JSON evidence every probe produces. It is deliberately gitignored: the
evidence contains target symbol names, memory addresses, and other consuming-project
identifiers that must never land in this repository. The scrubbed, committed record of what the
evidence showed is `docs/m0-findings.md`.
