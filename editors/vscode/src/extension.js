'use strict';

const path = require('node:path');

const DEBUG_TYPE = 'multi-dap';
const CONFIGURATION_SECTION = 'multiDap';
const FORBIDDEN_CONNECTION_OPTIONS = ['debugServer', 'host', 'port'];
const MAX_PERF_EVENTS = 100000;

function configurationError(configuration, requireAbsolutePaths = true) {
  if (configuration === undefined || configuration === null || typeof configuration !== 'object') {
    return 'A launch configuration is required.';
  }
  if (configuration.type !== DEBUG_TYPE) {
    return `The debugger type must be ${DEBUG_TYPE}.`;
  }
  if (configuration.request !== 'attach') {
    return 'Only attach requests are supported.';
  }
  for (const key of FORBIDDEN_CONNECTION_OPTIONS) {
    if (Object.prototype.hasOwnProperty.call(configuration, key)) {
      return `${key} is unsupported; connect through the authenticated local proxy instead.`;
    }
  }
  if (typeof configuration.configFile !== 'string' || configuration.configFile.trim() === '') {
    return 'configFile must be a non-empty absolute path to a project TOML file.';
  }
  if (requireAbsolutePaths && !path.isAbsolute(configuration.configFile)) {
    return 'configFile must be an absolute path. Use ${workspaceFolder} in launch.json when appropriate.';
  }
  if (Object.prototype.hasOwnProperty.call(configuration, 'controlDirectory')) {
    if (typeof configuration.controlDirectory !== 'string' || configuration.controlDirectory.trim() === '') {
      return 'controlDirectory must be a non-empty absolute path when supplied.';
    }
    if (requireAbsolutePaths && !path.isAbsolute(configuration.controlDirectory)) {
      return 'controlDirectory must be an absolute path when supplied.';
    }
  }
  const hasPerfOutput = Object.prototype.hasOwnProperty.call(configuration, 'perfOutput');
  if (hasPerfOutput) {
    if (typeof configuration.perfOutput !== 'string' || configuration.perfOutput.trim() === '') {
      return 'perfOutput must be a non-empty absolute path to a new private capture file.';
    }
    if (/[\u0000-\u001f\u007f]/.test(configuration.perfOutput)) {
      return 'perfOutput contains an invalid control character.';
    }
    if (requireAbsolutePaths && !path.isAbsolute(configuration.perfOutput)) {
      return 'perfOutput must be an absolute path after variable substitution.';
    }
  }
  if (Object.prototype.hasOwnProperty.call(configuration, 'perfMaxEvents')) {
    if (!hasPerfOutput) {
      return 'perfMaxEvents requires an explicit perfOutput.';
    }
    if (!Number.isInteger(configuration.perfMaxEvents) ||
        configuration.perfMaxEvents < 1 || configuration.perfMaxEvents > MAX_PERF_EVENTS) {
      return `perfMaxEvents must be an integer in 1..${MAX_PERF_EVENTS}.`;
    }
  }
  return undefined;
}

function executableError(executable) {
  if (typeof executable !== 'string' || executable.trim() === '') {
    return 'The multiDap.executable setting must be a non-empty command or executable path.';
  }
  if (/[\0\r\n]/.test(executable)) {
    return 'The multiDap.executable setting contains an invalid control character.';
  }
  return undefined;
}

function proxyCommand(configuration, executable) {
  const args = ['proxy', '--config', configuration.configFile];
  if (Object.prototype.hasOwnProperty.call(configuration, 'controlDirectory')) {
    args.push('--control-dir', configuration.controlDirectory);
  }
  if (Object.prototype.hasOwnProperty.call(configuration, 'perfOutput')) {
    args.push('--perf-output', configuration.perfOutput);
  }
  if (Object.prototype.hasOwnProperty.call(configuration, 'perfMaxEvents')) {
    args.push('--perf-max-events', String(configuration.perfMaxEvents));
  }
  return { command: executable.trim(), args };
}

function resolveDebugConfiguration(configuration, showErrorMessage, requireAbsolutePaths = true) {
  const error = configurationError(configuration, requireAbsolutePaths);
  if (error !== undefined) {
    showErrorMessage(`multi-dap: ${error}`);
    return undefined;
  }
  return configuration;
}

function descriptorFor(vscode, configuration) {
  const error = configurationError(configuration);
  if (error !== undefined) {
    throw new Error(`multi-dap: ${error}`);
  }
  const executable = vscode.workspace.getConfiguration(CONFIGURATION_SECTION).get('executable');
  const executableIssue = executableError(executable);
  if (executableIssue !== undefined) {
    throw new Error(`multi-dap: ${executableIssue}`);
  }
  const command = proxyCommand(configuration, executable);
  return new vscode.DebugAdapterExecutable(command.command, command.args);
}

function activate(context, vscode = require('vscode')) {
  const provider = {
    resolveDebugConfiguration(_folder, configuration) {
      return resolveDebugConfiguration(configuration, (message) => vscode.window.showErrorMessage(message), false);
    },
    resolveDebugConfigurationWithSubstitutedVariables(_folder, configuration) {
      return resolveDebugConfiguration(configuration, (message) => vscode.window.showErrorMessage(message));
    },
  };
  const factory = {
    createDebugAdapterDescriptor(session) {
      return descriptorFor(vscode, session.configuration);
    },
  };
  context.subscriptions.push(
    vscode.debug.registerDebugConfigurationProvider(DEBUG_TYPE, provider),
    vscode.debug.registerDebugAdapterDescriptorFactory(DEBUG_TYPE, factory),
  );
}

function deactivate() {}

module.exports = {
  CONFIGURATION_SECTION,
  DEBUG_TYPE,
  FORBIDDEN_CONNECTION_OPTIONS,
  MAX_PERF_EVENTS,
  activate,
  configurationError,
  deactivate,
  descriptorFor,
  executableError,
  proxyCommand,
  resolveDebugConfiguration,
};
