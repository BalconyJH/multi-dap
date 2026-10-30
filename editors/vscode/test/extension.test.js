'use strict';

const assert = require('node:assert/strict');
const path = require('node:path');
const test = require('node:test');
const { execFile } = require('node:child_process');

const extension = require('../src/extension');
const manifest = require('../package.json');

const configFile = path.resolve('test-fixtures', 'project.toml');
const perfOutput = path.resolve('test-fixtures', 'host-001.trace.json');
const bridgeScript = path.resolve('bridge files', 'bridge.py');
const primaryElf = path.resolve('images', 'primary.elf');

function configuration(overrides = {}) {
  return {
    type: 'multi-dap',
    request: 'attach',
    configFile,
    ...overrides,
  };
}

test('accepts an attach configuration with an absolute TOML path', () => {
  assert.equal(extension.configurationError(configuration()), undefined);
});

test('fails closed for unsupported requests and direct connection fields', () => {
  assert.match(extension.configurationError(configuration({ request: 'launch' })), /Only attach/);
  for (const key of extension.FORBIDDEN_CONNECTION_OPTIONS) {
    assert.match(extension.configurationError(configuration({ [key]: key === 'host' ? '127.0.0.1' : 4711 })), new RegExp(key));
  }
});

test('requires absolute configuration and control-record paths', () => {
  assert.match(extension.configurationError(configuration({ configFile: 'project.toml' })), /absolute/);
  assert.match(extension.configurationError(configuration({ controlDirectory: 'control' })), /controlDirectory.*absolute/);
});

test('builds a proxy command without connection arguments', () => {
  assert.deepEqual(
    extension.proxyCommand(configuration({ controlDirectory: 'C:\\control' }), ' C:\\bin\\multi-dap.exe '),
    {
      command: 'C:\\bin\\multi-dap.exe',
      args: ['proxy', '--config', configFile, '--control-dir', 'C:\\control'],
    },
  );
});

test('provider reports invalid configuration without returning a descriptor', () => {
  const messages = [];
  assert.equal(extension.resolveDebugConfiguration(configuration({ port: 4711 }), (message) => messages.push(message)), undefined);
  assert.equal(messages.length, 1);
  assert.match(messages[0], /proxy/);
});

test('initial resolution preserves VS Code variables until substituted resolution validates paths', () => {
  const messages = [];
  const unresolved = configuration({ configFile: '${workspaceFolder}/project.toml' });
  assert.equal(extension.resolveDebugConfiguration(unresolved, (message) => messages.push(message), false), unresolved);
  assert.equal(extension.resolveDebugConfiguration(unresolved, (message) => messages.push(message)), undefined);
  assert.equal(messages.length, 1);
});

test('descriptor factory delegates only to the authenticated proxy command', () => {
  class DebugAdapterExecutable {
    constructor(command, args) {
      this.command = command;
      this.args = args;
    }
  }
  const vscode = {
    DebugAdapterExecutable,
    workspace: {
      getConfiguration(section) {
        assert.equal(section, 'multiDap');
        return { get: () => 'multi-dap' };
      },
    },
  };
  const descriptor = extension.descriptorFor(vscode, configuration());
  assert.ok(descriptor instanceof DebugAdapterExecutable);
  assert.deepEqual(descriptor.args, ['proxy', '--config', configFile]);
});

test('activation registers both the configuration provider and descriptor factory', () => {
  const subscriptions = [];
  const registrations = [];
  const vscode = {
    DebugAdapterExecutable: class {},
    debug: {
      registerDebugConfigurationProvider(type, provider) {
        registrations.push({ kind: 'provider', type, provider });
        return { dispose() {} };
      },
      registerDebugAdapterDescriptorFactory(type, factory) {
        registrations.push({ kind: 'factory', type, factory });
        return { dispose() {} };
      },
      registerDebugAdapterTrackerFactory(type, factory) {
        registrations.push({ kind: 'tracker', type, factory });
        return { dispose() {} };
      },
      onDidTerminateDebugSession() { return { dispose() {} }; },
    },
    commands: {
      registerCommand(command) {
        registrations.push({ kind: 'command', command });
        return { dispose() {} };
      },
    },
    window: { showErrorMessage() {} },
    workspace: { getConfiguration: () => ({ get: () => 'multi-dap' }) },
  };
  extension.activate({ subscriptions }, vscode);
  assert.equal(subscriptions.length, 7);
  assert.deepEqual(registrations.map((item) => [item.kind, item.type || item.command]), [
    ['provider', 'multi-dap'], ['factory', 'multi-dap'], ['tracker', 'multi-dap'],
    ['command', 'multiDap.captureNativeTrace'], ['command', 'multiDap.inspectNativeTraceStatus'],
  ]);
});

test('host capture is opt-in and never added by default', () => {
  assert.equal(extension.configurationError(configuration()), undefined);
  assert.deepEqual(extension.proxyCommand(configuration(), 'multi-dap'), {
    command: 'multi-dap',
    args: ['proxy', '--config', configFile],
  });
  const snippet = manifest.contributes.debuggers[0].configurationSnippets[0].body;
  assert.equal(Object.hasOwn(snippet, 'perfOutput'), false);
  assert.equal(Object.hasOwn(snippet, 'perfMaxEvents'), false);
});

test('host capture accepts only explicit valid paths and bounded integer limits', () => {
  assert.equal(extension.configurationError(configuration({ perfOutput })), undefined);
  for (const perfMaxEvents of [1, 10000, extension.MAX_PERF_EVENTS]) {
    assert.equal(extension.configurationError(configuration({ perfOutput, perfMaxEvents })), undefined);
  }
  for (const value of ['', ' ', '\t', null, undefined, false, true, 1, {}, [], 'relative.trace.json']) {
    assert.match(extension.configurationError(configuration({ perfOutput: value })), /perfOutput/);
  }
  for (const value of ['\0', '\r', '\n', '\t', '\x1f', '\x7f']) {
    const issue = extension.configurationError(configuration({ perfOutput: `${perfOutput}${value}PRIVATE_PATH` }));
    assert.match(issue, /perfOutput.*control/);
    assert.equal(issue.includes('PRIVATE_PATH'), false);
  }
  for (const perfMaxEvents of [0, -1, 1.5, 100001, NaN, Infinity, -Infinity, '10000', '', null, undefined, false, {}, [], 1n]) {
    assert.match(extension.configurationError(configuration({ perfOutput, perfMaxEvents })), /perfMaxEvents.*integer/);
  }
  assert.match(extension.configurationError(configuration({ perfMaxEvents: 1 })), /requires.*perfOutput/);
});

test('host capture path follows both VS Code variable-substitution validation stages', () => {
  const unresolved = configuration({ perfOutput: '${workspaceFolder}/private/host-001.trace.json', perfMaxEvents: 1 });
  const messages = [];
  assert.equal(extension.resolveDebugConfiguration(unresolved, (message) => messages.push(message), false), unresolved);
  assert.equal(extension.resolveDebugConfiguration(unresolved, (message) => messages.push(message)), undefined);
  assert.match(messages[0], /perfOutput.*absolute/);
  const resolved = configuration({ perfOutput });
  assert.equal(extension.resolveDebugConfiguration(resolved, assert.fail), resolved);
});

test('host capture arguments remain separate literal argv entries', () => {
  const literalPath = path.resolve('private captures', 'host & $value; 001.trace.json');
  const selected = configuration({ perfOutput: literalPath, perfMaxEvents: 123, controlDirectory: path.resolve('private control') });
  assert.equal(extension.configurationError(selected), undefined);
  assert.deepEqual(extension.proxyCommand(selected, 'multi-dap'), {
    command: 'multi-dap',
    args: [
      'proxy', '--config', configFile, '--control-dir', selected.controlDirectory,
      '--perf-output', literalPath, '--perf-max-events', '123',
    ],
  });
  assert.deepEqual(extension.proxyCommand(configuration({ perfOutput }), 'multi-dap').args, [
    'proxy', '--config', configFile, '--perf-output', perfOutput,
  ]);
});

test('provider and descriptor enforce host capture admission before creating an adapter', () => {
  const registrations = [];
  const messages = [];
  const descriptors = [];
  const vscode = {
    DebugAdapterExecutable: class {
      constructor(command, args) { descriptors.push({ command, args }); }
    },
    debug: {
      registerDebugConfigurationProvider(_type, provider) {
        registrations.push(provider);
        return { dispose() {} };
      },
      registerDebugAdapterDescriptorFactory(_type, factory) {
        registrations.push(factory);
        return { dispose() {} };
      },
      registerDebugAdapterTrackerFactory() { return { dispose() {} }; },
      onDidTerminateDebugSession() { return { dispose() {} }; },
    },
    commands: { registerCommand() { return { dispose() {} }; } },
    window: { showErrorMessage(message) { messages.push(message); } },
    workspace: { getConfiguration: () => ({ get: () => 'multi-dap' }) },
  };
  extension.activate({ subscriptions: [] }, vscode);
  const [provider, factory] = registrations;
  const unresolved = configuration({ perfOutput: '${workspaceFolder}/host-001.trace.json' });
  assert.equal(provider.resolveDebugConfiguration(undefined, unresolved), unresolved);
  assert.equal(provider.resolveDebugConfigurationWithSubstitutedVariables(undefined, unresolved), undefined);
  for (const invalid of [
    configuration({ perfMaxEvents: 1 }),
    configuration({ perfOutput, perfMaxEvents: 0 }),
    configuration({ perfOutput: 'PRIVATE_RELATIVE_PATH' }),
  ]) {
    assert.equal(provider.resolveDebugConfigurationWithSubstitutedVariables(undefined, invalid), undefined);
    assert.throws(() => factory.createDebugAdapterDescriptor({ configuration: invalid }), /multi-dap:/);
  }
  assert.equal(descriptors.length, 0);
  assert.equal(messages.some((message) => message.includes('PRIVATE_RELATIVE_PATH')), false);
  const valid = configuration({ perfOutput, perfMaxEvents: 1 });
  assert.equal(provider.resolveDebugConfigurationWithSubstitutedVariables(undefined, valid), valid);
  factory.createDebugAdapterDescriptor({ configuration: valid });
  assert.deepEqual(descriptors, [{
    command: 'multi-dap',
    args: ['proxy', '--config', configFile, '--perf-output', perfOutput, '--perf-max-events', '1'],
  }]);
});

test('package host capture schema agrees with runtime bounds without enabling capture', () => {
  const attach = manifest.contributes.debuggers[0].configurationAttributes.attach;
  const output = attach.properties.perfOutput;
  const maximum = attach.properties.perfMaxEvents;
  assert.equal(output.type, 'string');
  assert.equal(output.minLength, 1);
  assert.equal(new RegExp(output.pattern).test(perfOutput), true);
  for (const invalid of ['', ' \t', `${perfOutput}\0`, `${perfOutput}\r`, `${perfOutput}\x7f`]) {
    assert.equal(new RegExp(output.pattern).test(invalid), false);
  }
  assert.equal(maximum.type, 'integer');
  assert.equal(maximum.minimum, 1);
  assert.equal(maximum.maximum, extension.MAX_PERF_EVENTS);
  assert.deepEqual(attach.dependencies.perfMaxEvents, ['perfOutput']);
  assert.equal(attach.required.includes('perfOutput'), false);
  assert.equal(attach.required.includes('perfMaxEvents'), false);
  assert.equal(Object.hasOwn(output, 'default'), false);
  assert.equal(Object.hasOwn(maximum, 'default'), false);
});

function registeredExtension(executeFile = assert.fail) {
  const registrations = {};
  const descriptors = [];
  const messages = [];
  const vscode = {
    DebugAdapterExecutable: class {
      constructor(command, args) { descriptors.push({ command, args }); }
    },
    debug: {
      registerDebugConfigurationProvider(_type, provider) {
        registrations.provider = provider;
        return { dispose() {} };
      },
      registerDebugAdapterDescriptorFactory(_type, factory) {
        registrations.factory = factory;
        return { dispose() {} };
      },
      registerDebugAdapterTrackerFactory() { return { dispose() {} }; },
      onDidTerminateDebugSession() { return { dispose() {} }; },
    },
    commands: { registerCommand() { return { dispose() {} }; } },
    window: { showErrorMessage(message) { messages.push(message); } },
    workspace: { getConfiguration: () => ({ get: () => 'multi-dap' }) },
  };
  extension.activate({ subscriptions: [] }, vscode, executeFile);
  return { ...registrations, descriptors, messages };
}

function receipt(event = 'ready') {
  return JSON.stringify({ event, dap_address: '127.0.0.1:12345', pid: 123 });
}

test('all project paths require full Windows identities and retain POSIX support', () => {
  for (const value of ['C:\\work\\project.toml', 'D:/work/project.toml', '\\\\server\\share\\project.toml', '//server/share/project.toml']) {
    assert.equal(extension.fullyQualifiedPath(value, path.win32), true, value);
  }
  for (const value of [
    '/remote/work/project.toml', '\\project.toml', 'C:project.toml', 'project.toml',
    '\\\\server', '\\\\server\\', '\\\\?\\C:\\project.toml', '\\\\.\\C:\\project.toml',
    '\\\\server\\..\\project.toml', '\\\\server\\ \\project.toml', '\\\\server\\bad:share\\project.toml',
  ]) {
    assert.equal(extension.fullyQualifiedPath(value, path.win32), false, value);
  }
  assert.equal(extension.fullyQualifiedPath('/work/project.toml', path.posix), true);
  assert.equal(extension.fullyQualifiedPath('work/project.toml', path.posix), false);
  for (const field of ['configFile', 'controlDirectory', 'perfOutput', 'bridgeScript', 'primaryElf']) {
    const base = configuration({ configFile: 'C:\\project.toml', acquisition: 'warm', bridgeScript: 'C:\\bridge.py', primaryElf: 'C:\\primary.elf' });
    assert.match(extension.configurationError({ ...base, [field]: '\\PRIVATE' }, true, path.win32), new RegExp(`${field}.*absolute`));
    assert.match(extension.configurationError({ ...base, [field]: 'C:\\${unresolved}' }, true, path.win32), new RegExp(`${field}.*absolute`));
    for (const control of ['\0', '\t', '\n', '\x1f', '\x7f']) {
      for (const requireAbsolute of [true, false]) {
        const issue = extension.configurationError({ ...base, [field]: `C:\\PRIVATE${control}` }, requireAbsolute, path.win32);
        assert.match(issue, new RegExp(`${field}.*control`));
        assert.equal(issue.includes('PRIVATE'), false);
      }
    }
  }
});

test('remote and virtual workspaces fail in both provider stages and factory before spawning', () => {
  const { provider, factory, descriptors, messages } = registeredExtension();
  for (const scheme of ['vscode-remote', 'git', 'untitled']) {
    const folder = { uri: { scheme } };
    const selected = configuration({ acquisition: 'cold', bridgeScript });
    assert.equal(provider.resolveDebugConfiguration(folder, selected), undefined);
    assert.equal(provider.resolveDebugConfigurationWithSubstitutedVariables(folder, selected), undefined);
    assert.throws(() => factory.createDebugAdapterDescriptor({ configuration: selected, workspaceFolder: folder }), /local file workspaces/);
  }
  assert.equal(descriptors.length, 0);
  assert.equal(messages.length, 6);
  for (const folder of [undefined, { uri: { scheme: 'file' } }]) {
    const selected = configuration();
    assert.equal(provider.resolveDebugConfiguration(folder, selected), selected);
    assert.equal(provider.resolveDebugConfigurationWithSubstitutedVariables(folder, selected), selected);
    factory.createDebugAdapterDescriptor({ configuration: selected, workspaceFolder: folder });
  }
  assert.equal(descriptors.length, 2);
});

test('bootstrap requires explicit complete acquisition inputs before any process starts', () => {
  const { provider, factory, descriptors } = registeredExtension();
  for (const overrides of [
    { acquisition: 'auto', bridgeScript },
    { acquisition: 'cold' },
    { acquisition: 'warm', bridgeScript },
    { acquisition: 'cold', bridgeScript, primaryElf },
    { bridgeScript },
    { primaryElf },
    { acquisition: undefined, bridgeScript },
    { acquisition: 'cold', bridgeScript: 'PRIVATE_RELATIVE' },
  ]) {
    const selected = configuration(overrides);
    assert.equal(provider.resolveDebugConfigurationWithSubstitutedVariables(undefined, selected), undefined);
    assert.throws(() => factory.createDebugAdapterDescriptor({ configuration: selected }), /multi-dap:/);
  }
  assert.equal(descriptors.length, 0);
  const unresolved = configuration({
    configFile: '${workspaceFolder}/project.toml',
    acquisition: 'warm',
    bridgeScript: '${env:BRIDGE_ROOT}/bridge.py',
    primaryElf: '${workspaceFolder}/primary.elf',
  });
  assert.equal(provider.resolveDebugConfiguration(undefined, unresolved), unresolved);
  assert.equal(provider.resolveDebugConfigurationWithSubstitutedVariables(undefined, unresolved), undefined);
});

test('cold and warm bootstrap run literal ensure arguments and wait for its successful receipt', async () => {
  for (const acquisition of ['cold', 'warm']) {
    for (const event of ['ready', 'already_ready']) {
      let completed;
      const invocations = [];
      const { factory, descriptors } = registeredExtension((command, args, options, callback) => {
        invocations.push({ command, args, options });
        completed = callback;
      });
      const selected = configuration({
        acquisition,
        configFile: path.resolve('private files', 'project & $value; 1.toml'),
        bridgeScript,
        controlDirectory: path.resolve('private control'),
        perfOutput,
        perfMaxEvents: 10,
        ...(acquisition === 'warm' ? { primaryElf } : {}),
      });
      const pending = factory.createDebugAdapterDescriptor({ configuration: selected });
      assert.ok(pending instanceof Promise);
      assert.equal(descriptors.length, 0);
      const expectedArgs = [
        'ensure', '--config', selected.configFile, '--bridge-script', bridgeScript, '--acquisition', acquisition,
        ...(acquisition === 'warm' ? ['--primary-elf', primaryElf] : []),
        '--control-dir', selected.controlDirectory,
      ];
      assert.deepEqual(invocations, [{
        command: 'multi-dap',
        args: expectedArgs,
        options: {
          encoding: 'utf8', shell: false, windowsHide: true,
          timeout: extension.ENSURE_TIMEOUT_MS, maxBuffer: extension.ENSURE_MAX_OUTPUT_BYTES,
        },
      }]);
      completed(null, receipt(event), 'PRIVATE_DIAGNOSTIC');
      await pending;
      assert.deepEqual(descriptors, [extension.proxyCommand(selected, 'multi-dap')]);
    }
  }
});

test('default attach never invokes ensure and keeps acquisition out of the default snippet', () => {
  const { factory, descriptors } = registeredExtension();
  const descriptor = factory.createDebugAdapterDescriptor({ configuration: configuration() });
  assert.equal(descriptor instanceof Promise, false);
  assert.deepEqual(descriptors, [extension.proxyCommand(configuration(), 'multi-dap')]);
  const snippet = manifest.contributes.debuggers[0].configurationSnippets[0].body;
  for (const name of ['acquisition', 'bridgeScript', 'primaryElf']) {
    assert.equal(Object.hasOwn(snippet, name), false);
  }
});

test('failed bootstrap never constructs a proxy or retries another acquisition mode', async () => {
  const failures = [
    (callback) => callback(new Error('PRIVATE_SPAWN_ERROR'), receipt(), 'PRIVATE_STDERR'),
    (callback) => callback(Object.assign(new Error('PRIVATE_EXIT'), { code: 1 }), receipt()),
    (callback) => callback(Object.assign(new Error('PRIVATE_TIMEOUT'), { killed: true }), receipt()),
    (callback) => callback(Object.assign(new Error('PRIVATE_OVERFLOW'), { code: 'ERR_CHILD_PROCESS_STDIO_MAXBUFFER' }), receipt()),
    () => { throw new Error('PRIVATE_THROW'); },
  ];
  for (const failure of failures) {
    let calls = 0;
    const { factory, descriptors } = registeredExtension((_command, args, _options, callback) => {
      calls++;
      assert.equal(args[args.indexOf('--acquisition') + 1], 'warm');
      failure(callback);
    });
    await assert.rejects(factory.createDebugAdapterDescriptor({
      configuration: configuration({ acquisition: 'warm', bridgeScript, primaryElf }),
    }), (error) => {
      assert.match(error.message, /bootstrap failed/);
      assert.equal(error.message.includes('PRIVATE'), false);
      return true;
    });
    assert.equal(calls, 1);
    assert.equal(descriptors.length, 0);
  }
});

test('only bounded complete ensure receipts enable a proxy', async () => {
  const invalid = [
    '', 'PRIVATE_NOT_JSON', 'null', '[]', '{}', receipt('started'),
    `${receipt()}\n${receipt()}`, receipt().replace('12345', '0'),
    receipt().replace('127.0.0.1', 'PRIVATE_HOST'),
    receipt().replace('"pid":123', '"pid":0'),
    receipt().replace('"pid":123', '"pid":9007199254740992'),
    receipt().replace('}', ',"private":"PRIVATE_DATA"}'),
    `${receipt()}${' '.repeat(extension.ENSURE_MAX_OUTPUT_BYTES)}`,
  ];
  for (const stdout of invalid) {
    const { factory, descriptors } = registeredExtension((_command, _args, _options, callback) => callback(null, stdout));
    await assert.rejects(factory.createDebugAdapterDescriptor({
      configuration: configuration({ acquisition: 'cold', bridgeScript }),
    }), (error) => {
      assert.match(error.message, /bootstrap failed/);
      assert.equal(error.message.includes('PRIVATE'), false);
      return true;
    });
    assert.equal(descriptors.length, 0);
  }
  assert.equal(extension.validEnsureReceipt(receipt().replace('127.0.0.1', '[::1]')), true);
});

test('real child output, timeout, and output overflow follow bootstrap bounds without native tools', async () => {
  await extension.runEnsure({
    command: process.execPath,
    args: ['-e', `process.stdout.write(${JSON.stringify(receipt())})`],
  });
  await assert.rejects(extension.runEnsure({
    command: process.execPath,
    args: ['-e', 'setInterval(() => {}, 1000)'],
  }, (command, args, options, callback) => execFile(command, args, { ...options, timeout: 50 }, callback)), /bootstrap failed/);
  await assert.rejects(extension.runEnsure({
    command: process.execPath,
    args: ['-e', `process.stdout.write('X'.repeat(${extension.ENSURE_MAX_OUTPUT_BYTES + 1}))`],
  }), /bootstrap failed/);
});

test('bootstrap manifest declares explicit dependencies and disables virtual workspaces', () => {
  const attach = manifest.contributes.debuggers[0].configurationAttributes.attach;
  assert.deepEqual(attach.properties.acquisition.enum, ['cold', 'warm']);
  assert.deepEqual(attach.dependencies.acquisition, ['bridgeScript']);
  assert.deepEqual(attach.dependencies.bridgeScript, ['acquisition']);
  assert.deepEqual(attach.dependencies.primaryElf, ['acquisition', 'bridgeScript']);
  for (const name of ['acquisition', 'bridgeScript', 'primaryElf']) {
    assert.equal(Object.hasOwn(attach.properties[name], 'default'), false);
    assert.equal(attach.required.includes(name), false);
  }
  assert.equal(manifest.capabilities.virtualWorkspaces, false);
});
