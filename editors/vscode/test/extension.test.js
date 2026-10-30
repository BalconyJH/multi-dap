'use strict';

const assert = require('node:assert/strict');
const path = require('node:path');
const test = require('node:test');

const extension = require('../src/extension');
const manifest = require('../package.json');

const configFile = path.resolve('test-fixtures', 'project.toml');
const perfOutput = path.resolve('test-fixtures', 'host-001.trace.json');

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
    },
    window: { showErrorMessage() {} },
    workspace: { getConfiguration: () => ({ get: () => 'multi-dap' }) },
  };
  extension.activate({ subscriptions }, vscode);
  assert.equal(subscriptions.length, 2);
  assert.deepEqual(registrations.map((item) => [item.kind, item.type]), [['provider', 'multi-dap'], ['factory', 'multi-dap']]);
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
    },
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
