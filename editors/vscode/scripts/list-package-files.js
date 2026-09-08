'use strict';

const fs = require('node:fs');
const path = require('node:path');
const { spawnSync } = require('node:child_process');

const root = path.resolve(__dirname, '..');
const publishedFiles = ['README.md', 'package.json', 'src/extension.js'];

for (const relativePath of publishedFiles) {
  const absolutePath = path.join(root, relativePath);
  if (!fs.statSync(absolutePath).isFile()) {
    throw new Error(`Missing publishable file: ${relativePath}`);
  }
}

const npmCli = process.env.npm_execpath;
if (typeof npmCli !== 'string' || npmCli === '') {
  throw new Error('npm_execpath is required to enumerate package contents.');
}
const packed = spawnSync(process.execPath, [npmCli, 'pack', '--dry-run', '--json'], {
  cwd: root,
  encoding: 'utf8',
  windowsHide: true,
});
if (packed.status !== 0) {
  const detail = packed.error ? packed.error.message : (packed.stderr || '').trim();
  throw new Error(`Could not enumerate package contents: ${detail}`);
}
const archive = JSON.parse(packed.stdout);
const actualFiles = archive[0].files.map((file) => file.path).sort();
const expectedFiles = [...publishedFiles].sort();
if (JSON.stringify(actualFiles) !== JSON.stringify(expectedFiles)) {
  throw new Error(`Unexpected package contents: ${actualFiles.join(', ')}`);
}

process.stdout.write(`${actualFiles.join('\n')}\n`);
