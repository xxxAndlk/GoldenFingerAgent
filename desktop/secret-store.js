'use strict';

const fs = require('node:fs');
const path = require('node:path');

const REF_PATTERN = /^[A-Za-z0-9_-]{1,80}$/;

// SecretStore 只接受主进程传入的凭据，用 Electron safeStorage（Windows 上为 DPAPI）
// 加密后落盘。明文不写 settings.json、数据库、日志或 IPC 返回值。
class SecretStore {
  constructor({ filePath, safeStorage, now = () => new Date() }) {
    this.filePath = filePath;
    this.safeStorage = safeStorage;
    this.now = now;
  }

  available() {
    return Boolean(this.safeStorage && this.safeStorage.isEncryptionAvailable());
  }

  readFile() {
    try {
      const raw = fs.readFileSync(this.filePath, 'utf8');
      const parsed = JSON.parse(raw);
      if (parsed && parsed.version === 1 && parsed.items && typeof parsed.items === 'object') {
        return parsed;
      }
    } catch {
      // 文件不存在或损坏时按空存储处理，不覆盖到日志。
    }
    return { version: 1, items: {} };
  }

  writeFile(data) {
    fs.mkdirSync(path.dirname(this.filePath), { recursive: true });
    const tmp = `${this.filePath}.tmp`;
    fs.writeFileSync(tmp, JSON.stringify(data, null, 2), { mode: 0o600 });
    fs.renameSync(tmp, this.filePath);
    try { fs.chmodSync(this.filePath, 0o600); } catch { /* Windows ACL 由用户目录继承 */ }
  }

  save(ref, payload) {
    ref = String(ref || '').trim();
    if (!REF_PATTERN.test(ref)) throw new Error('credential_ref 只能包含字母、数字、下划线或短横线');
    if (!this.available()) throw new Error('系统安全存储不可用，拒绝保存明文密码');
    const origin = String(payload && payload.origin || '').trim().toLowerCase();
    if (!/^https:\/\/[^/]+$/.test(origin)) throw new Error('自动登录只允许 https origin');
    const loginUrl = String(payload && payload.loginUrl || '').trim();
    const username = String(payload && payload.username || '');
    const password = String(payload && payload.password || '');
    if (!username || !password) throw new Error('账号和密码不能为空');
    if (!loginUrl.toLowerCase().startsWith(`${origin}/`)) {
      throw new Error('登录地址必须属于已确认的 HTTPS origin');
    }
    const encrypted = this.safeStorage.encryptString(JSON.stringify({ origin, loginUrl, username, password }));
    const data = this.readFile();
    data.items[ref] = {
      cipher: Buffer.from(encrypted).toString('base64'),
      origin,
      updated_at: this.now().toISOString(),
    };
    this.writeFile(data);
    return { saved: true, ref, origin };
  }

  get(ref) {
    ref = String(ref || '').trim();
    const data = this.readFile();
    const item = data.items[ref];
    if (!item || !item.cipher) throw new Error('没有保存该站点的凭据');
    if (!this.available()) throw new Error('系统安全存储不可用');
    const raw = this.safeStorage.decryptString(Buffer.from(item.cipher, 'base64'));
    return JSON.parse(raw);
  }

  delete(ref) {
    ref = String(ref || '').trim();
    const data = this.readFile();
    if (!data.items[ref]) return false;
    delete data.items[ref];
    this.writeFile(data);
    return true;
  }

  list() {
    const data = this.readFile();
    return Object.entries(data.items).map(([ref, item]) => ({
      ref,
      origin: item.origin,
      updated_at: item.updated_at,
    }));
  }
}

module.exports = { SecretStore, REF_PATTERN };
