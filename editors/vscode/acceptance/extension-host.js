'use strict';

const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { execFile } = require('node:child_process');
const { promisify } = require('node:util');
const vscode = require('vscode');

function deadline(promise, description, milliseconds = 20000) {
  let timer;
  return Promise.race([
    promise,
    new Promise((_, reject) => {
      timer = setTimeout(() => reject(new Error(`${description} timed out`)), milliseconds);
    }),
  ]).finally(() => clearTimeout(timer));
}

function nextSession(event, name) {
  let subscription;
  const promise = new Promise((resolve) => {
    subscription = event((session) => {
      if (session.name === name) {
        subscription.dispose();
        resolve(session);
      }
    });
  });
  return { promise, dispose: () => subscription.dispose() };
}

function fixtureEvents(file) {
  const text = fs.readFileSync(file, 'utf8');
  return text.split('\n').filter(Boolean).map((line) => JSON.parse(line));
}

function comparablePath(value) {
  return process.platform === 'win32' ? value.toLowerCase() : value;
}

async function waitForFile(file) {
  const started = Date.now();
  while (!fs.existsSync(file) || fs.statSync(file).size === 0) {
    if (Date.now() - started > 5000) {
      throw new Error('Proxy capture was not finalized');
    }
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
  return JSON.parse(fs.readFileSync(file, 'utf8'));
}

async function run() {
  const input = process.env.MULTI_DAP_EDITOR_ACCEPTANCE;
  assert.ok(input, 'isolated acceptance descriptor required');
  const fixture = JSON.parse(fs.readFileSync(input, 'utf8'));
  assert.equal(fixture.synthetic, true);
  assert.equal(vscode.workspace.workspaceFolders.length, 1);
  const workspace = vscode.workspace.workspaceFolders[0];
  assert.equal(comparablePath(workspace.uri.fsPath), comparablePath(fixture.workspace));
  const extension = vscode.extensions.getExtension('tacrolimus.multi-dap-vscode');
  assert.ok(extension, 'development extension must be installed');
  await extension.activate();
  assert.equal(extension.isActive, true);
  assert.equal(vscode.workspace.getConfiguration('multiDap').get('executable'), fixture.executable);
  const nativeCapture = require(path.join(extension.extensionPath, 'src', 'native_capture.js'));
  const extensionCode = require(path.join(extension.extensionPath, 'src', 'extension.js'));
  const nativePath = path.join(fixture.output, 'native-capture.json');
  const inputValues = ['17', 'editor-synthetic-source', '0', '', '0', '3'];
  let inputIndex = 0;
  const nativeController = nativeCapture.createController(vscode, {
    // Only prompt answers are synthetic. Capability/barrier tracking, helper
    // process, authenticated frontend, native requests and file writer are real.
    ui: {
      showQuickPick: async (items) => items.find((item) => item.sourceKind === 'live'),
      showInputBox: async (options) => {
        const value = inputValues[inputIndex++];
        assert.equal(options.validateInput(value), undefined);
        return value;
      },
      showWarningMessage: async () => 'Capture selected source',
      showSaveDialog: async () => vscode.Uri.file(nativePath),
      withProgress: vscode.window.withProgress.bind(vscode.window),
      showInformationMessage: vscode.window.showInformationMessage.bind(vscode.window),
    },
    sessionError(session, output) {
      return extensionCode.workspaceError(session.workspaceFolder) ||
        extensionCode.configurationError(output === undefined ? session.configuration : { ...session.configuration, perfOutput: output });
    },
    executableForSession: () => vscode.workspace.getConfiguration('multiDap').get('executable'),
  });
  const nativeTracker = vscode.debug.registerDebugAdapterTrackerFactory('multi-dap', {
    createDebugAdapterTracker: nativeController.tracker,
  });
  const nativeTermination = vscode.debug.onDidTerminateDebugSession(nativeController.terminate);
  const runs = [];
  let succeeded = false;
  let failure;
  const sessions = new Map();
  const tracker = vscode.debug.registerDebugAdapterTrackerFactory('multi-dap', {
    createDebugAdapterTracker(session) {
      const messages = [];
      sessions.set(session.name, messages);
      const capture = (direction, message) => {
        messages.push({
          direction, type: message.type, command: message.command, event: message.event,
          seq: message.seq, request_seq: message.request_seq, success: message.success,
        });
      };
      return {
        onWillReceiveMessage: (message) => capture('request', message),
        onDidSendMessage: (message) => capture('response', message),
      };
    },
  });
  try {
    for (const scenario of [
      { mode: 'attach' }, { mode: 'cold', acquisition: 'cold' },
      { mode: 'warm', acquisition: 'warm' }, { mode: 'capture', capture: true },
      { mode: 'native', native: true },
    ]) {
      const { mode } = scenario;
      const name = `Synthetic multi-dap ${mode}`;
      const before = fixtureEvents(fixture.eventsFile);
      const capturePath = path.join(fixture.output, `${mode}.trace.json`);
      const configuration = {
        name, type: 'multi-dap', request: 'attach',
        configFile: fixture.configFile, controlDirectory: fixture.controlDirectory,
      };
      if (scenario.capture) {
        configuration.perfOutput = capturePath;
        configuration.perfMaxEvents = 1000;
      }
      if (scenario.acquisition) {
        configuration.acquisition = scenario.acquisition;
        configuration.bridgeScript = fixture.bridgeScript;
      }
      if (mode === 'warm') {
        configuration.primaryElf = fixture.primaryElf;
      }
      const started = nextSession(vscode.debug.onDidStartDebugSession, name);
      let session;
      try {
        assert.equal(await deadline(vscode.debug.startDebugging(workspace, configuration), 'startDebugging'), true);
        session = await deadline(started.promise, 'debug session start');
      } finally {
        started.dispose();
      }
      try {
        const threads = await deadline(session.customRequest('threads'), 'threads');
        assert.deepEqual(threads.threads, [{ id: 1, name: 'Synthetic core' }]);
        const stack = await session.customRequest('stackTrace', { threadId: 1, startFrame: 0, levels: 1 });
        assert.equal(stack.totalFrames, 1);
        assert.equal(stack.stackFrames[0].name, 'synthetic_main');
        assert.equal(comparablePath(stack.stackFrames[0].source.path), comparablePath(fixture.sourceFile));
        const scopes = await session.customRequest('scopes', { frameId: stack.stackFrames[0].id });
        assert.equal(scopes.scopes[0].name, 'Locals');
        const variables = await session.customRequest('variables', { variablesReference: scopes.scopes[0].variablesReference });
        assert.equal(variables.variables[0].name, 'synthetic_value');
        assert.equal(variables.variables[0].value, '42');
        const evaluated = await session.customRequest('evaluate', {
          expression: 'synthetic_value', frameId: stack.stackFrames[0].id, context: 'watch',
        });
        assert.equal(evaluated.result, '42');
        if (scenario.native) {
          assert.equal(vscode.debug.activeDebugSession.id, session.id);
          const result = await deadline(nativeController.capture(), 'native interval capture', 30000);
          assert.equal(result.capture_written, true);
          assert.equal(result.native_disconnect_confirmed, true);
          assert.equal(result.needs_reconciliation, false);
          assert.equal(result.native_runtime_qualified, false);
          assert.equal(result.hardware_qualified, false);
          assert.equal(vscode.debug.activeDebugSession.id, session.id);
          assert.deepEqual((await session.customRequest('threads')).threads, threads.threads);
          assert.equal((await session.customRequest('evaluate', {
            expression: 'synthetic_value', frameId: 1, context: 'watch',
          })).result, '42');
          const capture = JSON.parse(fs.readFileSync(nativePath, 'utf8'));
          assert.equal(capture.schema, 'multi-dap.native-capture.v1');
          assert.equal(capture.generations[0].id, '18446744073709551614');
          assert.equal(capture.generations[0].packets_hex.length, 3);
          const normalized = path.join(fixture.output, 'native-normalized.json');
          const { stdout } = await promisify(execFile)(fixture.executable, [
            'trace', 'normalize', '--input', nativePath, '--output', normalized,
          ], { windowsHide: true, shell: false, timeout: 10000, maxBuffer: 16384 });
          assert.equal(JSON.parse(stdout).event, 'native_trace_normalized');
          const report = JSON.parse(fs.readFileSync(normalized, 'utf8'));
          assert.equal(report.statistics.packets, 3);
          assert.equal(report.statistics.completed_function_spans, 1);
          assert.equal(report.spans[0].elapsed, '100000000');
          assert.equal(report.native_runtime_qualified, false);
          assert.equal(report.hardware_qualified, false);
        }
      } finally {
        const terminated = nextSession(vscode.debug.onDidTerminateDebugSession, name);
        try {
          await deadline(vscode.debug.stopDebugging(session), 'stopDebugging');
          await deadline(terminated.promise, 'debug session termination');
        } finally {
          terminated.dispose();
        }
      }
      let traceEvents = 0;
      if (scenario.capture) {
        const trace = await waitForFile(capturePath);
        assert.ok(Array.isArray(trace.traceEvents) && trace.traceEvents.length > 0);
        assert.equal(trace.otherData.schema, 'multi-dap.host-trace.v1');
        assert.equal(trace.otherData.target_cpu_timing, false);
        assert.equal(trace.otherData.framing_errors, 0);
        assert.equal(trace.otherData.unfinished_requests, 0);
        assert.equal(trace.otherData.proxy_failed, false);
        assert.equal(trace.otherData.capture_complete, true);
        assert.equal(trace.otherData.finalization_boundary, 'disconnect_response');
        assert.equal(trace.otherData.proxy_exit_observed, false);
        traceEvents = trace.traceEvents.length;
      } else {
        assert.equal(fs.existsSync(capturePath), false, 'ordinary attach must not create a capture');
      }
      const after = fixtureEvents(fixture.eventsFile);
      const fresh = after.slice(before.length);
      assert.equal(fresh.filter((event) => event.event === 'proxy').length, 1);
      const statusRequests = fresh.filter((event) => event.event === 'status').length;
      assert.equal(statusRequests, scenario.acquisition ? 1 : 0, 'only explicit acquisition probes daemon readiness');
      const messages = sessions.get(name);
      for (const command of ['initialize', 'attach', 'configurationDone', 'threads', 'stackTrace', 'scopes', 'variables', 'evaluate', 'disconnect']) {
        assert.ok(messages.some((message) => message.type === 'request' && message.command === command), `missing editor request ${command}`);
        assert.ok(messages.some((message) => message.type === 'response' && message.command === command && message.success === true), `missing successful response ${command}`);
      }
      assert.ok(messages.some((message) => message.event === 'initialized'));
      assert.ok(messages.some((message) => message.event === 'stopped'));
      const initialize = messages.findIndex((message) => message.type === 'request' && message.command === 'initialize');
      const attach = messages.findIndex((message) => message.type === 'request' && message.command === 'attach');
      const configured = messages.findIndex((message) => message.type === 'request' && message.command === 'configurationDone');
      assert.ok(initialize < attach && attach < configured);
      if (scenario.native) {
        const nativeRequests = messages.filter((message) => message.type === 'request' && message.command?.startsWith('multi-dap/trace/'));
        assert.deepEqual(nativeRequests.map((message) => message.command), ['status', 'open', 'poll', 'read', 'close'].map((operation) => `multi-dap/trace/${operation}`));
        assert.equal(messages.filter((message) => message.type === 'request' && message.command === 'initialize').length, 1);
        assert.equal(messages.filter((message) => message.type === 'request' && message.command === 'disconnect').length, 1);
      }
      runs.push({
        mode, authenticatedProxyConnections: 1, readinessRequests: statusRequests, traceEvents,
        requests: messages.filter((message) => message.type === 'request').map((message) => message.command),
      });
    }
    succeeded = true;
  } catch (error) {
    failure = error.message;
    throw error;
  } finally {
    tracker.dispose();
    nativeTracker.dispose();
    nativeTermination.dispose();
    nativeController.dispose();
    fs.writeFileSync(fixture.report, JSON.stringify({
      schemaVersion: 1, success: succeeded, syntheticBackend: true, hardwareUsed: false,
      vscodeVersion: vscode.version, extensionActive: extension.isActive, runs, failure,
      observedSessions: Object.fromEntries(sessions),
    }, null, 2));
  }
}

module.exports = { run };
