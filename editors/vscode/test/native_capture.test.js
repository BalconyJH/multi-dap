'use strict';

const assert = require('node:assert/strict');
const { EventEmitter } = require('node:events');
const path = require('node:path');
const { PassThrough, Writable } = require('node:stream');
const test = require('node:test');
const native = require('../src/native_capture');

const prefix = 'multi-dap/trace/';
const captureId = '0123456789abcdef0123456789abcdef';
const output = path.resolve('private capture & $literal.json');

function receipt(operation = '', outcome = '', closed = false) {
  return {
    operation, outcome, native_state_uncertain: false, helper_abort_requested: false,
    native_disconnect_confirmed: closed, collection_may_continue: operation !== '',
  };
}

function status(state = 'idle') {
  return {
    handle: state === 'idle' ? 0 : 7, state, busy: false,
    native_generation: state === 'idle' ? '0' : '18446744073709551614',
    packet_count: '3', packet_count_known: true, timebase: 'picoseconds',
    trace_state: 'unknown', source_binding: 'operator_declared', needs_reconciliation: false,
    native_runtime_qualified: false, hardware_qualified: false,
    last_receipt: state === 'closed' ? receipt('close', 'closed', true) : receipt(),
  };
}

function nativeReply(command) {
  const operation = command.slice(prefix.length);
  if (operation === 'status') return { status: status() };
  if (operation === 'close') return { status: status('closed'), receipt: receipt('close', 'closed', true) };
  return {
    status: status('open'), receipt: receipt(operation === 'poll' ? 'status' : operation, operation === 'read' ? 'read' : 'observed'),
    ...(operation === 'read' ? { batch: { generations: [{ id: '18446744073709551614', first_index: '0' }] } } : {}),
  };
}

function input() {
  return {
    sourceKind: 'live', sessionId: 17, sourceId: 'synthetic-source', coreId: 0,
    start: '0', count: 3, acknowledgeUnverifiedSource: true, output,
  };
}

function terminal(success = true, overrides = {}) {
  return {
    protocol: native.PROTOCOL, type: 'result', captureId,
    capture_written: success, native_attempted: true, native_disconnect_confirmed: success,
    needs_reconciliation: !success, collection_may_continue: true,
    native_runtime_qualified: false, hardware_qualified: false,
    failure_stage: success ? 'none' : 'open', ...overrides,
  };
}

class FakeChild extends EventEmitter {
  constructor(onResponse, onEOF) {
    super();
    this.stdout = new PassThrough();
    this.stderr = new PassThrough();
    this.responses = [];
    this.kills = 0;
    this.exitCode = null;
    this.stdin = new Writable({
      write: (chunk, _, done) => {
        try {
          const response = JSON.parse(chunk.toString());
          this.responses.push(response);
          onResponse?.(response, this);
          done();
        } catch (error) {
          done(error);
        }
      },
    });
    this.stdin.on('finish', () => onEOF?.(this));
  }
  send(value) { this.stdout.write(typeof value === 'string' || Buffer.isBuffer(value) ? value : JSON.stringify(value) + '\n'); }
  close(code = 0) {
    if (this.exitCode !== null) return;
    this.exitCode = code;
    this.stdout.end();
    this.stderr.end();
    this.emit('close', code);
  }
  kill() {
    this.kills++;
    setImmediate(() => this.close(1));
    return true;
  }
}

function successChild(parameters, alter) {
  let id = 0;
  const request = (operation, args) => {
    const value = {
      protocol: native.PROTOCOL, type: 'request', captureId,
      id: ++id, command: prefix + operation, arguments: args,
    };
    child.send(alter ? alter(value) : value);
  };
  const child = new FakeChild((response) => {
    if (!response.success) {
      child.errorSent = true;
      child.send(terminal(false));
      setImmediate(() => child.close(1));
      return;
    }
    switch (response.command) {
      case prefix + 'status':
        request('open', {
          sourceKind: parameters.sourceKind, sessionId: parameters.sessionId, sourceId: parameters.sourceId,
          acknowledgeUnverifiedSource: true, coreId: parameters.coreId,
          ...(parameters.imageSha256 ? { imageSha256: parameters.imageSha256 } : {}),
          ...(parameters.snapshotSha256 ? { snapshotSha256: parameters.snapshotSha256 } : {}),
        });
        break;
      case prefix + 'open': request('poll', { handle: 7 }); break;
      case prefix + 'poll':
        request('read', { handle: 7, start: parameters.start, generation: '18446744073709551614', count: parameters.count });
        break;
      case prefix + 'read': request('close', { handle: 7 }); break;
    }
  }, () => {
    if (child.errorSent) return;
    child.send(terminal());
    setImmediate(() => child.close());
  });
  setImmediate(() => {
    child.send({ protocol: native.PROTOCOL, type: 'ready', captureId });
    request('status', {});
  });
  return child;
}

function harness(overrides = {}) {
  const events = new EventEmitter();
  let cancelled = false;
  const uiCalls = [];
  const parameters = overrides.input || input();
  const values = [
    String(parameters.sessionId), parameters.sourceId,
    parameters.coreId === undefined ? '' : String(parameters.coreId), parameters.imageSha256 || '',
    ...(parameters.sourceKind === 'snapshot' ? [parameters.snapshotSha256] : []),
    parameters.start, String(parameters.count),
  ];
  let promptIndex = 0;
  const calls = [];
  const children = [];
  const spawnCalls = [];
  const ui = {
    async showQuickPick(items) {
      uiCalls.push('source');
      if (overrides.cancelAt === 'source') return undefined;
      assert.ok(items.every((item) => !Object.hasOwn(item, 'kind')));
      return items.find((item) => item.sourceKind === parameters.sourceKind);
    },
    async showInputBox(options) {
      const index = promptIndex++;
      uiCalls.push(index);
      if (overrides.cancelAt === index) return undefined;
      const value = values[index % values.length];
      assert.equal(options.validateInput(value), undefined);
      return value;
    },
    async showWarningMessage() {
      uiCalls.push('ack');
      return overrides.cancelAt === 'ack' ? undefined : 'Capture selected source';
    },
    async showSaveDialog() {
      uiCalls.push('output');
      return overrides.cancelAt === 'output' ? undefined : { scheme: 'file', fsPath: output };
    },
    async withProgress(_, action) {
      return action({}, {
        get isCancellationRequested() { return cancelled; },
        onCancellationRequested(callback) {
          events.on('cancel', callback);
          return { dispose: () => events.off('cancel', callback) };
        },
      });
    },
    async showInformationMessage(text) { uiCalls.push(text); },
  };
  const session = {
    id: 'session-1', type: 'multi-dap',
    configuration: { type: 'multi-dap', request: 'attach', configFile: path.resolve('project.toml'), controlDirectory: path.resolve('control') },
    async customRequest(command, args) {
      calls.push({ command, args });
      return overrides.customRequest ? overrides.customRequest(command, args) : nativeReply(command);
    },
  };
  const vscode = { debug: { activeDebugSession: session }, window: ui, ProgressLocation: { Notification: 15 } };
  const controller = native.createController(vscode, {
    ui, randomId: () => captureId,
    requestTimeoutMs: overrides.requestTimeoutMs || 1000, operationTimeoutMs: overrides.operationTimeoutMs || 3000, reapTimeoutMs: 100,
    sessionError: () => undefined, executableForSession: () => path.resolve('bin with spaces', 'multi-dap.exe'),
    spawnChild(command, args, options) {
      spawnCalls.push({ command, args, options });
      const child = overrides.child ? overrides.child(parameters) : successChild(parameters, overrides.alter);
      children.push(child);
      return child;
    },
  });
  const tracker = controller.tracker(session);
  tracker.onWillReceiveMessage({ type: 'request', command: 'initialize', seq: 1 });
  tracker.onDidSendMessage({ type: 'response', command: 'initialize', request_seq: 1, success: true, body: { multiDapSupportsNativeTraceRequests: true } });
  tracker.onWillReceiveMessage({ type: 'request', command: 'configurationDone', seq: 2 });
  tracker.onDidSendMessage({ type: 'response', command: 'configurationDone', request_seq: 2, success: true });
  return {
    controller, session, tracker, vscode, calls, children, spawnCalls, uiCalls,
    cancel() { cancelled = true; events.emit('cancel'); },
  };
}

async function until(predicate) {
  const end = Date.now() + 1000;
  while (!predicate()) {
    if (Date.now() > end) throw new Error('fixture did not reach its expected point');
    await new Promise((resolve) => setTimeout(resolve, 1));
  }
}

test('capture relays one bounded interval through the original session and literal helper argv', async () => {
  const h = harness();
  const result = await h.controller.capture();
  assert.equal(result.capture_written, true);
  assert.deepEqual(h.calls.map((call) => call.command), ['status', 'open', 'poll', 'read', 'close'].map((name) => prefix + name));
  assert.equal(h.calls[3].args.generation, '18446744073709551614');
  const launch = h.spawnCalls[0];
  assert.deepEqual(launch.options, { windowsHide: true, shell: false, stdio: ['pipe', 'pipe', 'pipe'] });
  assert.deepEqual(launch.args, [
    'trace', 'capture-editor', '--config', h.session.configuration.configFile, '--capture-id', captureId,
    '--output', output, '--source-kind', 'live', '--session-id', '17', '--source-id', 'synthetic-source',
    '--start', '0', '--count', '3', '--acknowledge-unverified-source',
    '--control-dir', h.session.configuration.controlDirectory, '--core-id', '0',
  ]);
  assert.equal(h.children[0].responses.length, 5);
  assert.equal(h.children[0].stdin.writableFinished, true);
  h.controller.dispose();
});

test('admission requires the active original session, capability and configuration response', async () => {
  for (const mode of ['absent', 'type', 'capability', 'configuration', 'uncorrelated']) {
    const h = harness();
    if (mode === 'absent') h.vscode.debug.activeDebugSession = undefined;
    if (mode === 'type') h.session.type = 'other';
    if (mode === 'capability') {
      h.tracker.onWillReceiveMessage({ type: 'request', command: 'initialize', seq: 3 });
      h.tracker.onDidSendMessage({ type: 'response', command: 'initialize', request_seq: 3, success: true, body: {} });
    }
    if (mode === 'configuration' || mode === 'uncorrelated') {
      h.tracker = h.controller.tracker(h.session);
      h.tracker.onDidSendMessage({ type: 'response', command: 'configurationDone', request_seq: 999, success: true });
    }
    await assert.rejects(h.controller.capture(), (error) => error.code === 'admission');
    assert.equal(h.spawnCalls.length, 0);
    h.controller.dispose();
  }
});

test('every cancelled input and missing acknowledgement prevents helper and native requests', async () => {
  for (const cancelAt of ['source', 0, 1, 2, 3, 4, 5, 'ack', 'output']) {
    const h = harness({ cancelAt });
    assert.equal(await h.controller.capture(), undefined);
    assert.equal(h.spawnCalls.length, 0);
    assert.equal(h.calls.length, 0);
    h.controller.dispose();
  }
});

test('snapshot selection requires an explicit digest and preserves optional source fields', async () => {
  const parameters = {
    ...input(), sourceKind: 'snapshot', coreId: undefined, imageSha256: '1'.repeat(64),
    snapshotSha256: '2'.repeat(64), start: '18446744073709551615', count: 1,
  };
  for (const cancelAt of ['source', 0, 1, 2, 3, 4, 5, 6, 'ack', 'output']) {
    const h = harness({ input: parameters, cancelAt });
    assert.equal(await h.controller.capture(), undefined);
    assert.equal(h.spawnCalls.length, 0);
    assert.equal(h.calls.length, 0);
    h.controller.dispose();
  }
  const h = harness({ input: parameters });
  assert.equal((await h.controller.capture()).capture_written, true);
  assert.equal(h.spawnCalls[0].args.includes('--core-id'), false);
  assert.ok(h.spawnCalls[0].args.includes('--snapshot-sha256'));
  assert.deepEqual(h.calls[1].args, {
    sourceKind: 'snapshot', sessionId: 17, sourceId: 'synthetic-source',
    acknowledgeUnverifiedSource: true, imageSha256: '1'.repeat(64), snapshotSha256: '2'.repeat(64),
  });
  assert.equal(h.calls[3].args.start, '18446744073709551615');
  h.controller.dispose();
});

test('late lifecycle responses from an earlier configuration cannot admit capture', async () => {
  const h = harness();
  h.tracker.onWillReceiveMessage({ type: 'request', command: 'initialize', seq: 3 });
  h.tracker.onWillReceiveMessage({ type: 'request', command: 'configurationDone', seq: 4 });
  h.tracker.onWillReceiveMessage({ type: 'request', command: 'initialize', seq: 5 });
  h.tracker.onDidSendMessage({ type: 'response', command: 'initialize', request_seq: 3, success: true, body: { multiDapSupportsNativeTraceRequests: true } });
  h.tracker.onDidSendMessage({ type: 'response', command: 'configurationDone', request_seq: 4, success: true });
  await assert.rejects(h.controller.capture(), (error) => error.code === 'admission');
  assert.equal(h.spawnCalls.length, 0);
  h.controller.dispose();
});

test('strict envelopes reject duplicate escaped keys, malformed UTF-8, arrays and excess bytes', () => {
  for (const bytes of [
    Buffer.from('{"type":"ready","\\u0074ype":"request"}'),
    Buffer.from([123, 34, 120, 34, 58, 34, 255, 34, 125]),
    Buffer.from('[]'), Buffer.alloc(native.MAX_LINE + 1, 32), Buffer.from('\ufeff{}'),
  ]) assert.throws(() => native.decodeFrame(bytes), native.CaptureError);
  assert.deepEqual(native.decodeFrame(Buffer.from('{"body":{"repeated":1,"repeated":2},"type":"response"}')), {
    body: { repeated: 2 }, type: 'response',
  });
});

test('helper cannot select another source, nonce, sequence, command or ordinary DAP operation', async () => {
  for (const mutation of [
    (frame) => ({ ...frame, captureId: 'f'.repeat(32) }),
    (frame) => ({ ...frame, id: 0 }),
    (frame) => ({ ...frame, extra: true }),
    (frame) => ({ ...frame, command: prefix + 'start' }),
    (frame) => ({ ...frame, command: 'disconnect' }),
    (frame) => frame.command.endsWith('/open') ? { ...frame, arguments: { ...frame.arguments, sessionId: 18 } } : frame,
  ]) {
    const h = harness({ alter: mutation });
    await assert.rejects(h.controller.capture(), (error) => error.code === 'protocol');
    assert.equal(h.calls.some((call) => ![prefix + 'status'].includes(call.command)), false);
    h.controller.dispose();
  }
});

test('split output lines relay correctly and a contradictory success receipt is rejected', async () => {
  const h = harness({
    child(parameters) {
      const child = successChild(parameters);
      const send = child.send.bind(child);
      child.send = (value) => {
        const text = typeof value === 'string' ? value : JSON.stringify(value) + '\n';
        send(text.slice(0, 7));
        send(text.slice(7));
      };
      return child;
    },
  });
  assert.equal((await h.controller.capture()).capture_written, true);
  h.controller.dispose();
  const malformed = harness({
    child() {
      const child = new FakeChild();
      setImmediate(() => {
        child.send({ protocol: native.PROTOCOL, type: 'ready', captureId });
        child.send(terminal());
      });
      return child;
    },
  });
  await assert.rejects(malformed.controller.capture(), (error) => error.code === 'protocol');
  assert.equal(malformed.calls.length, 0);
  malformed.controller.dispose();
});

test('DAP rejection and helper stderr are never echoed into protocol or UI errors', async () => {
  const h = harness({
    customRequest(command) {
      if (command.endsWith('/open')) throw new Error('PRIVATE_NATIVE_PATH_AND_MEMORY');
      return nativeReply(command);
    },
    child(parameters) {
      const child = successChild(parameters);
      setImmediate(() => child.stderr.write('PRIVATE_HELPER_PATH'));
      return child;
    },
  });
  const result = await h.controller.capture();
  assert.equal(result.capture_written, false);
  assert.equal(result.needs_reconciliation, true);
  assert.equal(JSON.stringify(h.children[0].responses).includes('PRIVATE_'), false);
  await assert.rejects(h.controller.capture(), (error) => error.code === 'reconciliation' && !error.message.includes('PRIVATE_'));
  h.controller.dispose();
});

test('cancellation fences late open and cannot clear reconciliation while customRequest remains pending', async () => {
  let resolveOpen;
  let reconciled = false;
  const h = harness({
    customRequest(command) {
      if (command.endsWith('/open')) return new Promise((resolve) => { resolveOpen = resolve; });
      if (reconciled && command.endsWith('/status')) return { status: status('closed') };
      return nativeReply(command);
    },
  });
  const operation = h.controller.capture();
  await until(() => resolveOpen);
  h.cancel();
  await assert.rejects(operation, (error) => error.code === 'cancelled');
  await assert.rejects(h.controller.inspectStatus(), (error) => error.code === 'busy');
  resolveOpen(nativeReply(prefix + 'open'));
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(h.children[0].responses.some((response) => response.command === prefix + 'open'), false);
  await assert.rejects(h.controller.capture(), (error) => error.code === 'reconciliation');
  const notClean = await h.controller.inspectStatus();
  // An idle initial-state response is authoritative only after the late RPC settles.
  assert.equal(notClean.needs_reconciliation, false);
  reconciled = true;
  assert.equal((await h.controller.inspectStatus()).needs_reconciliation, false);
  assert.equal(h.calls.some((call) => call.command === prefix + 'close'), false);
  h.controller.dispose();
});

test('request timeout preserves pending-operation exclusion until a late request settles', async () => {
  let resolveOpen;
  const h = harness({
    requestTimeoutMs: 10,
    customRequest(command) {
      if (command.endsWith('/open')) return new Promise((resolve) => { resolveOpen = resolve; });
      return nativeReply(command);
    },
  });
  await assert.rejects(h.controller.capture(), (error) => error.code === 'timeout');
  await assert.rejects(h.controller.inspectStatus(), (error) => error.code === 'busy');
  resolveOpen(nativeReply(prefix + 'open'));
  await new Promise((resolve) => setImmediate(resolve));
  await assert.rejects(h.controller.capture(), (error) => error.code === 'reconciliation');
  h.controller.dispose();
});

test('a failed terminal receipt fences a request that outlives the Go helper deadline', async () => {
  let resolveOpen;
  const h = harness({
    customRequest(command) {
      if (command.endsWith('/open')) return new Promise((resolve) => { resolveOpen = resolve; });
      return nativeReply(command);
    },
    child(parameters) {
      const child = successChild(parameters);
      const send = child.send.bind(child);
      child.send = (frame) => {
        send(frame);
        if (frame.command === prefix + 'open') {
          setImmediate(() => {
            child.errorSent = true;
            send(terminal(false, { failure_stage: 'transport' }));
            child.close(1);
          });
        }
      };
      return child;
    },
  });
  const result = await h.controller.capture();
  assert.equal(result.needs_reconciliation, true);
  await assert.rejects(h.controller.inspectStatus(), (error) => error.code === 'busy');
  resolveOpen(nativeReply(prefix + 'open'));
  await new Promise((resolve) => setImmediate(resolve));
  await assert.rejects(h.controller.capture(), (error) => error.code === 'reconciliation');
  h.controller.dispose();
});

test('switching active sessions never redirects an established capture', async () => {
  const h = harness({
    customRequest(command) {
      if (command.endsWith('/open')) h.vscode.debug.activeDebugSession = { id: 'other', type: 'multi-dap', customRequest() { throw new Error('redirected'); } };
      return nativeReply(command);
    },
  });
  assert.equal((await h.controller.capture()).capture_written, true);
  assert.equal(h.calls.length, 5);
  h.controller.dispose();
});

test('session termination and extension disposal stop only the owned helper', async () => {
  for (const terminate of [true, false]) {
    let resolveOpen;
    const h = harness({
      customRequest(command) {
        if (command.endsWith('/open')) return new Promise((resolve) => { resolveOpen = resolve; });
        return nativeReply(command);
      },
    });
    const operation = h.controller.capture();
    await until(() => resolveOpen);
    if (terminate) h.controller.terminate(h.session); else h.controller.dispose();
    await assert.rejects(operation, (error) => error.code === 'session');
    assert.equal(h.children[0].kills, 1);
    resolveOpen(nativeReply(prefix + 'open'));
    await new Promise((resolve) => setImmediate(resolve));
    assert.equal(h.calls.some((call) => ['disconnect', prefix + 'stop', prefix + 'close'].includes(call.command)), false);
    h.controller.dispose();
  }
});

test('tracker replacement retains uncertainty and pending exclusion across the same session ID', async () => {
  let resolveOpen;
  let recovered = false;
  let opens = 0;
  const h = harness({
    customRequest(command) {
      if (command.endsWith('/open') && ++opens === 1) return new Promise((resolve) => { resolveOpen = resolve; });
      if (command.endsWith('/status') && opens !== 0) return { status: status(recovered ? 'closed' : 'open') };
      return nativeReply(command);
    },
  });
  const old = h.controller.capture();
  await until(() => resolveOpen);
  const replacement = h.controller.tracker(h.session);
  replacement.onWillReceiveMessage({ type: 'request', command: 'initialize', seq: 1 });
  replacement.onDidSendMessage({ type: 'response', command: 'initialize', request_seq: 1, success: true, body: { multiDapSupportsNativeTraceRequests: true } });
  replacement.onWillReceiveMessage({ type: 'request', command: 'configurationDone', seq: 2 });
  replacement.onDidSendMessage({ type: 'response', command: 'configurationDone', request_seq: 2, success: true });
  h.tracker.onExit();
  await assert.rejects(old, (error) => error.code === 'session');
  await assert.rejects(h.controller.capture(), (error) => error.code === 'busy');
  await assert.rejects(h.controller.inspectStatus(), (error) => error.code === 'busy');
  resolveOpen(nativeReply(prefix + 'open'));
  await new Promise((resolve) => setImmediate(resolve));
  await assert.rejects(h.controller.capture(), (error) => error.code === 'reconciliation');
  assert.equal((await h.controller.inspectStatus()).needs_reconciliation, true);
  await assert.rejects(h.controller.capture(), (error) => error.code === 'reconciliation');
  recovered = true;
  assert.equal((await h.controller.inspectStatus()).needs_reconciliation, false);
  assert.equal((await h.controller.capture()).capture_written, true);
  h.controller.dispose();
});

test('reconfiguration cancels an old capture even if the same tracker becomes ready again', async () => {
  let resolveOpen;
  const h = harness({
    customRequest(command) {
      if (command.endsWith('/open')) return new Promise((resolve) => { resolveOpen = resolve; });
      return nativeReply(command);
    },
  });
  const operation = h.controller.capture();
  await until(() => resolveOpen);
  h.tracker.onWillReceiveMessage({ type: 'request', command: 'configurationDone', seq: 3 });
  h.tracker.onDidSendMessage({ type: 'response', command: 'configurationDone', request_seq: 3, success: true });
  await assert.rejects(operation, (error) => error.code === 'session');
  await assert.rejects(h.controller.capture(), (error) => error.code === 'busy');
  resolveOpen(nativeReply(prefix + 'open'));
  await new Promise((resolve) => setImmediate(resolve));
  await assert.rejects(h.controller.capture(), (error) => error.code === 'reconciliation');
  h.controller.dispose();
});

test('an unreaped helper blocks capture and status even after bounded cancellation returns', async () => {
  const h = harness({
    child() {
      const child = new FakeChild();
      child.kill = () => { child.kills++; return true; };
      return child;
    },
  });
  const operation = h.controller.capture();
  await until(() => h.children.length > 0);
  h.cancel();
  await assert.rejects(operation, (error) => error.code === 'cancelled');
  assert.equal(h.children[0].kills, 2);
  await assert.rejects(h.controller.capture(), (error) => error.code === 'busy');
  await assert.rejects(h.controller.inspectStatus(), (error) => error.code === 'busy');
  h.children[0].close(1);
  await assert.rejects(h.controller.capture(), (error) => error.code === 'reconciliation');
  assert.equal((await h.controller.inspectStatus()).needs_reconciliation, false);
  h.controller.dispose();
});

test('status started before a new configuration cannot clear the reconciliation tombstone', async () => {
  let resolveOpen;
  let resolveStatus;
  let inspection = false;
  const h = harness({
    customRequest(command) {
      if (command.endsWith('/open')) return new Promise((resolve) => { resolveOpen = resolve; });
      if (command.endsWith('/status') && inspection) return new Promise((resolve) => { resolveStatus = resolve; });
      return nativeReply(command);
    },
  });
  const operation = h.controller.capture();
  await until(() => resolveOpen);
  h.cancel();
  await assert.rejects(operation, (error) => error.code === 'cancelled');
  resolveOpen(nativeReply(prefix + 'open'));
  await new Promise((resolve) => setImmediate(resolve));
  inspection = true;
  const statusRequest = h.controller.inspectStatus();
  await until(() => resolveStatus);
  h.tracker.onWillReceiveMessage({ type: 'request', command: 'configurationDone', seq: 3 });
  h.tracker.onDidSendMessage({ type: 'response', command: 'configurationDone', request_seq: 3, success: true });
  resolveStatus(nativeReply(prefix + 'status'));
  await assert.rejects(statusRequest, (error) => error.code === 'transport');
  await assert.rejects(h.controller.capture(), (error) => error.code === 'reconciliation');
  h.controller.dispose();
});

test('oversized lines, arguments, responses and stderr fail without exposing payloads', async () => {
  for (const mode of ['line', 'arguments', 'body', 'stderr']) {
    const h = harness({
      customRequest(command) {
        return mode === 'body' ? { status: status(), data: 'PRIVATE'.repeat(native.MAX_BODY) } : nativeReply(command);
      },
      child(parameters) {
        if (mode === 'body') return successChild(parameters);
        const child = new FakeChild();
        setImmediate(() => {
          if (mode === 'stderr') {
            child.stderr.write(Buffer.alloc(16385, 65));
            return;
          }
          if (mode === 'line') {
            child.send(Buffer.alloc(native.MAX_LINE + 1, 65));
            return;
          }
          child.send({ protocol: native.PROTOCOL, type: 'ready', captureId });
          child.send({
            protocol: native.PROTOCOL, type: 'request', captureId, id: 1,
            command: prefix + 'status', arguments: { private: 'A'.repeat(native.MAX_ARGUMENTS) },
          });
        });
        return child;
      },
    });
    await assert.rejects(h.controller.capture(), (error) => error.code === 'protocol' && !error.message.includes('PRIVATE'));
    assert.equal(h.calls.some((call) => call.command === prefix + 'open'), false);
    h.controller.dispose();
  }
});

test('request floods and trailing terminal frames cannot pass a success result', async () => {
  const flooded = harness({
    child() {
      let id = 0;
      const send = () => child.send({
        protocol: native.PROTOCOL, type: 'request', captureId, id: ++id,
        command: prefix + 'status', arguments: {},
      });
      const child = new FakeChild(() => send());
      setImmediate(() => {
        child.send({ protocol: native.PROTOCOL, type: 'ready', captureId });
        send();
      });
      return child;
    },
  });
  await assert.rejects(flooded.controller.capture(), (error) => error.code === 'protocol');
  assert.ok(flooded.calls.length <= native.MAX_FRAMES / 2);
  flooded.controller.dispose();
  const trailing = harness({
    child(parameters) {
      const child = successChild(parameters);
      const send = child.send.bind(child);
      child.send = (frame) => frame.type === 'result'
        ? send(`${JSON.stringify(frame)}\n${JSON.stringify(frame)}\n`) : send(frame);
      return child;
    },
  });
  await assert.rejects(trailing.controller.capture(), (error) => error.code === 'protocol');
  trailing.controller.dispose();
});

test('generation or interval substitution cannot reach the native read request', async () => {
  for (const key of ['start', 'generation', 'count', 'handle']) {
    const h = harness({
      alter(frame) {
        if (frame.command !== prefix + 'read') return frame;
        return { ...frame, arguments: { ...frame.arguments, [key]: typeof frame.arguments[key] === 'string' ? '2' : 2 } };
      },
    });
    await assert.rejects(h.controller.capture(), (error) => error.code === 'protocol');
    assert.equal(h.calls.some((call) => call.command === prefix + 'read'), false);
    assert.equal(h.calls.some((call) => call.command === prefix + 'close'), false);
    await assert.rejects(h.controller.capture(), (error) => error.code === 'reconciliation');
    h.controller.dispose();
  }
});

test('a clean-looking status cannot clear uncertainty without full authoritative fields', () => {
  assert.equal(native.cleanStatus({ status: status() }), true);
  assert.equal(native.cleanStatus({ status: status('closed') }), true);
  for (const patch of [
    { busy: true }, { needs_reconciliation: true }, { source_binding: 'unknown' },
    { native_runtime_qualified: true }, { hardware_qualified: true },
    { state: 'closed', handle: 0 }, { last_receipt: {} },
  ]) assert.equal(native.cleanStatus({ status: { ...status(), ...patch } }), false);
});

test('contradictory native close receipts retain reconciliation until authoritative clean status', async () => {
  for (const flag of ['native_state_uncertain', 'helper_abort_requested']) {
    let fault = true;
    const h = harness({
      customRequest(command) {
        const body = nativeReply(command);
        if (fault && command.endsWith('/close')) body.status.last_receipt[flag] = true;
        return body;
      },
      child(parameters) {
        const child = successChild(parameters);
        const send = child.send.bind(child);
        child.send = (frame) => {
          if (fault && frame.type === 'result') {
            send(terminal(false, { failure_stage: 'close' }));
            setImmediate(() => child.close(1));
          } else {
            send(frame);
          }
        };
        return child;
      },
    });
    const result = await h.controller.capture();
    assert.equal(result.capture_written, false);
    assert.equal(result.native_disconnect_confirmed, false);
    assert.equal(result.needs_reconciliation, true);
    await assert.rejects(h.controller.capture(), (error) => error.code === 'reconciliation');
    assert.equal(h.spawnCalls.length, 1);
    assert.equal((await h.controller.inspectStatus()).needs_reconciliation, false);
    fault = false;
    assert.equal((await h.controller.capture()).capture_written, true);
    h.controller.dispose();
  }
});

test('a helper disagreement after seemingly confirmed native close still requires reconciliation', async () => {
  const h = harness({
    child(parameters) {
      const child = successChild(parameters);
      const send = child.send.bind(child);
      child.send = (frame) => frame.type === 'result'
        ? send(terminal(false, { failure_stage: 'close' })) : send(frame);
      return child;
    },
  });
  await assert.rejects(h.controller.capture(), (error) => error.code === 'protocol');
  await assert.rejects(h.controller.capture(), (error) => error.code === 'reconciliation');
  assert.equal(h.spawnCalls.length, 1);
  assert.equal((await h.controller.inspectStatus()).needs_reconciliation, false);
  h.controller.dispose();
});

test('input validation preserves uint64 strings and rejects inferred source or collection settings', () => {
  assert.equal(native.inputValid(input()), true);
  assert.equal(native.canonicalUnsigned(UINT64()), true);
  assert.equal(native.canonicalUnsigned('18446744073709551616'), false);
  for (const replacement of [
    { sessionId: 0 }, { sessionId: '17' }, { sourceId: '\u202esource' }, { acknowledgeUnverifiedSource: false },
    { count: 4097 }, { start: '01' }, { sourceKind: 'snapshot' }, { snapshotSha256: 'a'.repeat(64) },
  ]) assert.equal(native.inputValid({ ...input(), ...replacement }), false);
  function UINT64() { return '18446744073709551615'; }
});
