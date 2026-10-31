'use strict';

const { execFile } = require('node:child_process');

function git(args, cwd, execFileImpl = execFile) {
  return new Promise((resolve, reject) => {
    execFileImpl('git', args, { cwd, windowsHide: true, timeout: 5000, encoding: 'utf8', maxBuffer: 1024 * 1024 }, (error, stdout, stderr) => {
      if (error) reject(new Error(String(stderr || error.message).trim()));
      else resolve(stdout.trim());
    });
  });
}

async function readGitEvidence(workspace, execFileImpl = execFile) {
  const status = await git(['status', '--short'], workspace, execFileImpl);
  const diff = await git(['diff', '--stat'], workspace, execFileImpl);
  const files = status ? status.split(/\r?\n/).slice(0, 30).map(line => line.slice(2).trim()).filter(Boolean) : [];
  return {
    workspace,
    changedFiles: files,
    changeCount: status ? status.split(/\r?\n/).length : 0,
    diffStat: diff.slice(-4000),
    summary: files.length ? `${files.length} 个文件有未提交改动：${files.slice(0, 5).join('、')}` : '当前没有未提交改动',
  };
}

module.exports = { readGitEvidence };
