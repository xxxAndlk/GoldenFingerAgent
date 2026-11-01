'use strict';

(function exposeAgentPanel(root, factory) {
  const api = factory();
  if (typeof module !== 'undefined' && module.exports) module.exports = api;
  else root.AgentPanel = api;
}(typeof window !== 'undefined' ? window : globalThis, () => {
  const KIND_NAMES = { desktop: '桌面软件', cli: '命令行', service: '项目服务', bma: 'BMA', a2a: 'A2A' };
  const EMPTY_MESSAGES = {
    desktop: '未发现可管理的桌面软件',
    cli: '未发现独立 CLI；同名桌面软件不会被当作命令行工具',
    service: '未发现 A2A 服务或 BMA 端点',
  };

  function canDelegate(agent) {
    return Array.isArray(agent.capabilities) && agent.capabilities.some(item => item === 'delegate' || item === 'desktop_delegate');
  }

  function canOpen(agent) {
    return ['desktop', 'cli'].includes(agent.kind) && Array.isArray(agent.capabilities) && agent.capabilities.includes('open');
  }

  // 只列出本机真实存在（已安装或已配置）或正在运行的 Agent；
  // 扫描器预置但未发现的条目不在列表里占位。
  function visibleAgents(agents, filter) {
    return agents.filter(agent => {
      if (!agent.installed && !agent.running) return false;
      return filter === 'service'
        ? ['service', 'bma', 'a2a'].includes(agent.kind)
        : agent.kind === filter;
    });
  }

  function init({ api }) {
    const list = document.querySelector('#agent-list');
    const status = document.querySelector('#agent-status');
    const taskInput = document.querySelector('#agent-task');
    const workDirInput = document.querySelector('#agent-workdir');
    const resultSection = document.querySelector('#agent-result-section');
    const resultTitle = document.querySelector('#agent-result-title');
    const resultOutput = document.querySelector('#agent-result');
    const clarifyBox = document.querySelector('#agent-clarify');
    const clarifyQuestion = document.querySelector('#agent-clarify-question');
    const clarifyOptions = document.querySelector('#agent-clarify-options');
    const clarifyAnswer = document.querySelector('#agent-clarify-answer');
    const clarifySend = document.querySelector('#agent-clarify-send');
    const clarifyStatus = document.querySelector('#agent-clarify-status');
    const a2aConfig = document.querySelector('#a2a-config');
    let agents = [];
    let filter = 'desktop';
    let clarify = { sessionId: '', questionId: '', name: '', busy: false };

    function setClarifyBusy(value) {
      for (const button of clarifyOptions.querySelectorAll('button')) button.disabled = value;
      clarifyAnswer.disabled = value;
      clarifySend.disabled = value;
    }

    // BMA 遇到计划确认/ask_user 时不再让 GFA 等到超时：把问题和选项直接交给用户。
    function renderClarify(name, result) {
      const pending = result && result.state === 'awaiting_clarify' && result.task_id;
      if (!pending) {
        clarifyBox.hidden = true;
        clarify = { sessionId: '', questionId: '', name: '', busy: false };
        return;
      }
      clarify = {
        sessionId: result.task_id,
        questionId: result.question_id || '',
        name,
        busy: false,
      };
      clarifyBox.hidden = false;
      clarifyQuestion.textContent = result.question || result.output || 'BMA 正在等待你的答复。';
      clarifyOptions.replaceChildren();
      for (const option of result.options || []) {
        const button = document.createElement('button');
        button.type = 'button';
        button.className = 'secondary-button';
        button.textContent = option.label || option.id;
        if (option.description) button.title = option.description;
        button.addEventListener('click', () => sendClarify(option.id));
        clarifyOptions.append(button);
      }
      clarifyAnswer.value = '';
      clarifyStatus.textContent = '';
      clarifyAnswer.focus();
    }

    async function sendClarify(answer) {
      const value = String(answer || '').trim();
      if (!value) {
        clarifyStatus.textContent = '先填写答复内容';
        clarifyAnswer.focus();
        return;
      }
      if (clarify.busy || !clarify.sessionId) return;
      const name = clarify.name || 'BMA';
      const sessionId = clarify.sessionId;
      clarify.busy = true;
      setClarifyBusy(true);
      clarifyStatus.textContent = '正在发送答复，等待 BMA 处理…';
      try {
        showResult(name, await api.answerAgent(sessionId, value, clarify.questionId));
      } catch (error) {
        clarifyStatus.textContent = `答复失败：${error.message}`;
      } finally {
        clarify.busy = false;
        setClarifyBusy(false);
      }
    }

    function showResult(name, result) {
      resultTitle.textContent = `${name} · ${result.state || '已返回'}`;
      resultOutput.textContent = result.output || '任务已提交，等待执行结果。';
      resultSection.hidden = false;
      renderClarify(name, result);
      resultSection.scrollIntoView({ block: 'nearest', behavior: 'smooth' });
    }

    function render() {
      list.replaceChildren();
      const visible = visibleAgents(agents, filter);
      for (const agent of visible) {
        const card = document.createElement('article');
        card.className = 'agent-card';
        card.dataset.agentId = agent.id;
        const dot = document.createElement('span');
        dot.className = `agent-dot${agent.running ? ' running' : ''}`;
        const body = document.createElement('div');
        const name = document.createElement('strong');
        name.textContent = agent.name;
        const kind = document.createElement('span');
        kind.className = 'agent-kind';
        kind.textContent = KIND_NAMES[agent.kind] || agent.kind;
        name.appendChild(kind);
        const detail = document.createElement('small');
        const detailText = agent.detail || agent.description || '';
        const parts = [];
        // 扫描结果自己的 detail 往往已经写了「已安装 / 已配置」，不要重复前缀。
        if (agent.running) parts.push('正在运行');
        else if (agent.installed && !/已安装|已配置|未运行|已连接/.test(detailText)) parts.push('已安装 / 已配置');
        if (detailText) parts.push(detailText);
        detail.textContent = parts.join(' · ') || '无补充信息';
        body.append(name, detail);
        const actions = document.createElement('div');
        actions.className = 'agent-actions';
        if (['desktop', 'cli'].includes(agent.kind)) {
          const openButton = document.createElement('button');
          openButton.type = 'button';
          openButton.dataset.action = 'open';
          openButton.className = 'secondary-button';
          openButton.disabled = !canOpen(agent);
          openButton.textContent = agent.kind === 'cli' ? '启动终端' : (agent.running ? '唤起' : '打开');
          openButton.title = canOpen(agent)
            ? (agent.kind === 'cli' ? '在项目目录打开交互式命令行终端' : '打开或唤起桌面软件')
            : '未发现可启动入口，请先安装对应 CLI';
          openButton.addEventListener('click', () => open(agent, openButton));
          actions.appendChild(openButton);
        }
        const button = document.createElement('button');
        button.type = 'button';
        button.dataset.action = 'delegate';
        button.className = 'primary-button';
        button.textContent = '分派';
        button.disabled = !canDelegate(agent);
        button.title = button.disabled ? '该 Agent 尚未提供可用调用通道' : '在指定项目目录中执行任务';
        button.addEventListener('click', () => delegate(agent, button));
        actions.appendChild(button);
        card.append(dot, body, actions);
        list.appendChild(card);
      }
      if (!visible.length) {
        const empty = document.createElement('p');
        empty.className = 'status-text';
        empty.textContent = EMPTY_MESSAGES[filter] || '此分类下没有 Agent';
        list.appendChild(empty);
      }
      const installed = agents.filter(agent => agent.installed).length;
      const running = agents.filter(agent => agent.running).length;
      status.textContent = `共发现 ${installed} 个已安装/已配置 Agent，${running} 个正在运行`;
      a2aConfig.hidden = filter !== 'service';
    }

    async function refresh(force = false) {
      status.textContent = force
        ? '正在重新扫描 A2A、本机命令、桌面软件和 BMA…'
        : '正在读取已保存的 Agent 配置…';
      try {
        const result = await api.listAgents(force);
        agents = (result.agents || []).filter(agent => agent.id !== 'ccswitch');
        render();
      } catch (error) {
        status.textContent = `读取失败：${error.message}`;
      }
    }

    async function open(agent, button) {
      button.disabled = true;
      status.textContent = agent.kind === 'cli'
        ? `正在项目目录启动 ${agent.name} 终端…`
        : (agent.running ? `正在唤起 ${agent.name}…` : `正在打开 ${agent.name}…`);
      try {
        const result = await api.openAgent(agent.id);
        status.textContent = result.output || `${agent.name} 已打开`;
        await new Promise(resolve => setTimeout(resolve, 1000));
        await refresh();
      } catch (error) {
        status.textContent = `打开失败：${error.message}`;
        button.disabled = false;
      }
    }

    async function delegate(agent, button) {
      const task = taskInput.value.trim();
      if (!task) {
        status.textContent = '先填写要分派的工作和验收要求';
        taskInput.focus();
        return;
      }
      button.disabled = true;
      status.textContent = `正在交给 ${agent.name} 执行…`;
      try {
        const result = await api.delegateAgent(agent.id, task, workDirInput.value.trim());
        status.textContent = `${agent.name}：${result.state || '已返回'}，结果已同步到主对话`;
        showResult(agent.name, result);
      } catch (error) {
        status.textContent = `分派失败：${error.message}`;
      } finally {
        button.disabled = false;
      }
    }

    async function chooseDirectory() {
      const directory = await api.chooseAgentDirectory();
      if (directory) workDirInput.value = directory;
    }

    async function register() {
      const button = document.querySelector('#a2a-add');
      button.disabled = true;
      status.textContent = '正在保存端点并读取 Agent Card…';
      try {
        await api.registerA2A({
          id: document.querySelector('#a2a-id').value.trim(),
          name: document.querySelector('#a2a-name').value.trim(),
          url: document.querySelector('#a2a-url').value.trim(),
          token: document.querySelector('#a2a-token').value,
        });
        for (const id of ['#a2a-id', '#a2a-name', '#a2a-url', '#a2a-token']) document.querySelector(id).value = '';
        await refresh();
      } catch (error) {
        status.textContent = `保存失败：${error.message}`;
      } finally {
        button.disabled = false;
      }
    }

    clarifySend.addEventListener('click', () => sendClarify(clarifyAnswer.value));
    clarifyAnswer.addEventListener('keydown', event => {
      if (event.key !== 'Enter') return;
      event.preventDefault();
      sendClarify(clarifyAnswer.value);
    });

    for (const tab of document.querySelectorAll('[data-filter]')) {
      tab.addEventListener('click', () => {
        filter = tab.dataset.filter;
        for (const item of document.querySelectorAll('[data-filter]')) item.classList.toggle('active', item === tab);
        render();
      });
    }
    document.querySelector('#agents-refresh').addEventListener('click', () => refresh(true));
    document.querySelector('#workdir-choose').addEventListener('click', chooseDirectory);
    document.querySelector('#ccswitch-open').addEventListener('click', () => api.openWindow('ccswitch'));
    document.querySelector('#a2a-add').addEventListener('click', register);
    refresh();
    return { refresh, canDelegate, canOpen, agents: () => agents };
  }

  if (typeof window !== 'undefined' && window.desktopAssistant && document.querySelector('#agent-list')) {
    init({ api: window.desktopAssistant });
  }
  return { init, canDelegate, canOpen, visibleAgents };
}));
