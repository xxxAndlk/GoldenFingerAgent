'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { SecretStore } = require('../secret-store');

function fakeSafeStorage() {
  return {
    isEncryptionAvailable: () => true,
    encryptString: value => Buffer.from(`enc:${value}`, 'utf8'),
    decryptString: value => Buffer.from(value).toString('utf8').replace(/^enc:/, ''),
  };
}

function newStore() {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'gfa-secret-'));
  return new SecretStore({
    filePath: path.join(dir, 'secrets.json'),
    safeStorage: fakeSafeStorage(),
  });
}

test('SecretStore encrypts payload and does not write plaintext', () => {
  const store = newStore();
  const saved = store.save('example-login', {
    origin: 'https://example.com',
    loginUrl: 'https://example.com/login',
    username: 'me@example.com',
    password: 'super-secret',
  });
  assert.equal(saved.saved, true);
  const raw = fs.readFileSync(store.filePath, 'utf8');
  assert.equal(raw.includes('super-secret'), false);
  assert.equal(raw.includes('me@example.com'), false);
  const credential = store.get('example-login');
  assert.equal(credential.username, 'me@example.com');
  assert.equal(credential.password, 'super-secret');
  assert.deepEqual(store.list().map(item => item.ref), ['example-login']);
  assert.equal(store.delete('example-login'), true);
  assert.throws(() => store.get('example-login'), /没有保存/);
});

test('SecretStore rejects non-HTTPS origins and missing fields', () => {
  const store = newStore();
  assert.throws(() => store.save('bad', {
    origin: 'http://example.com', loginUrl: 'http://example.com/login',
    username: 'u', password: 'p',
  }), /https/);
  assert.throws(() => store.save('empty', {
    origin: 'https://example.com', loginUrl: 'https://example.com/login',
    username: '', password: 'p',
  }), /账号/);
  assert.throws(() => store.save('../bad', {
    origin: 'https://example.com', loginUrl: 'https://example.com/login',
    username: 'u', password: 'p',
  }), /credential_ref/);
});

test('SecretStore refuses to save when encryption is unavailable', () => {
  const store = new SecretStore({
    filePath: path.join(os.tmpdir(), 'gfa-secret-unavailable', 'secrets.json'),
    safeStorage: { isEncryptionAvailable: () => false },
  });
  assert.throws(() => store.save('example', {
    origin: 'https://example.com', loginUrl: 'https://example.com/login',
    username: 'u', password: 'p',
  }), /安全存储/);
});
