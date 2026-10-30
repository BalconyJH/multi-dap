'use strict';

const { spawn } = require('node:child_process');
const { randomBytes } = require('node:crypto');
const { TextDecoder } = require('node:util');

const PROTOCOL = 'multi-dap.editor-trace.v1';
const PREFIX = 'multi-dap/trace/';
const MAX_LINE = 1 << 20;
const MAX_BODY = 512 << 10;
const MAX_ARGUMENTS = 16 << 10;
const MAX_FRAMES = 128;
const MAX_STDERR = 16 << 10;
const UINT64_MAX = '18446744073709551615';
const COMMANDS = new Set(['status', 'open', 'poll', 'read', 'close'].map((name) => PREFIX + name));
const STAGES = new Set(['none', 'admission', 'configuration', 'output', 'transport', 'open', 'poll',
  'read', 'validation', 'status', 'close', 'finalize', 'cancelled']);
const RESULT_FIELDS = ['protocol', 'type', 'captureId', 'capture_written', 'native_attempted',
  'native_disconnect_confirmed', 'needs_reconciliation', 'collection_may_continue',
  'native_runtime_qualified', 'hardware_qualified', 'failure_stage'];
const BOOLEAN_FIELDS = RESULT_FIELDS.slice(3, -1);
const ERROR_TEXT = {
  admission: 'An attached, configured multi-dap session with native trace capability is required.',
  busy: 'A native capture or status request is already in progress.',
  reconciliation: 'Native state is unconfirmed. Inspect native trace status before capturing again.',
  input: 'Native capture inputs are invalid.',
  cancelled: 'Native capture was cancelled. A reserved private file may be incomplete.',
  session: 'The bound debug session ended. Native state may need reconciliation.',
  timeout: 'Native capture timed out. Inspect native trace status before retrying.',
  protocol: 'The native capture helper returned an invalid protocol response.',
  helper: 'The native capture helper failed. The private output may be incomplete.',
  transport: 'The native trace request failed. Inspect native trace status before retrying.',
};

class CaptureError extends Error {
  constructor(code) {
    super(`multi-dap: ${ERROR_TEXT[code] || ERROR_TEXT.helper}`);
    this.code = code;
  }
}

function object(value) {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

function fields(value, names) {
  return object(value) && Object.keys(value).sort().join('\0') === [...names].sort().join('\0');
}

function canonicalUnsigned(value, maximum = UINT64_MAX) {
  return typeof value === 'string' && /^(0|[1-9][0-9]*)$/.test(value) &&
    (value.length < maximum.length || (value.length === maximum.length && value <= maximum));
}

function boundedSource(value) {
  return typeof value === 'string' && value.length !== 0 && value.trim() === value &&
    Buffer.byteLength(value, 'utf8') <= 128 && !/[\p{Cc}\p{Cf}\p{Cs}]/u.test(value);
}

function digest(value) {
  return typeof value === 'string' && /^[a-fA-F0-9]{64}$/.test(value);
}

// JSON.parse discards duplicate names. Detect them before trusting envelope identity.
function decodeFrame(bytes) {
  if (bytes.length > MAX_LINE) throw new CaptureError('protocol');
  let text;
  let value;
  try {
    text = new TextDecoder('utf-8', { fatal: true, ignoreBOM: true }).decode(bytes);
    value = JSON.parse(text);
  } catch {
    throw new CaptureError('protocol');
  }
  if (!object(value)) throw new CaptureError('protocol');
  const keys = new Set();
  let depth = 0;
  let expectKey = false;
  for (let index = 0; index < text.length; index++) {
    const character = text[index];
    if (character === '"') {
      const start = index++;
      while (index < text.length && text[index] !== '"') {
        if (text[index] === '\\') index++;
        index++;
      }
      if (depth === 1 && expectKey) {
        const key = JSON.parse(text.slice(start, index + 1));
        if (keys.has(key)) throw new CaptureError('protocol');
        keys.add(key);
        expectKey = false;
      }
    } else if (character === '{' || character === '[') {
      depth++;
      if (depth === 1) expectKey = true;
    } else if (character === '}' || character === ']') {
      depth--;
    } else if (character === ',' && depth === 1) {
      expectKey = true;
    }
  }
  return value;
}

function sourceArguments(input) {
  const result = {
    sourceKind: input.sourceKind, sessionId: input.sessionId, sourceId: input.sourceId,
    acknowledgeUnverifiedSource: true,
  };
  if (input.coreId !== undefined) result.coreId = input.coreId;
  if (input.imageSha256) result.imageSha256 = input.imageSha256;
  if (input.snapshotSha256) result.snapshotSha256 = input.snapshotSha256;
  return result;
}

function sameObject(left, right) {
  return fields(left, Object.keys(right)) && Object.keys(right).every((key) => left[key] === right[key]);
}

function inputValid(input) {
  return object(input) && ['live', 'snapshot'].includes(input.sourceKind) &&
    Number.isInteger(input.sessionId) && input.sessionId >= 1 && input.sessionId <= 2147483647 &&
    boundedSource(input.sourceId) &&
    (input.coreId === undefined || (Number.isInteger(input.coreId) && input.coreId >= 0 && input.coreId <= 4294967295)) &&
    (!input.imageSha256 || digest(input.imageSha256)) &&
    (input.sourceKind === 'snapshot' ? digest(input.snapshotSha256) : !input.snapshotSha256) &&
    canonicalUnsigned(input.start) && Number.isInteger(input.count) && input.count >= 1 && input.count <= 4096 &&
    input.acknowledgeUnverifiedSource === true && typeof input.output === 'string' && input.output.trim() !== '';
}

function helperCommand(session, executable, input, captureId) {
  const args = ['trace', 'capture-editor', '--config', session.configuration.configFile,
    '--capture-id', captureId, '--output', input.output, '--source-kind', input.sourceKind,
    '--session-id', String(input.sessionId), '--source-id', input.sourceId,
    '--start', input.start, '--count', String(input.count), '--acknowledge-unverified-source'];
  if (session.configuration.controlDirectory) args.push('--control-dir', session.configuration.controlDirectory);
  if (input.coreId !== undefined) args.push('--core-id', String(input.coreId));
  if (input.imageSha256) args.push('--image-sha256', input.imageSha256);
  if (input.snapshotSha256) args.push('--snapshot-sha256', input.snapshotSha256);
  return { command: executable.trim(), args };
}

function confirmedClose(body, handle) {
  const status = body?.status;
  const receipt = body?.receipt;
  return object(status) && object(receipt) && status.handle === handle && status.state === 'closed' &&
    status.busy === false && status.needs_reconciliation === false &&
    status.source_binding === 'operator_declared' &&
    status.native_runtime_qualified === false && status.hardware_qualified === false &&
    status.last_receipt?.native_state_uncertain === false &&
    status.last_receipt?.helper_abort_requested === false &&
    receipt.operation === 'close' && receipt.outcome === 'closed' &&
    receipt.native_disconnect_confirmed === true && receipt.native_state_uncertain === false &&
    receipt.helper_abort_requested === false && body.batch === undefined;
}

function cleanStatus(body) {
  const status = body?.status;
  const receipt = status?.last_receipt;
  if (!object(status) || !object(receipt) || status.busy !== false ||
      status.needs_reconciliation !== false || status.native_runtime_qualified !== false ||
      status.hardware_qualified !== false || status.source_binding !== 'operator_declared' ||
      receipt.native_state_uncertain !== false ||
      receipt.helper_abort_requested !== false) return false;
  return (status.state === 'idle' && status.handle === 0) ||
    (status.state === 'closed' && Number.isInteger(status.handle) && status.handle > 0 &&
      status.handle <= 2147483647 && receipt.native_disconnect_confirmed === true);
}

async function promptInput(ui) {
  const source = await ui.showQuickPick([
    { label: 'Live session', sourceKind: 'live' },
    { label: 'Already-loaded snapshot', sourceKind: 'snapshot' },
  ], { title: 'Select the existing native trace source', ignoreFocusOut: true });
  if (!source) return undefined;
  const ask = async (title, validateInput, value) => ui.showInputBox({
    title, ignoreFocusOut: true, validateInput, ...(value === undefined ? {} : { value }),
  });
  const session = await ask('Native trace session ID (not a PID, thread ID, or port)',
    (value) => canonicalUnsigned(value, '2147483647') && value !== '0' ? undefined : 'Enter a native session ID in 1..2147483647.');
  if (session === undefined) return undefined;
  const sourceId = await ask('Operator-declared source identity',
    (value) => boundedSource(value) ? undefined : 'Use a nonempty source identity of at most 128 UTF-8 bytes without control characters.');
  if (sourceId === undefined) return undefined;
  const core = await ask('Optional configured core ID (leave empty to omit)',
    (value) => value === '' || canonicalUnsigned(value, '4294967295') ? undefined : 'Enter a canonical uint32 or leave empty.');
  if (core === undefined) return undefined;
  const image = await ask('Optional image SHA-256 (leave empty to omit)',
    (value) => value === '' || digest(value) ? undefined : 'Enter 64 hexadecimal characters or leave empty.');
  if (image === undefined) return undefined;
  let snapshot;
  if (source.sourceKind === 'snapshot') {
    snapshot = await ask('SHA-256 of the already-loaded snapshot',
      (value) => digest(value) ? undefined : 'Enter the 64-character snapshot digest.');
    if (snapshot === undefined) return undefined;
  }
  const start = await ask('First packet index',
    (value) => canonicalUnsigned(value) ? undefined : 'Enter a canonical unsigned 64-bit decimal string.', '0');
  if (start === undefined) return undefined;
  const count = await ask('Maximum packets in this single interval',
    (value) => canonicalUnsigned(value, '4096') && value !== '0' ? undefined : 'Enter a count in 1..4096.', '4096');
  if (count === undefined) return undefined;
  const acknowledgement = await ui.showWarningMessage(
    'Native source/core association is unverified. Capture existing packets only from the session you explicitly selected. No collection start/stop or snapshot loading is performed.',
    { modal: true }, 'Capture selected source');
  if (acknowledgement !== 'Capture selected source') return undefined;
  const output = await ui.showSaveDialog({
    title: 'Choose a new private native capture file', saveLabel: 'Capture existing packets',
    filters: { 'Native capture JSON': ['json'] },
  });
  if (!output) return undefined;
  if (output.scheme !== 'file') throw new CaptureError('input');
  return {
    sourceKind: source.sourceKind, sessionId: Number(session), sourceId,
    ...(core === '' ? {} : { coreId: Number(core) }),
    ...(image === '' ? {} : { imageSha256: image }),
    ...(snapshot === undefined ? {} : { snapshotSha256: snapshot }),
    start, count: Number(count), acknowledgeUnverifiedSource: true, output: output.fsPath,
  };
}

function createController(vscode, options) {
  const ui = options.ui || vscode.window;
  const spawnChild = options.spawnChild || spawn;
  const randomId = options.randomId || (() => randomBytes(16).toString('hex'));
  const requestTimeout = options.requestTimeoutMs || 30000;
  const operationTimeout = options.operationTimeoutMs || 180000;
  const reapTimeout = options.reapTimeoutMs || 3000;
  const sessions = new Map();
  const states = new Set();
  let disposed = false;

  function tracker(session) {
    const previous = sessions.get(session.id);
    if (previous) terminateState(previous);
    const state = {
      session, capable: false, configured: false, terminated: false,
      inflight: 0, tombstone: previous?.tombstone === true, run: null, pending: new Map(), children: new Set(),
      generation: 0,
    };
    sessions.set(session.id, state);
    states.add(state);
    return {
      onWillReceiveMessage(message) {
        if (message.type === 'request' && Number.isSafeInteger(message.seq) && message.seq > 0 &&
            ['initialize', 'configurationDone', 'disconnect'].includes(message.command)) {
          if (message.command !== 'disconnect') {
            state.generation++;
            state.configured = false;
            if (message.command === 'initialize') state.capable = false;
            state.run?.abort?.('session');
          } else {
            terminateState(state);
          }
          if (state.pending.size >= 8) state.pending.delete(state.pending.keys().next().value);
          state.pending.set(message.seq, { command: message.command, generation: state.generation });
        }
      },
      onDidSendMessage(message) {
        const pending = state.pending.get(message.request_seq);
        if (message.type !== 'response' || pending?.command !== message.command) return;
        state.pending.delete(message.request_seq);
        if (pending.generation !== state.generation) return;
        if (message.command === 'initialize') {
          state.capable = message.success === true && message.body?.multiDapSupportsNativeTraceRequests === true;
        } else if (message.command === 'configurationDone') {
          state.configured = message.success === true;
        } else if (message.command === 'disconnect' && message.success === true) {
          terminateState(state);
        }
      },
      onWillStopSession: () => terminateState(state),
      onExit: () => terminateState(state),
    };
  }

  function admitted() {
    const session = vscode.debug.activeDebugSession;
    const state = session && sessions.get(session.id);
    if (disposed || !state || state.session !== session || session.type !== 'multi-dap' ||
        state.terminated || !state.capable || !state.configured || options.sessionError(session)) {
      throw new CaptureError('admission');
    }
    if (state.run || state.inflight !== 0 || state.children.size !== 0) throw new CaptureError('busy');
    return state;
  }

  function terminate(session) {
    const state = sessions.get(session.id);
    if (!state || state.session !== session) return;
    terminateState(state);
  }

  function retire(state) {
    if (!state.terminated || state.run || state.inflight !== 0 || state.children.size !== 0) return;
    states.delete(state);
    if (sessions.get(state.session.id) === state) sessions.delete(state.session.id);
  }

  function terminateState(state) {
    state.terminated = true;
    state.configured = false;
    state.run?.abort?.('session');
    retire(state);
  }

  function markUncertain(state) {
    state.tombstone = true;
    const current = sessions.get(state.session.id);
    if (current) current.tombstone = true;
  }

  async function relayRequest(state, command, args, timeout, stillLive = () => true) {
    state.inflight++;
    let timer;
    const request = Promise.resolve().then(() => {
      if (state.terminated || disposed || !state.configured || !state.capable || !stillLive()) throw new CaptureError('session');
      return state.session.customRequest(command, args);
    });
    // A timed-out promise still owns its request until it actually settles.
    request.finally(() => {
      state.inflight--;
      retire(state);
    }).catch(() => {});
    try {
      return await Promise.race([
        request,
        new Promise((_, reject) => { timer = setTimeout(() => reject(new CaptureError('timeout')), timeout); }),
      ]);
    } finally {
      clearTimeout(timer);
    }
  }

  function runHelper(state, input, executable, cancellation) {
    return new Promise((resolve, reject) => {
      const captureId = randomId();
      if (!/^[0-9a-f]{32}$/.test(captureId)) return reject(new CaptureError('helper'));
      const command = helperCommand(state.session, executable, input, captureId);
      let child;
      const run = state.run;
      Object.assign(run, {
        active: true, nativeAttempted: false, nativeClosed: false, result: undefined,
        ready: false, pending: false, expectedId: 1, frames: 0, bytes: Buffer.alloc(0),
        handle: undefined, generation: undefined, polled: false, read: false, closing: false,
        preflightClean: false, stderr: 0, error: undefined, settled: false,
      });
      const finish = (code) => {
        if (run.settled) return;
        run.settled = true;
        run.active = false;
        clearTimeout(overall);
        clearTimeout(run.reap);
        cancelled?.dispose();
        if (run.nativeAttempted && !run.nativeClosed) markUncertain(state);
        if (run.result?.needs_reconciliation) markUncertain(state);
        if (!run.error && (!run.result || run.bytes.length !== 0 || run.pending ||
            (run.result.capture_written && code !== 0) || (!run.result.capture_written && code === 0))) {
          run.error = new CaptureError('protocol');
        }
        if (run.error && run.nativeAttempted) markUncertain(state);
        if (run.error) reject(run.error);
        else resolve(run.result);
      };
      const fail = (code) => {
        if (!run.active) return;
        run.active = false;
        run.error = new CaptureError(code);
        if (run.nativeAttempted) markUncertain(state);
        child?.stdin.destroy();
        child?.kill();
        run.reap = setTimeout(() => {
          child?.kill('SIGKILL');
          // Never make a still-live child available for another capture.
          markUncertain(state);
          finish(undefined);
        }, reapTimeout);
      };
      run.abort = fail;
      const overall = setTimeout(() => fail('timeout'), operationTimeout);
      const cancelled = cancellation?.onCancellationRequested(() => fail('cancelled'));
      if (!run.active || cancellation?.isCancellationRequested) {
        run.error = new CaptureError('cancelled');
        finish(undefined);
        return;
      }
      try {
        child = spawnChild(command.command, command.args, {
          windowsHide: true, shell: false, stdio: ['pipe', 'pipe', 'pipe'],
        });
      } catch {
        run.error = new CaptureError('helper');
        finish(undefined);
        return;
      }
      run.child = child;
      state.children.add(child);
      if (cancellation?.isCancellationRequested || state.terminated || disposed) {
        fail(cancellation?.isCancellationRequested ? 'cancelled' : 'session');
      }
      child.stdin.on('error', () => fail('transport'));
      child.once('error', () => fail('helper'));
      child.once('close', (code) => {
        state.children.delete(child);
        retire(state);
        finish(code);
      });
      child.stderr.on('data', (data) => {
        run.stderr += data.length;
        if (run.stderr > MAX_STDERR) fail('protocol');
      });
      const receive = (frame) => {
        if (!run.active || ++run.frames > MAX_FRAMES || frame.protocol !== PROTOCOL ||
            frame.captureId !== captureId || run.result) throw new CaptureError('protocol');
        if (frame.type === 'ready') {
          if (run.ready || !fields(frame, ['protocol', 'type', 'captureId'])) throw new CaptureError('protocol');
          run.ready = true;
          return;
        }
        if (frame.type === 'result') {
          if (!run.ready || !fields(frame, RESULT_FIELDS) ||
              !BOOLEAN_FIELDS.every((key) => typeof frame[key] === 'boolean') ||
              !STAGES.has(frame.failure_stage) || frame.native_runtime_qualified || frame.hardware_qualified ||
              frame.native_attempted !== run.nativeAttempted ||
              frame.native_disconnect_confirmed !== run.nativeClosed ||
              (frame.native_attempted && !frame.collection_may_continue) ||
              (run.nativeAttempted && !frame.native_disconnect_confirmed && !frame.needs_reconciliation) ||
              (frame.capture_written && (run.pending || frame.failure_stage !== 'none' || !frame.native_attempted ||
                !frame.native_disconnect_confirmed || frame.needs_reconciliation || !run.read || !run.closing)) ||
              (!frame.capture_written && frame.failure_stage === 'none')) {
            throw new CaptureError('protocol');
          }
          run.result = frame;
          // A helper deadline can finish before customRequest settles. Its
          // late promise remains counted, but is no longer allowed to reply.
          run.pending = false;
          child.stdin.end();
          return;
        }
        if (frame.type !== 'request' || !run.ready || run.pending || run.closing ||
            !fields(frame, ['protocol', 'type', 'captureId', 'id', 'command', 'arguments']) ||
            frame.id !== run.expectedId || !Number.isInteger(frame.id) || !COMMANDS.has(frame.command) ||
            !object(frame.arguments) || Buffer.byteLength(JSON.stringify(frame.arguments)) > MAX_ARGUMENTS) {
          throw new CaptureError('protocol');
        }
        const operation = frame.command.slice(PREFIX.length);
        const args = frame.arguments;
        if (frame.id === 1 && operation !== 'status') throw new CaptureError('protocol');
        if (operation === 'status') {
          if (!fields(args, [])) throw new CaptureError('protocol');
        } else if (operation === 'open') {
          if (run.nativeAttempted || !run.preflightClean || !sameObject(args, sourceArguments(input))) {
            throw new CaptureError('protocol');
          }
          run.nativeAttempted = true;
        } else {
          if (!run.handle || args.handle !== run.handle) throw new CaptureError('protocol');
          if (operation === 'read') {
            if (!run.polled || run.read || !fields(args, ['handle', 'start', 'generation', 'count']) ||
                args.start !== input.start || args.count !== input.count || args.generation !== run.generation) {
              throw new CaptureError('protocol');
            }
            run.read = true;
          } else if (!fields(args, ['handle'])) {
            throw new CaptureError('protocol');
          }
          if (operation === 'poll' && run.polled) throw new CaptureError('protocol');
          if (operation === 'close') run.closing = true;
        }
        run.expectedId++;
        run.pending = true;
        relayRequest(state, frame.command, args, requestTimeout, () => run.active && !run.result).then((body) => {
          if (!run.active || run.result || state.terminated || disposed) return;
          let serialized;
          try {
            serialized = JSON.stringify(body);
            if (!object(body) || Buffer.byteLength(serialized) > MAX_BODY) throw new Error();
          } catch {
            fail('protocol');
            return;
          }
          if (operation === 'status' && frame.id === 1) run.preflightClean = cleanStatus(body);
          if (operation === 'open' && Number.isInteger(body.status?.handle) &&
              body.status.handle > 0 && body.status.handle <= 2147483647) {
            run.handle = body.status.handle;
          }
          if (operation === 'poll') {
            if (!canonicalUnsigned(body.status?.native_generation) || body.status.native_generation === '0') {
              fail('protocol');
              return;
            }
            run.polled = true;
            run.generation = body.status.native_generation;
          }
          if (operation === 'close') run.nativeClosed = confirmedClose(body, run.handle);
          sendResponse(true, body);
        }, (error) => {
          if (!run.active || run.result || state.terminated || disposed) return;
          if (error instanceof CaptureError && error.code === 'timeout') fail('timeout');
          else sendResponse(false, {});
        });
        const sendResponse = (success, body) => {
          if (!run.active) return;
          if (++run.frames > MAX_FRAMES) return fail('protocol');
          run.pending = false;
          const encoded = JSON.stringify({
            protocol: PROTOCOL, type: 'response', captureId, id: frame.id,
            command: frame.command, success, body,
          }) + '\n';
          if (Buffer.byteLength(encoded) > MAX_LINE) return fail('protocol');
          if (operation === 'close') child.stdin.end(encoded);
          else child.stdin.write(encoded);
        };
      };
      child.stdout.on('data', (data) => {
        if (!run.active) return;
        run.bytes = Buffer.concat([run.bytes, data]);
        try {
          let newline;
          while ((newline = run.bytes.indexOf(10)) !== -1) {
            const line = run.bytes.subarray(0, newline);
            run.bytes = run.bytes.subarray(newline + 1);
            receive(decodeFrame(line));
          }
          if (run.bytes.length > MAX_LINE) throw new CaptureError('protocol');
        } catch {
          fail('protocol');
        }
      });
    });
  }

  async function capture() {
    const state = admitted();
    if ([...states].some((entry) => entry.run || entry.inflight !== 0 || entry.children.size !== 0)) {
      throw new CaptureError('busy');
    }
    if (state.tombstone) throw new CaptureError('reconciliation');
    state.run = { admissionGeneration: state.generation };
    try {
      const input = await promptInput(ui);
      if (!input) return undefined;
      if (disposed || state.terminated || !state.configured || !state.capable ||
          state.run.admissionGeneration !== state.generation) throw new CaptureError('session');
      if (!inputValid(input) || options.sessionError(state.session, input.output)) throw new CaptureError('input');
      const executable = options.executableForSession(state.session);
      if (typeof executable !== 'string' || executable.trim() === '' || /\p{Cc}/u.test(executable)) {
        throw new CaptureError('input');
      }
      return await ui.withProgress({
        location: vscode.ProgressLocation.Notification, title: 'Capturing one native trace interval', cancellable: true,
      }, (_, token) => runHelper(state, input, executable, token));
    } finally {
      state.run = null;
      retire(state);
    }
  }

  async function inspectStatus() {
    const state = admitted();
    if ([...states].some((entry) => entry !== state && entry.session.id === state.session.id &&
        (entry.run || entry.inflight !== 0 || entry.children.size !== 0))) throw new CaptureError('busy');
    state.run = { admissionGeneration: state.generation };
    try {
      const body = await relayRequest(state, PREFIX + 'status', {}, requestTimeout);
      if (state.terminated || disposed || !state.configured || !state.capable ||
          state.run.admissionGeneration !== state.generation) throw new CaptureError('session');
      if (cleanStatus(body)) state.tombstone = false;
      else state.tombstone = true;
      const status = body?.status;
      const knownState = ['idle', 'open', 'closed', 'faulted', 'unavailable'].includes(status?.state) ? status.state : 'unconfirmed';
      await ui.showInformationMessage(`Native trace status: ${knownState}; reconciliation ${state.tombstone ? 'required' : 'not required'}.`);
      return { state: knownState, needs_reconciliation: state.tombstone };
    } catch {
      throw new CaptureError('transport');
    } finally {
      state.run = null;
      retire(state);
    }
  }

  function dispose() {
    disposed = true;
    for (const state of states) terminateState(state);
    sessions.clear();
  }

  return { tracker, capture, inspectStatus, terminate, dispose };
}

function register(context, vscode, options) {
  const controller = createController(vscode, options);
  const execute = (action) => async () => {
    try {
      const result = await action();
      if (result?.capture_written) await vscode.window.showInformationMessage('Native capture saved. Runtime and hardware qualification remain unverified.');
      else if (result?.type === 'result') await vscode.window.showErrorMessage(
        result.needs_reconciliation ? ERROR_TEXT.reconciliation : 'Native capture failed. The reserved private output may be incomplete.');
      return result;
    } catch (error) {
      await vscode.window.showErrorMessage(error instanceof CaptureError ? error.message : ERROR_TEXT.helper);
      return undefined;
    }
  };
  context.subscriptions.push(
    vscode.debug.registerDebugAdapterTrackerFactory('multi-dap', { createDebugAdapterTracker: controller.tracker }),
    vscode.debug.onDidTerminateDebugSession(controller.terminate),
    vscode.commands.registerCommand('multiDap.captureNativeTrace', execute(controller.capture)),
    vscode.commands.registerCommand('multiDap.inspectNativeTraceStatus', execute(controller.inspectStatus)),
    { dispose: controller.dispose },
  );
  return controller;
}

module.exports = {
  PROTOCOL, MAX_LINE, MAX_BODY, MAX_ARGUMENTS, MAX_FRAMES, CaptureError,
  canonicalUnsigned, inputValid, decodeFrame, helperCommand, cleanStatus, confirmedClose,
  promptInput, createController, register,
};
