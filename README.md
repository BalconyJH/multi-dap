# multi-dap

A Debug Adapter Protocol backend for Green Hills MULTI.

Exposes a MULTI debug session over DAP so CLion, VS Code, Zed, and agent tooling can do
full source-level debugging of an embedded target — breakpoints, stepping, call stacks,
variables, memory — without the MULTI GUI.

Target-agnostic: device file, connection title, target server arguments, core-to-ELF
mapping, and source path rewriting all come from a project configuration file.

**Status: architecture accepted; MULTI binding contract pending M0.** No implementation has
started. Nine hardware reconnaissance blockers must be answered first — see
[docs/architecture.md](docs/architecture.md) §12.

## Shape

```
IDE ──DAP──▶ multi-dap (Go daemon) ──NDJSON/TCP──▶ bridge.py (Python 2.7)
                                                        │
                                                   MULTI-Python
                                                        ▼
                                            MULTI ▶ probe ▶ target
```

Go holds all debugger semantics. `bridge.py` is mechanism only. See
[docs/architecture.md](docs/architecture.md) for the layering rules and why they are
enforced in CI.
