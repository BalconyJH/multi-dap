# Zed Extension Dependency Notices

The multi-dap extension source is licensed under the repository's MIT License.
Its Rust dependencies are separate upstream packages. The following metadata
comes from `cargo metadata --locked --offline --format-version 1` for the
checked-in `Cargo.lock`:

| Package | Locked version | Declared license | Role |
|---|---|---|---|
| `zed_extension_api` | `0.7.0` | `Apache-2.0` | Zed extension SDK and registration bindings. |
| `serde` | `1.0.229` | `MIT OR Apache-2.0` | Strict configuration decoding. |
| `serde_json` | `1.0.154` | `MIT OR Apache-2.0` | JSON support re-exported by the SDK. |
| `toml` | `0.9.12+spec-1.1.0` | `MIT OR Apache-2.0` | Development-only manifest validation tests. |

`Cargo.lock` records exact versions and registry checksums for the complete
dependency graph. Cargo's downloaded package directories contain the upstream
license texts; the SDK package includes `LICENSE-APACHE`. The dependency
metadata can be regenerated using the command above when the lockfile changes.

This source extension and its compiled Wasm artifact are excluded from the
multi-dap Windows ZIP. No Zed registry publication or compiled-extension
distribution is performed by this repository's Windows package workflow.
These notes describe the source/build dependency inventory; they are not a
claim that a separately distributed Wasm bundle has completed release review.
