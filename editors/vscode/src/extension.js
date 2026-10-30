'use strict';

const path = require('node:path');
const { execFile } = require('node:child_process');
const { isIP } = require('node:net');

const DEBUG_TYPE = 'multi-dap';
const CONFIGURATION_SECTION = 'multiDap';
const FORBIDDEN_CONNECTION_OPTIONS = ['debugServer', 'host', 'port'];
const MAX_PERF_EVENTS = 100000;
const ENSURE_TIMEOUT_MS = 120000;
const ENSURE_MAX_OUTPUT_BYTES = 16384;
const CONTROL_CHARACTERS = /[\u0000-\u001f\u007f]/;

function fullyQualifiedPath(value, pathAPI = path) {
  if (pathAPI === path.win32) {
    if (/^[a-zA-Z]:[\\/]/.test(value)) {
      return true;
    }
    // Root-relative and device paths are not local project path identities.
    const unc = /^\\\\([^\\]+)\\([^\\]+)(?:\\|$)/.exec(value.replace(/\//g, '\\'));
    return unc !== null && unc.slice(1).every((part) =>
      part.trim() !== '' && part !== '.' && part !== '..' && !/[:*?"<>|]/.test(part));
  }
  return pathAPI.isAbsolute(value);
}

function pathError(value, name, requireAbsolutePaths, pathAPI) {
  if (typeof value !== 'string' || value.trim() === '') {
    return `${name} must be a non-empty absolute path.`;
  }
  if (CONTROL_CHARACTERS.test(value)) {
    return `${name} contains an invalid control character.`;
  }
  if (!requireAbsolutePaths && /\$\{[^{}]+\}/.test(value)) {
    return undefined;
  }
  if (value.includes('${') || !fullyQualifiedPath(value, pathAPI)) {
    return `${name} must be a fully qualified absolute path after variable substitution.`;
  }
  return undefined;
}

function workspaceError(folder) {
  if (folder !== undefined && (!folder || !folder.uri || folder.uri.scheme !== 'file')) {
    return 'Only local file workspaces are supported.';
  }
  return undefined;
}

function configurationError(configuration, requireAbsolutePaths = true, pathAPI = path) {
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
  for (const name of ['configFile', 'controlDirectory', 'perfOutput', 'bridgeScript', 'primaryElf']) {
    if (name === 'configFile' || Object.hasOwn(configuration, name)) {
      const issue = pathError(configuration[name], name, requireAbsolutePaths, pathAPI);
      if (issue !== undefined) {
        return issue;
      }
    }
  }
  const hasPerfOutput = Object.hasOwn(configuration, 'perfOutput');
  if (Object.prototype.hasOwnProperty.call(configuration, 'perfMaxEvents')) {
    if (!hasPerfOutput) {
      return 'perfMaxEvents requires an explicit perfOutput.';
    }
    if (!Number.isInteger(configuration.perfMaxEvents) ||
        configuration.perfMaxEvents < 1 || configuration.perfMaxEvents > MAX_PERF_EVENTS) {
      return `perfMaxEvents must be an integer in 1..${MAX_PERF_EVENTS}.`;
    }
  }
  if (Object.hasOwn(configuration, 'acquisition')) {
    if (!['cold', 'warm'].includes(configuration.acquisition)) {
      return 'acquisition must explicitly select cold or warm.';
    }
    if (!Object.hasOwn(configuration, 'bridgeScript')) {
      return 'acquisition requires an explicit absolute bridgeScript.';
    }
    if (configuration.acquisition === 'warm' && !Object.hasOwn(configuration, 'primaryElf')) {
      return 'Warm acquisition requires an explicit absolute primaryElf.';
    }
    if (configuration.acquisition === 'cold' && Object.hasOwn(configuration, 'primaryElf')) {
      return 'primaryElf requires warm acquisition.';
    }
  } else if (Object.hasOwn(configuration, 'bridgeScript') || Object.hasOwn(configuration, 'primaryElf')) {
    return 'bridgeScript and primaryElf require an explicit acquisition choice.';
  }
  return undefined;
}

function executableError(executable) {
  if (typeof executable !== 'string' || executable.trim() === '') {
    return 'The multiDap.executable setting must be a non-empty command or executable path.';
  }
  if (CONTROL_CHARACTERS.test(executable)) {
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

function ensureCommand(configuration, executable) {
  const args = [
    'ensure', '--config', configuration.configFile,
    '--bridge-script', configuration.bridgeScript, '--acquisition', configuration.acquisition,
  ];
  if (configuration.acquisition === 'warm') {
    args.push('--primary-elf', configuration.primaryElf);
  }
  if (Object.hasOwn(configuration, 'controlDirectory')) {
    args.push('--control-dir', configuration.controlDirectory);
  }
  return { command: executable.trim(), args };
}

function validEnsureReceipt(stdout) {
  if (typeof stdout !== 'string' || Buffer.byteLength(stdout, 'utf8') > ENSURE_MAX_OUTPUT_BYTES) {
    return false;
  }
  try {
    const receipt = JSON.parse(stdout);
    if (!receipt || Array.isArray(receipt) ||
        Object.keys(receipt).sort().join(',') !== 'dap_address,event,pid' ||
        !['ready', 'already_ready'].includes(receipt.event) ||
        !Number.isSafeInteger(receipt.pid) || receipt.pid <= 0 ||
        typeof receipt.dap_address !== 'string') {
      return false;
    }
    const address = /^(\[[^\]]+\]|[^:]+):([0-9]+)$/.exec(receipt.dap_address);
    return address !== null && isIP(address[1].replace(/^\[|\]$/g, '')) !== 0 &&
      Number(address[2]) >= 1 && Number(address[2]) <= 65535;
  } catch {
    return false;
  }
}

function runEnsure(command, executeFile = execFile) {
  return new Promise((resolve, reject) => {
    const failed = () => reject(new Error(
      'multi-dap: Daemon bootstrap failed. Use multi-dap diagnose with the same configuration; no proxy was started.',
    ));
    try {
      executeFile(command.command, command.args, {
        encoding: 'utf8',
        shell: false,
        windowsHide: true,
        timeout: ENSURE_TIMEOUT_MS,
        maxBuffer: ENSURE_MAX_OUTPUT_BYTES,
      }, (error, stdout) => {
        // Native diagnostics and receipts may contain private paths or endpoints.
        if (error || !validEnsureReceipt(stdout)) {
          failed();
          return;
        }
        resolve();
      });
    } catch {
      failed();
    }
  });
}

function resolveDebugConfiguration(configuration, showErrorMessage, requireAbsolutePaths = true, folder) {
  const error = workspaceError(folder) || configurationError(configuration, requireAbsolutePaths);
  if (error !== undefined) {
    showErrorMessage(`multi-dap: ${error}`);
    return undefined;
  }
  return configuration;
}

function descriptorFor(vscode, configuration, folder, executeFile = execFile) {
  const error = workspaceError(folder) || configurationError(configuration);
  if (error !== undefined) {
    throw new Error(`multi-dap: ${error}`);
  }
  const executable = vscode.workspace.getConfiguration(CONFIGURATION_SECTION).get('executable');
  const executableIssue = executableError(executable);
  if (executableIssue !== undefined) {
    throw new Error(`multi-dap: ${executableIssue}`);
  }
  const command = proxyCommand(configuration, executable);
  const descriptor = () => new vscode.DebugAdapterExecutable(command.command, command.args);
  if (Object.hasOwn(configuration, 'acquisition')) {
    return runEnsure(ensureCommand(configuration, executable), executeFile).then(descriptor);
  }
  return descriptor();
}

function activate(context, vscode = require('vscode'), executeFile = execFile) {
  const provider = {
    resolveDebugConfiguration(folder, configuration) {
      return resolveDebugConfiguration(configuration, (message) => vscode.window.showErrorMessage(message), false, folder);
    },
    resolveDebugConfigurationWithSubstitutedVariables(folder, configuration) {
      return resolveDebugConfiguration(configuration, (message) => vscode.window.showErrorMessage(message), true, folder);
    },
  };
  const factory = {
    createDebugAdapterDescriptor(session) {
      return descriptorFor(vscode, session.configuration, session.workspaceFolder, executeFile);
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
  ENSURE_MAX_OUTPUT_BYTES,
  ENSURE_TIMEOUT_MS,
  activate,
  configurationError,
  deactivate,
  descriptorFor,
  ensureCommand,
  executableError,
  fullyQualifiedPath,
  proxyCommand,
  resolveDebugConfiguration,
  runEnsure,
  validEnsureReceipt,
  workspaceError,
};
