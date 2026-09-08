# Getting Started

This guide builds and starts the adapter from source. It does not replace the
target-specific Green Hills MULTI setup or qualify a hardware configuration.

## Prerequisites

- Windows amd64 for the production runtime.
- Go 1.26.6, matching `go.mod`.
- A Green Hills MULTI installation whose `mpythonrun.exe` provides Python 2.7
  and the MULTI Python modules.
- A project file, target-server arguments, and one absolute ELF path per
  configured core.
- A numeric loopback address for every configured endpoint.

Python 3.12, `uv`, and Node.js are needed only for host tests and editor
development; they are not production runtime dependencies.

## Build

From the repository root:

```powershell
go build -mod=readonly -o .\multi-dap.exe .\cmd\multi-dap
```

## Configure

Copy the example outside any public or shared location, then replace every
placeholder with an absolute local value:

```powershell
Copy-Item .\multi-dap.example.toml C:\private\project.toml
.\multi-dap.exe check --config C:\private\project.toml
```

Do not commit the completed configuration. It can contain private paths and
target connection arguments. Service-router coordinates and a warm session's
primary ELF are runtime inputs and must not be stored in the TOML file.

Give `[probe].id` a globally unique value for the exact project and physical
probe. Config-based control commands also bind an opaque digest of all
normalized validated runtime values to the daemon record. Shut down a daemon
before editing those values; a mismatch is rejected rather than reused. See
the [project configuration reference](configuration.md) for every field and
cross-field rule.

For multicore numeric-address memory or disassembly requests, set
`inspection.default_core` explicitly. Without it, ambiguous requests are
rejected before they reach MULTI.

## Start a cold session

```powershell
.\multi-dap.exe serve `
  --config C:\private\project.toml `
  --bridge-script <repository-root>\bridge\bridge.py `
  --session-mode cold
```

The process writes one JSON `ready` record to stdout after the DAP endpoint is
available. Keep the process under operator control. Cold acquisition may create
a new MULTI target connection and can contend with an operator-owned session.

## Reuse an existing session

Warm acquisition is explicit and fail-closed:

```powershell
.\multi-dap.exe serve `
  --config C:\private\project.toml `
  --bridge-script <repository-root>\bridge\bridge.py `
  --session-mode warm `
  --service-router-host 127.0.0.1 `
  --service-router-port <runtime-port> `
  --primary-elf C:\absolute\configured-core.elf
```

The adapter accepts only a unique, stable, full-path program binding. It does
not fall back to a cold connection, a basename match, or the first window.

## Operate the daemon

```powershell
.\multi-dap.exe status --config C:\private\project.toml
.\multi-dap.exe diagnose --config C:\private\project.toml
.\multi-dap.exe shutdown --config C:\private\project.toml
```

`diagnose` returns a sanitized terminal classification rather than raw MULTI
output. For editor setup, continue with the VS Code or CLion guide referenced by
[Windows packaging and editor integration](m6-packaging-and-editors.md).
CLion is the primary supported IDE; follow the dedicated
[CLion configuration guide](clion.md) for the complete Cidr DAP setup.

## Confirm the boundary

Before using execution or inspection features, read the
[capability matrix](capabilities.md). In particular, multicore stepping,
register access, memory writes, `stepOut`, and several hardware-safety claims
remain unsupported or unverified.
