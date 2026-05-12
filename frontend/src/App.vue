<script setup lang="ts">
import { ref, nextTick } from 'vue'
import { marked } from 'marked'
import { useSSE, type SSEEvent } from './composables/useSSE'
import MonitorDashboard from './components/MonitorDashboard.vue'

interface LogEntry {
  id: number
  type: 'info' | 'success' | 'error' | 'tool-start' | 'tool-end' | 'markdown'
  text: string
  category?: string
}

const currentPage = ref<'chat' | 'monitor'>('chat')
const logEntries = ref<LogEntry[]>([])
const inputQuery = ref('')
const isRunning = ref(false)
const finalMarkdown = ref('')
const hostStatus = ref({
  soul_mark: '...',
  realm: '...',
  realm_stage: '',
  total_tasks: 0,
})

let entryId = 0

function addLog(type: LogEntry['type'], text: string, category?: string) {
  logEntries.value.push({ id: ++entryId, type, text, category })
  nextTick(() => {
    const el = document.getElementById('log-area')
    if (el) el.scrollTop = el.scrollHeight
  })
}

const { sendQuery } = useSSE()

async function handleSubmit() {
  const q = inputQuery.value.trim()
  if (!q || isRunning.value) return

  inputQuery.value = ''
  isRunning.value = true
  logEntries.value = []
  finalMarkdown.value = ''

  addLog('info', `宿主> ${q}`)
  addLog('info', '')

  try {
    await sendQuery(q, (event: SSEEvent, eventType: string) => {
      const { status } = event

      if (eventType === 'analysis_started') {
        addLog('info', '🔮 天机推演中...', 'analysis')
      } else if (eventType === 'analysis_completed' && event.plan) {
        const n = event.plan.tasks.length
        addLog('success', `✓ 拆解为 ${n} 个原子任务`, 'analysis')
        for (const t of event.plan.tasks) {
          addLog('info', `  └ [${t.matched_skill || 'general'}] ${t.description}`, 'analysis')
        }
      } else if (eventType === 'analysis_error') {
        addLog('error', `✗ 天机推演失败: ${event.error}`, 'analysis')

      } else if (eventType === 'execution_started') {
        addLog('info', '⚡ 施法执行中...', 'execution')
      } else if (eventType === 'execution_tool_call' && event.tool_event) {
        const te = event.tool_event
        if (te.phase === 'tool_start') {
          const paramsStr = Object.entries(te.params)
            .map(([k, v]) => `${k}=${JSON.stringify(v).slice(0, 60)}`)
            .join(', ')
          addLog('tool-start', `⚙ ${te.tool_name}(${paramsStr})`, 'execution')
        } else if (te.phase === 'tool_end') {
          if (te.error) {
            addLog('error', `  ✗ 失败 (${te.duration_ms}ms): ${te.error}`, 'execution')
          } else {
            const preview = typeof te.result === 'string'
              ? te.result.slice(0, 200).replace(/\n/g, ' ')
              : JSON.stringify(te.result).slice(0, 200)
            addLog('tool-end', `  ✓ 成功 (${te.duration_ms}ms): ${preview}`, 'execution')
          }
        }
      } else if (eventType === 'execution_completed') {
        addLog('success', `✓ 执行完成 (${event.total_duration_ms}ms)`, 'execution')
        if (event.anomalies?.length) {
          for (const a of event.anomalies) {
            addLog('error', `  ⚠ ${a}`, 'execution')
          }
        }
      } else if (eventType === 'execution_error') {
        addLog('error', `✗ 执行失败: ${event.error}`, 'execution')

      } else if (eventType === 'verification_started') {
        addLog('info', '🔍 验道校验中...', 'verification')
      } else if (eventType === 'verification_completed') {
        const passed = event.overall_pass
        addLog(passed ? 'success' : 'error',
          `✓ 校验结果: ${passed ? '通过' : '未通过 → ' + event.action}`,
          'verification')
        if (event.failed_checks?.length) {
          for (const c of event.failed_checks) {
            addLog('error', `  ✗ ${c.name}: ${c.detail}`, 'verification')
          }
        }
      } else if (eventType === 'verification_error') {
        addLog('error', `✗ 校验失败: ${event.error}`, 'verification')

      } else if (eventType === 'persistence_started') {
        addLog('info', '📝 刻碑沉淀中...', 'persistence')
      } else if (eventType === 'persistence_completed') {
        addLog('success', '✓ 经验已沉淀', 'persistence')

      } else if (eventType === 'complete_done' || eventType === 'done') {
        // ignore
      } else if (eventType === 'error' || status === 'error') {
        addLog('error', `走火入魔: ${event.error || event}`, 'system')
      }

      // Handle final text
      if (event.final_text) {
        finalMarkdown.value = marked.parse(event.final_text) as string
      }
    })
  } catch (e: unknown) {
    addLog('error', `走火入魔: ${e}`)
  } finally {
    isRunning.value = false
  }
}

async function copySelectedText() {
  const selected = (window.getSelection?.()?.toString() || '').trim()
  if (!selected) return
  await navigator.clipboard.writeText(selected)
  addLog('success', '已复制选中文本')
}

async function copyFinalOutput() {
  const text = (finalMarkdown.value || '').replace(/<[^>]+>/g, '').trim()
  if (!text) return
  await navigator.clipboard.writeText(text)
  addLog('success', '已复制修炼成果')
}

// Load status on mount
async function loadStatus() {
  try {
    const res = await fetch('/api/status')
    const data = await res.json()
    hostStatus.value = {
      soul_mark: data.soul_mark || '...',
      realm: data.realm || '...',
      realm_stage: data.realm_stage || '',
      total_tasks: data.total_tasks || 0,
    }
  } catch {
    // ignore
  }
}

loadStatus()
</script>

<template>
  <div class="app-container">
    <!-- Header -->
    <header class="app-header">
      <div class="title-wrap">
        <span class="app-title">✦ 金指 Agent System</span>
        <span class="app-subtitle">Cyber Cultivation Console</span>
      </div>
      <div class="header-right">
        <button class="header-nav-btn" @click="currentPage = 'chat'">
          💬 对话
        </button>
        <button class="header-nav-btn" @click="currentPage = 'monitor'">
          📊 监控
        </button>
        <span class="app-status">
          宿主: {{ hostStatus.soul_mark?.slice(0, 8) }} |
          {{ hostStatus.realm }} {{ hostStatus.realm_stage }} |
          已完成: {{ hostStatus.total_tasks }}
        </span>
      </div>
    </header>

    <!-- Monitor Dashboard Page -->
    <MonitorDashboard v-if="currentPage === 'monitor'" />

    <!-- Main content (Chat Page) -->
    <main v-else class="app-main">
      <div class="chat-toolbar">
        <button class="toolbar-btn" @click="copySelectedText">
          📋 复制选中
        </button>
        <button class="toolbar-btn" @click="copyFinalOutput">
          🧾 复制结果
        </button>
      </div>
      <!-- Log area -->
      <div id="log-area" class="log-area">
        <div
          v-for="entry in logEntries"
          :key="entry.id"
          class="log-entry"
          :class="entry.type"
        >
          <span
            v-if="entry.type === 'error'"
            class="log-icon"
          >✗</span>
          <span
            v-else-if="entry.type === 'success'"
            class="log-icon"
          >✓</span>
          <span
            v-else-if="entry.type === 'tool-start'"
            class="log-icon"
          >⚙</span>
          <span
            v-else-if="entry.type === 'tool-end'"
            class="log-icon"
          >✓</span>
          <span class="log-text">{{ entry.text }}</span>
        </div>
        <div
          v-if="isRunning"
          class="log-entry info"
        >
          <span class="spinner" />
        </div>
      </div>

      <!-- Final output -->
      <div
        v-if="finalMarkdown"
        class="final-output"
      >
        <div class="final-output-label">📄 修炼成果</div>
        <div
          class="markdown-body"
          v-html="finalMarkdown"
        />
      </div>
    </main>

    <!-- Input area -->
    <footer class="input-area">
      <span class="input-prompt">宿主&gt;</span>
      <input
        v-model="inputQuery"
        type="text"
        class="query-input"
        placeholder="输入你的问题，按 Enter 开始修炼..."
        :disabled="isRunning"
        @keydown.enter="handleSubmit()"
      >
    </footer>
  </div>
</template>
