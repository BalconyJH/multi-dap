# multi-dap Windows package

This package contains the `multi-dap.exe` daemon and the bridge source that it
launches through the local Green Hills MULTI `mpythonrun.exe`. MULTI 8 uses its
embedded Python 3 runtime by default; Python 2.7 remains an explicit fallback
through the project configuration. The separate native trace binding and
helper require Python 3.8+ and are not imported by the Python 2 bridge.

## Contents

```
multi-dap.exe
bridge/bridge.py
bridge/timemachine_helper.py
bridge/timemachine_native.py
editors/clion/README.md
editors/clion/profile-contract.json
examples/multi-dap.example.toml
README.md
LICENSE.txt
SHA256SUMS.txt
release-provenance.json
THIRD_PARTY_NOTICES.md
licenses/Go.txt
licenses/go-toml.txt
```

The package intentionally excludes `recon/`, raw probe evidence, daemon control records,
bridge ready files, and project connection data. The example TOML is not a runnable
configuration; first copy it outside the package, then populate the operator-private paths
and connection arguments.

`notifier.py` is also excluded. Although the source tree includes an experimental loopback
hint notifier, the current daemon does not install its `AFTER_GHS_STARTUP_PYTHON` hook;
shipping it would imply a runtime contract that does not exist.

## Starting the daemon

The packaged executable discovers `bridge/bridge.py` relative to its own path,
so the current working directory does not need to be the package root:

```powershell
Copy-Item .\examples\multi-dap.example.toml C:\private\project.toml
# Edit C:\private\project.toml with private paths and target-server arguments.
.\multi-dap.exe check --config C:\private\project.toml
.\multi-dap.exe serve --config C:\private\project.toml
```

Use `--bridge-script <absolute-path>` only when intentionally selecting a
different bridge. `multi-dap version` prints the embedded product version,
source commit when present, and reproducible build timestamp as JSON.

The native trace helper is resolved from the same directory as the selected
`bridge.py`: keep `timemachine_helper.py` and `timemachine_native.py` beside it,
including when using an explicit `--bridge-script`. There is no helper lookup
through the working directory or system Python. Native trace uses the selected
MULTI installation's standalone Python 3.8+ and vendor library; it does not require pip
or additional third-party Python packages. Neither Python nor the vendor DLL
is redistributed in this archive.

Packaging the helper enables an optional experimental interface, not proof
that a native runtime, source binding, decoder, or physical trace source is
qualified. Startup performs only static native-runtime checks; the helper is
started only by an explicit native trace open request. Missing prerequisites
disable this optional capability without disabling ordinary DAP.

If a TCP DAP client requires a fixed address, set `endpoints.dap.port` in the private
configuration to an unused fixed loopback port. Port `0` enables automatic assignment; the
actual address appears only in the single-line `ready` JSON written by the daemon to stdout.

The daemon owns only the child processes it creates: the selected bridge runner
and, after an explicit native trace open request, the optional trace helper.
It never cleans up the MULTI GUI, service router, target server, or any other
process by process name.

CLion uses the native **Cidr DAP CMake Debug** integration. Create a `multi-dap` DAP profile
under Debug Profiles: set Executable to the packaged `multi-dap.exe`, Arguments to
`proxy --config "<absolute-project.toml>"`, communication to `stdin/stdout`, and the **Launch** tab JSON to
`{"request":"attach"}`. Do not use this profile's Attach tab.
Use the same absolute private TOML as the Before Launch `ensure` action. The
proxy validates its normalized runtime semantics and rejects a same-ID daemon
created from different semantics. `[probe].id` must still be globally unique
for that project and physical-probe binding because it keys the record and
probe lock. The legacy `proxy --probe-id` mode is unbound and is not the
supported CLion path. If a custom `--control-dir` is used, pass the same private
absolute path to both `ensure` and `proxy`.

In **Run | Edit Configurations**, select each applicable CMake Application configuration (for example,
`<your-cmake-application>`) and choose the `multi-dap` Debug Profile. Click each configuration's **Debug (bug icon)**; never click
the green Run button, which would make Windows attempt to execute the target ELF.

Ensure the daemon is ready before debugging. Under **Settings | Tools | External Tools**, create
`multi-dap ensure`: set Program to the packaged `multi-dap.exe`, Arguments to
`ensure --config "<absolute-project.toml>" --bridge-script "<absolute-path-to-bridge.py>" --acquisition cold`, and set the
Working directory to `$PROJECT_DIR$` or the absolute project root. Then add it to **Before launch** for each CMake
configuration through `+ | Run External Tool`. You may instead run the same `ensure` command
manually before debugging. It is a synchronous, short-lived, idempotent explicit cold
acquisition: an existing, authenticated multi-dap daemon is reused; otherwise it may create a
new MULTI session, and it never implicitly falls back to warm acquisition. To reuse an
operator's existing MULTI session, explicitly select warm acquisition as described in the full
configuration documentation. The audited CLion contract keeps
`--bridge-script` explicit and requires the absolute path to `bridge/bridge.py`
within the extracted package. This makes the selected adapter bundle visible
even though the executable can discover its own companion bridge
automatically. Working directory is the target project root.

The legacy `multi-dap attach` is a Python configuration and must be disabled or removed. See
`editors/clion/README.md` for the full configuration, capability boundaries,
and Hardware validation checklist; `profile-contract.json` is a field contract,
not a file importable by CLion.

## Command-line and analysis features

Run `.\multi-dap.exe help` for the complete command syntax. The bundle includes
`version`, installation discovery (`multi-version`), configuration checks
(`doctor` / `check`), lifecycle commands (`serve`, `ensure`, `status`,
`shutdown`, `diagnose`), the authenticated DAP `proxy`, and the two offline
analysis commands below. Discovery, lifecycle, and target operations retain
their separate documented authorization requirements.

Add bounded host-side recording to the existing editor's stdio proxy command:

```powershell
.\multi-dap.exe proxy --config C:\private\project.toml `
  --perf-output C:\private\host-session-001.trace.json `
  --perf-max-events 10000
```

This observes only the DAP traffic the proxy already forwards. The editor must
still provide stdin/stdout DAP traffic, and its daemon must already be ready.
The recorder does not start another session or sample the target; its Chrome
Trace Event JSON measures host request latency, not target CPU/function time.
Protocol payloads and private source fields are excluded.

Import an operator-provided Profiler CSV with an explicit schema mapping:

```powershell
.\multi-dap.exe profile import --input C:\private\profile.csv `
  --mapping C:\private\profile-mapping.json `
  --output C:\private\profile-report-001.json
```

The mapping must declare the actual columns, units, missing-value rules, view,
and provenance. No vendor CSV layout or self/inclusive-time meaning is guessed.
This command imports files only; it does not ask MULTI to generate an export.

Normalize a `multi-dap.native-capture.v1` packet envelope:

```powershell
.\multi-dap.exe trace normalize --input C:\private\capture.json `
  --output C:\private\normalized-001.json `
  --chrome-output C:\private\target-001.trace.json
```

This is multi-dap's own capture envelope, not a decoder for arbitrary `.dsnap`,
`.tra`, `.trs`, or CSV files. Optional Chrome output requires supported
physical-time observations and explicit context; cycles or instructions are
not silently treated as seconds. Both offline commands operate without a
daemon, MULTI process, or target connection. See the source repository's
`docs/native-analysis.md` and `docs/performance.md` for the input contracts and
interpretation limits.

All analysis input/output paths are absolute. Outputs require new filenames
in an existing private directory and are never overwritten. Keep captures and
reports outside the installation and source tree; they are not package assets.

Native acquisition is an experimental custom DAP interface, not a `trace start`
command-line subcommand. An attached, configured client must first
observe `multiDapSupportsNativeTraceRequests`, then use
`multi-dap/trace/open`, `/poll`, `/read`, `/start`, `/stop`, `/close`, or
`/status`. Opening requires an explicitly acknowledged, unverified source
declaration; start/stop require separate authorization. They acknowledge trace
requests, never target-running/stopped state, and do not continue/pause the
CPU. `status` reads the local receipt without native I/O, including after a
failure. This does not imply a ready-made IDE trace panel or qualified
hardware tracing.

For one read-only packet interval, the `trace capture` client uses that same
authenticated DAP path against an already-running, configuration-bound daemon:

```powershell
.\multi-dap.exe trace capture --config C:\private\project.toml `
  --session-id 17 --source-id operator-selected-source `
  --acknowledge-unverified-source --count 4096 `
  --output C:\private\capture-001.json
```

Replace the illustrative `17` with the explicitly selected native traceserv
session ID, not a core, process, thread, or port number. Use `--control-dir`
when required by the existing daemon. The command refuses an occupied frontend,
does not create a daemon or start collection, and never issues execution,
reset, download, or flash commands. It opens/polls/reads the chosen native
source, explicitly closes it, and detaches without terminating or suspending
the debuggee. Successful output is a private packet envelope for
`trace normalize`; failures can leave an empty/partial private file and never
report capture success. Native disconnect does not imply collection stopped.
This remains software-tested, not a qualified real-native/hardware workflow.

## Integrity

`SHA256SUMS.txt` lists the SHA-256 for every payload file except the manifest itself. Run this
from the package root:

```powershell
Get-Content .\SHA256SUMS.txt | ForEach-Object {
    $digest, $path = $_ -split '  ', 2
    if ((Get-FileHash -Algorithm SHA256 -LiteralPath $path).Hash.ToLowerInvariant() -ne $digest) {
        throw "SHA-256 mismatch: $path"
    }
}
```

The release directory also contains
`multi-dap-<version>-windows-amd64.zip.sha256`, which protects the downloaded
archive itself. It is not stored inside the archive.

`release-provenance.json` binds the package version, optional source commit,
reproducible build time, target, Go toolchain, and executable digest. The file
is itself covered by `SHA256SUMS.txt`.

`THIRD_PARTY_NOTICES.md` identifies the Go standard library and go-toml v2;
their complete license texts are under `licenses/`. Green Hills MULTI remains a
separately licensed external prerequisite and is not included in this package.

Before release, also run the default full mode of
`scripts/Invoke-WindowsReleaseAcceptance.ps1` in the source repository. Without downloading Go
dependencies, it runs bridge/native-binding/helper host tests, available MULTI
Python compilation checks, and reproducibility plus manifest/allowlist
validation for two independent ZIPs. Package self-tests also exercise the
packaged help surface and both offline analysis commands with synthetic
fixtures, without starting a daemon or a target. See
`docs/m6-packaging-and-editors.md` for details.
The explicit Python 2 compilation fallback applies only to the legacy
bridge/recon sources. The separate native compilation step requires Python
3.8+ and compiles both the binding and helper. These software checks do not
load a vendor DLL, operate a target, or establish hardware qualification.
