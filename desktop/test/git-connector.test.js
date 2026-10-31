'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const { readGitEvidence } = require('../git-connector');

test('Git connector returns file metadata without reading file contents', async () => {
  const calls = [];
  const evidence = await readGitEvidence('C:\\project', (_command, args, options, callback) => {
    calls.push({ args, options });
    const output = args[0] === 'status' ? ' M main.js\n?? new-file.js\n' : ' main.js | 4 ++--\n 1 file changed';
    callback(null, output, '');
  });
  assert.equal(evidence.changeCount, 2);
  assert.deepEqual(evidence.changedFiles, ['main.js', 'new-file.js']);
  assert.deepEqual(calls.map(call => call.args), [['status', '--short'], ['diff', '--stat']]);
});
