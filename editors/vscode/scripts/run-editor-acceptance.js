'use strict';

const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { execFile, spawn } = require('node:child_process');

const extensionRoot = path.resolve(__dirname, '..');
const repository = path.resolve(extensionRoot, '..', '..');
const children = new Set();

function ownedProcess(command, args, options = {}) {
  const child = spawn(command, args, {
    cwd: repository, windowsHide: true, shell: false, stdio: ['pipe', 'pipe', 'pipe'], ...options,
  });
  children.add(child);
  child.once('close', () => children.delete(child));
  return child;
}

function completion(child, label, timeout = 120000, logFile) {
  return new Promise((resolve, reject) => {
    const output = logFile ? fs.createWriteStream(logFile, { flags: 'wx' }) : undefined;
    let settled = false;
    const done = (error) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      if (output) output.end();
      if (error) reject(error); else resolve();
    };
    const timer = setTimeout(() => {
      stopOwned(child).then(
        () => done(new Error(`${label} timed out`)),
        () => done(new Error(`${label} timed out; owned process cleanup failed`)),
      );
    }, timeout);
    for (const stream of [child.stdout, child.stderr]) {
      stream.on('data', (data) => {
        if (output) output.write(data);
      });
    }
    child.once('error', () => done(new Error(`${label} could not start`)));
    child.once('close', (code) => done(code === 0 ? undefined : new Error(`${label} exited with code ${code}`)));
  });
}

function exitedWithin(child, timeout) {
  if (!children.has(child)) return Promise.resolve(true);
  return new Promise((resolve) => {
    const onClose = () => {
      clearTimeout(timer);
      resolve(true);
    };
    const timer = setTimeout(() => {
      child.off('close', onClose);
      resolve(false);
    }, timeout);
    child.once('close', onClose);
  });
}

async function stopOwned(child) {
  if (!children.has(child) || child.exitCode !== null || child.signalCode !== null) return;
  child.stdin.end();
  if (await exitedWithin(child, 500)) return;
  if (process.platform === 'win32' && child.pid) {
    // This exact spawned root owns its isolated editor descendants.
    await new Promise((resolve) => {
      execFile('taskkill.exe', ['/PID', String(child.pid), '/T', '/F'], {
        windowsHide: true, shell: false, timeout: 10000, maxBuffer: 16384,
      }, () => resolve());
    });
  } else {
    child.kill('SIGKILL');
  }
  if (!await exitedWithin(child, 10000)) {
    throw new Error('An owned acceptance child did not exit');
  }
}

async function fixtureReady(child) {
  return new Promise((resolve, reject) => {
    let text = '';
    const timer = setTimeout(() => reject(new Error('synthetic daemon readiness timed out')), 10000);
    const finish = (error, value) => {
      clearTimeout(timer);
      child.stdout.off('data', receive);
      if (error) reject(error); else resolve(value);
    };
    const receive = (data) => {
      text += data.toString('utf8');
      if (text.length > 16384) return finish(new Error('synthetic readiness exceeds limit'));
      if (text.includes('\n')) {
        try {
          const value = JSON.parse(text.trim());
          if (value.synthetic !== true) throw new Error('unexpected readiness');
          finish(undefined, value);
        } catch {
          finish(new Error('synthetic readiness is invalid'));
        }
      }
    };
    child.stdout.on('data', receive);
    child.once('error', () => finish(new Error('synthetic daemon could not start')));
    child.once('exit', () => finish(new Error('synthetic daemon exited before readiness')));
  });
}

async function main() {
  const args = process.argv.slice(2);
  if (args.length !== 2 || args[0] !== '--code' || !path.isAbsolute(args[1]) || !fs.statSync(args[1]).isFile()) {
    throw new Error('Usage: npm run test:editor -- --code <absolute-Code-executable>');
  }
  fs.mkdirSync(path.join(repository, 'tmp'), { recursive: true });
  const output = fs.mkdtempSync(path.join(repository, 'tmp', 'vscode-editor-acceptance-'));
  const privateFixture = fs.mkdtempSync(path.join(os.tmpdir(), 'multi-dap-editor-fixture-'));
  const executable = path.join(output, process.platform === 'win32' ? 'multi-dap.exe' : 'multi-dap');
  const fixtureExecutable = path.join(output, process.platform === 'win32' ? 'fixture.exe' : 'fixture');
  await completion(ownedProcess('go', ['build', '-mod=readonly', '-o', executable, './cmd/multi-dap']), 'production build', 120000, path.join(output, 'build.log'));
  await completion(ownedProcess('go', ['build', '-mod=readonly', '-o', fixtureExecutable, './editors/vscode/acceptance/fixture']), 'fixture build', 120000, path.join(output, 'fixture-build.log'));
  const fixture = ownedProcess(fixtureExecutable, ['--root', path.join(privateFixture, 'fixture')]);
  fixture.stderr.pipe(fs.createWriteStream(path.join(output, 'fixture.log'), { flags: 'wx' }));
  const ready = await fixtureReady(fixture);
  const workspace = path.join(privateFixture, 'workspace');
  const userData = path.join(privateFixture, 'user-data');
  const extensions = path.join(privateFixture, 'extensions');
  const captures = path.join(privateFixture, 'captures');
  for (const directory of [workspace, path.join(userData, 'User'), extensions, captures]) {
    fs.mkdirSync(directory, { recursive: true });
  }
  fs.writeFileSync(path.join(userData, 'User', 'settings.json'), JSON.stringify({
    'multiDap.executable': executable,
    'telemetry.telemetryLevel': 'off',
    'workbench.startupEditor': 'none',
    'update.mode': 'none',
    'extensions.autoCheckUpdates': false,
    'extensions.autoUpdate': false,
    'chat.disableAIFeatures': true,
    'debug.openDebug': 'neverOpen',
  }, null, 2));
  const descriptor = path.join(output, 'descriptor.json');
  const report = path.join(output, 'result.json');
  fs.writeFileSync(descriptor, JSON.stringify({
    ...ready, output: captures, workspace, executable, report, bridgeScript: path.join(repository, 'bridge', 'bridge.py'),
  }));
  const env = { ...process.env, MULTI_DAP_EDITOR_ACCEPTANCE: descriptor };
  delete env.ELECTRON_RUN_AS_NODE;
  const editor = ownedProcess(args[1], [
    '--user-data-dir', userData, '--extensions-dir', extensions,
    '--extensionDevelopmentPath', extensionRoot,
    '--extensionTestsPath', path.join(extensionRoot, 'acceptance', 'extension-host.js'),
    '--disable-extensions', '--disable-workspace-trust', '--skip-welcome', '--skip-release-notes',
    '--disable-extension', 'GitHub.copilot', '--disable-extension', 'GitHub.copilot-chat',
    '--disable-extension', 'vscode.git', '--disable-extension', 'vscode.git-base',
    '--disable-updates', '--no-sandbox', '--new-window', workspace,
  ], { env });
  process.stdout.write(`Editor acceptance artifacts: ${output}\n`);
  await completion(editor, 'editor extension host', 150000, path.join(output, 'editor.log'));
  fixture.stdin.end();
  await completion(fixture, 'synthetic daemon shutdown', 10000);
  const result = JSON.parse(fs.readFileSync(report, 'utf8'));
  if (result.success !== true || result.syntheticBackend !== true || result.hardwareUsed !== false || result.runs.length !== 5) {
    throw new Error('Editor acceptance report is incomplete');
  }
  process.stdout.write(`EDITOR_ACCEPTANCE_RESULT ${JSON.stringify({
    success: result.success, vscodeVersion: result.vscodeVersion,
    syntheticBackend: result.syntheticBackend, hardwareUsed: result.hardwareUsed, runs: result.runs, report,
  })}\n`);
}

main().catch((error) => {
  process.stderr.write(`${error.message}\n`);
  process.exitCode = 1;
}).finally(async () => {
  const cleanup = await Promise.allSettled([...children].map(stopOwned));
  if (cleanup.some((result) => result.status === 'rejected')) {
    process.stderr.write('Owned acceptance process cleanup failed.\n');
    process.exitCode = 1;
  }
});
