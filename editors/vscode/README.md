# multi-dap VS Code integration

This extension supports connections only to an **already running** local `multi-dap` daemon. It registers
a configuration provider and `DebugAdapterDescriptorFactory` for the `multi-dap` debug type, and invokes the supported:

```text
multi-dap proxy --config <absolute-project.toml> [--control-dir <absolute-directory>]
```

`proxy` first authenticates the daemon through its private control record, then relays DAP bytes between
standard input and output. The extension does not read, log, or pass control tokens, target-server arguments,
or router connection arguments.

## Usage

1. Start the daemon from a controlled terminal, for example: `multi-dap serve --config C:\\work\\project.toml`.
2. Install this extension and ensure that `multiDap.executable` points to the local `multi-dap` executable
   (the default is `multi-dap`, resolved through `PATH`).
3. Create `.vscode/launch.json`:

```json
{
  "version": "0.2.0",
  "configurations": [
    {
      "name": "Attach to multi-dap",
      "type": "multi-dap",
      "request": "attach",
      "configFile": "${workspaceFolder}/project.toml"
    }
  ]
}
```

After variable substitution, `configFile` must be an absolute path; if supplied, `controlDirectory` must
also be absolute. The extension rejects `launch`, `host`, `port`, and `debugServer` to prevent bypassing the
authenticated local proxy. It always runs in the local UI extension host, so both the TOML file and executable
must be locally accessible; remote workspaces are not currently supported.

If `serve` fails, the daemon is absent, or the control record cannot be authenticated, `proxy` exits and
VS Code reports a startup error. The extension does not attempt to cold- or warm-start `serve` itself because
the current CLI requires the user to explicitly provide session inputs, bridge paths, and lifecycle choices.

## Validation and release contents

```powershell
npm test
npm run lint
npm run package-contents
```

The last command lists the three files required for publishing; `.vscodeignore` excludes tests, scripts,
dependency directories, and common temporary packages.
