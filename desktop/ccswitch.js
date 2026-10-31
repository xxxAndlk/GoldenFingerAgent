'use strict';

const api = window.desktopAssistant;
const container = document.querySelector('#provider-list');
const status = document.querySelector('#provider-status');
const type = document.querySelector('#provider-type');

async function loadProviders() {
  status.textContent = '正在读取供应商与用量…';
  container.replaceChildren();
  try {
    const result = await api.agentProviders(type.value);
    const providers = result.providers || [];
    for (const provider of providers) {
      const row = document.createElement('article');
      row.className = 'provider-row';
      const body = document.createElement('div');
      const title = document.createElement('strong');
      title.textContent = `${provider.name}${provider.current ? ' · 当前' : ''}`;
      const usage = document.createElement('p');
      const limits = [
        provider.limit_daily_usd ? `日限额 $${provider.limit_daily_usd}` : '',
        provider.limit_monthly_usd ? `月限额 $${provider.limit_monthly_usd}` : '',
      ].filter(Boolean).join(' · ');
      usage.textContent = `${provider.app_type} · 今日 $${Number(provider.used_today_usd || 0).toFixed(4)} · 本月 $${Number(provider.used_month_usd || 0).toFixed(4)}${limits ? ` · ${limits}` : ''}`;
      body.append(title, usage);
      const button = document.createElement('button');
      button.className = 'secondary-button';
      button.textContent = provider.current ? '使用中' : '切换';
      button.disabled = !provider.can_switch || provider.current;
      button.title = provider.can_switch ? '备份并切换 Claude Code 配置' : '此类型目前只读';
      button.addEventListener('click', async () => {
        if (!window.confirm(`确认将 Claude Code 切换到“${provider.name}”？原配置会先备份。`)) return;
        button.disabled = true;
        try { await api.switchAgentProvider(provider.app_type, provider.id); await loadProviders(); }
        catch (error) { status.textContent = `切换失败：${error.message}`; button.disabled = false; }
      });
      row.append(body, button);
      container.appendChild(row);
    }
    status.textContent = providers.length ? `共 ${providers.length} 个供应商` : '此类型下还没有供应商';
  } catch (error) {
    status.textContent = `读取失败：${error.message}`;
  }
}

document.querySelector('#providers-load').addEventListener('click', loadProviders);
type.addEventListener('change', loadProviders);
loadProviders();
