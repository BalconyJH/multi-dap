'use strict';

const assert = require('node:assert/strict');
const test = require('node:test');

const extension = require('../src/extension');

function configuration(overrides = {}) {
  return {
    type: 'multi-dap',
    request: 'attach',
    configFile: 'C:\\work\\project.toml',
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
      args: ['proxy', '--config', 'C:\\work\\project.toml', '--control-dir', 'C:\\control'],
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
  assert.deepEqual(descriptor.args, ['proxy', '--config', 'C:\\work\\project.toml']);
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
