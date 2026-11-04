'use strict';

// 只把会话发现需要的安全字段交回服务端；Summary、正文、密钥和路径始终留在桌面侧。
function safeWeChatChats(chats) {
  return (Array.isArray(chats) ? chats : []).slice(0, 50).map(chat => ({
    source_id: String(chat && chat.source_id || '').slice(0, 500),
    chat_key: String(chat && chat.chat_key || '').slice(0, 500),
    display_name: String(chat && chat.display_name || '').slice(0, 300),
    chat_type: String(chat && chat.chat_type || '').slice(0, 40),
    account_id: String(chat && chat.account_id || '').slice(0, 200),
    last_message_at: String(chat && chat.last_message_at || '').slice(0, 80),
  })).filter(chat => chat.source_id && chat.chat_key);
}

function safeError(error) {
  return String(error && error.message || error || '桌面请求执行失败')
    .replace(/[\r\n]+/g, ' ').slice(0, 300);
}

class ConnectorRequestRunner {
  constructor({
    api,
    refreshChannels,
    listWeChatChats,
    setActivityEnabled,
    intervalMs = 2000,
    logger = console,
  }) {
    this.api = api;
    this.refreshChannels = refreshChannels;
    this.listWeChatChats = listWeChatChats;
    this.setActivityEnabled = setActivityEnabled;
    this.intervalMs = intervalMs;
    this.logger = logger;
    this.timer = null;
    this.polling = false;
    this.lastError = '';
  }

  start() {
    if (this.timer) return;
    this.timer = setInterval(() => { this.pollOnce().catch(() => {}); }, this.intervalMs);
    this.timer.unref?.();
    this.pollOnce().catch(() => {});
  }

  stop() {
    clearInterval(this.timer);
    this.timer = null;
  }

  async pollOnce() {
    // 微信发现可能需要启动连接器，耗时超过轮询间隔；必须防止重入。
    if (this.polling) return false;
    this.polling = true;
    try {
      // 聊天里的授权增删也要尽快反映到连接器和 Spool，而不是只等 30 秒后台刷新。
      // 刷新失败不阻断 activity 命令；原有后台轮询仍会重试。
      try { await this.refreshChannels(); } catch (error) { this.lastError = safeError(error); }
      const response = await this.api.pendingConnectorRequests();
      for (const request of response.requests || []) {
        const result = await this.execute(request);
        try {
          await this.api.completeConnectorRequest(request.id, result);
          this.lastError = result.ok ? '' : result.error;
        } catch (error) {
          this.lastError = safeError(error);
        }
      }
      return true;
    } catch (error) {
      this.lastError = safeError(error);
      return false;
    } finally {
      this.polling = false;
    }
  }

  async execute(request) {
    try {
      if (!request || !request.id) throw new Error('桌面请求缺少 id');
      if (request.kind === 'list_wechat_chats' && request.channel === 'wechat_desktop') {
        await this.refreshChannels();
        const chats = await this.listWeChatChats(String(request.query || ''), Number(request.limit) || 20);
        return { ok: true, chats: safeWeChatChats(chats) };
      }
      if (request.kind === 'set_activity' && request.channel === 'activity') {
        const status = await this.setActivityEnabled(Boolean(request.enabled));
        if (!status || typeof status.enabled !== 'boolean') throw new Error('桌面工作轨迹未返回真实开关状态');
        return { ok: true, enabled: status.enabled };
      }
      throw new Error('不支持的桌面采集请求');
    } catch (error) {
      return { ok: false, error: safeError(error) };
    }
  }
}

module.exports = { ConnectorRequestRunner, safeWeChatChats, safeError };
