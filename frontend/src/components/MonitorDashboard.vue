<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, nextTick } from 'vue'

// --- 类型 ---
interface LogEntry {
  id: number
  timestamp: string
  category: 'system' | 'step' | 'api_request' | 'api_response'
  level: string
  message: string
  meta: Record<string, any>
}

interface LogStats {
  total_entries: number
  by_category: Record<string, number>
  by_level: Record<string, number>
  api_success_rate: number
  total_tokens: number
  llm_call_count: number
  avg_llm_duration_ms: number
}

// --- 状态 ---
const logs = ref<LogEntry[]>([])
const stats = ref<LogStats>({
  total_entries: 0, by_category: {}, by_level: {},
  api_success_rate: 100, total_tokens: 0, llm_call_count: 0, avg_llm_duration_ms: 0
})
const activeCategories = ref<Set<string>>(new Set())
const searchQuery = ref('')
const autoScroll = ref(true)
const paused = ref(false)

let eventSource: EventSource | null = null
let statsTimer: number | null = null

// --- 计算属性 ---
const CATEGORIES = [
  { key: 'system', label: 'SYSTEM', cls: 'cat-system' },
  { key: 'step', label: 'STEP', cls: 'cat-step' },
  { key: 'api_request', label: 'API_REQ', cls: 'cat-apireq' },
  { key: 'api_response', label: 'API_RES', cls: 'cat-apires' },
] as const

const filteredLogs = computed(() => {
  let result = logs.value
  if (activeCategories.value.size > 0) {
    result = result.filter(l => activeCategories.value.has(l.category))
  }
  const q = searchQuery.value.trim().toLowerCase()
  if (q) {
    result = result.filter(l =>
      l.message.toLowerCase().includes(q) ||
      JSON.stringify(l.meta).toLowerCase().includes(q)
    )
  }
  return result
})

const groupedLogs = computed(() => {
  const groups: { time: string; entries: LogEntry[] }[] = []
  for (const entry of filteredLogs.value) {
    const time = entry.timestamp.slice(11, 19) // HH:MM:SS
    const last = groups[groups.length - 1]
    if (last && last.time === time) {
      last.entries.push(entry)
    } else {
      groups.push({ time, entries: [entry] })
    }
  }
  return groups
})

function formatTime(ts: string) {
  return ts.slice(11, 23) // HH:MM:SS.mmm
}

function categoryCls(cat: string) {
  const map: Record<string, string> = {
    system: 'cat-system', step: 'cat-step',
    api_request: 'cat-apireq', api_response: 'cat-apires'
  }
  return map[cat] || ''
}

function levelCls(level: string) {
  return level === 'ERROR' ? 'level-error' : level === 'WARNING' ? 'level-warn' : ''
}

function fmtMeta(meta: Record<string, any>) {
  const parts: string[] = []
  const showKeys = ['method', 'path', 'provider', 'model', 'status_code', 'duration_ms',
    'input_tokens', 'output_tokens', 'task_count', 'complexity', 'domain', 'tool_name']
  for (const k of showKeys) {
    if (meta[k] !== undefined) {
      if (k === 'duration_ms') parts.push(`${meta[k]}ms`)
      else if (k === 'input_tokens' || k === 'output_tokens') parts.push(`${k}=${meta[k]}`)
      else parts.push(String(meta[k]))
    }
  }
  return parts.join(' | ')
}

// --- SSE 连接 ---
function connectSSE() {
  if (eventSource) eventSource.close()

  const catParam = activeCategories.value.size > 0
    ? '?category=' + [...activeCategories.value].join(',')
    : ''

  eventSource = new EventSource('/api/logs/stream' + catParam)

  eventSource.onmessage = (e) => {
    if (paused.value) return
    try {
      const entry = JSON.parse(e.data) as LogEntry
      logs.value.push(entry)
      // 限制前端最大展示 2000 条
      if (logs.value.length > 2000) {
        logs.value = logs.value.slice(-1500)
      }
      if (autoScroll.value) {
        nextTick(() => {
          const el = document.getElementById('monitor-log-area')
          if (el) el.scrollTop = el.scrollHeight
        })
      }
    } catch { /* ignore parse errors */ }
  }

  eventSource.onerror = () => {
    // 5 秒后重连
    setTimeout(() => {
      if (eventSource && eventSource.readyState === EventSource.CLOSED) {
        connectSSE()
      }
    }, 5000)
  }
}

function reloadSSE() {
  logs.value = []
  connectSSE()
  fetchStats()
}

// --- 统计轮询 ---
async function fetchStats() {
  try {
    const res = await fetch('/api/logs/stats')
    stats.value = await res.json()
  } catch { /* ignore */ }
}

// --- 分类切换 ---
function toggleCategory(key: string) {
  if (activeCategories.value.has(key)) {
    activeCategories.value.delete(key)
  } else {
    activeCategories.value.add(key)
  }
  // 重新连接以应用过滤
  reloadSSE()
}

// --- 导出 ---
function exportLogs() {
  const text = filteredLogs.value.map(l =>
    `[${l.timestamp}] [${l.category.toUpperCase()}] [${l.level}] ${l.message} ${JSON.stringify(l.meta)}`
  ).join('\n')
  const blob = new Blob([text], { type: 'text/plain' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = `golden_finger_logs_${new Date().toISOString().slice(0, 19).replace(/:/g, '-')}.txt`
  a.click()
  URL.revokeObjectURL(url)
}

// --- 生命周期 ---
onMounted(() => {
  connectSSE()
  fetchStats()
  statsTimer = window.setInterval(fetchStats, 10000)
})

onUnmounted(() => {
  eventSource?.close()
  if (statsTimer) clearInterval(statsTimer)
})
</script>

<template>
  <div class="monitor-container">
    <!-- 统计卡片 -->
    <div class="monitor-stats">
      <div class="stat-card">
        <div class="stat-val" style="color: var(--accent);">{{ stats.total_entries }}</div>
        <div class="stat-lbl">总日志数</div>
      </div>
      <div class="stat-card">
        <div class="stat-val" :style="{ color: stats.api_success_rate >= 95 ? 'var(--success)' : 'var(--warning)' }">
          {{ stats.api_success_rate }}%
        </div>
        <div class="stat-lbl">API 成功率</div>
      </div>
      <div class="stat-card">
        <div class="stat-val" style="color: var(--warning);">{{ stats.avg_llm_duration_ms }}ms</div>
        <div class="stat-lbl">平均 LLM 延迟</div>
      </div>
      <div class="stat-card">
        <div class="stat-val" style="color: var(--purple);">{{ stats.llm_call_count }}</div>
        <div class="stat-lbl">LLM 调用次数</div>
      </div>
      <div class="stat-card">
        <div class="stat-val" style="color: var(--cyan);">{{ stats.total_tokens.toLocaleString() }}</div>
        <div class="stat-lbl">Token 消耗</div>
      </div>
    </div>

    <!-- 主内容区 -->
    <div class="monitor-main">
      <!-- 日志流 -->
      <div
        id="monitor-log-area"
        class="monitor-log"
        @scroll="autoScroll = false"
      >
        <div v-if="filteredLogs.length === 0" class="log-empty">
          等待日志...
        </div>
        <div
          v-for="group in groupedLogs"
          :key="group.entries[0].id"
          class="log-group"
        >
          <div
            v-for="entry in group.entries"
            :key="entry.id"
            class="log-line"
            :class="levelCls(entry.level)"
          >
            <span class="log-time">{{ formatTime(entry.timestamp) }}</span>
            <span class="log-cat" :class="categoryCls(entry.category)">
              {{ entry.category.toUpperCase().padEnd(10) }}
            </span>
            <span class="log-level" :class="levelCls(entry.level)">
              {{ entry.level.padEnd(5) }}
            </span>
            <span class="log-msg">{{ entry.message }}</span>
            <span v-if="Object.keys(entry.meta).length" class="log-meta">
              {{ fmtMeta(entry.meta) }}
            </span>
          </div>
        </div>
      </div>

      <!-- 侧边栏 -->
      <div class="monitor-sidebar">
        <div class="sidebar-section">
          <div class="sidebar-title">分类筛选</div>
          <div class="filter-chips">
            <button
              v-for="cat in CATEGORIES"
              :key="cat.key"
              class="filter-chip"
              :class="{ active: activeCategories.has(cat.key), [cat.cls]: true }"
              @click="toggleCategory(cat.key)"
            >
              {{ cat.label }}
            </button>
          </div>
        </div>

        <div class="sidebar-section">
          <div class="sidebar-title">搜索</div>
          <input
            v-model="searchQuery"
            type="text"
            class="sidebar-search"
            placeholder="搜索关键词..."
          >
        </div>

        <div class="sidebar-section">
          <div class="sidebar-title">操作</div>
          <div class="sidebar-actions">
            <button class="action-btn" @click="paused = !paused">
              {{ paused ? '▶ 继续' : '⏸ 暂停' }}
            </button>
            <button class="action-btn" @click="autoScroll = true">
              🔗 跟随最新
            </button>
            <button class="action-btn" @click="logs = []; reloadSSE()">
              🗑 清空
            </button>
            <button class="action-btn" @click="exportLogs">
              📥 导出
            </button>
          </div>
        </div>

        <div class="sidebar-section">
          <div class="sidebar-title">分类统计</div>
          <div class="sidebar-stats">
            <div v-for="cat in CATEGORIES" :key="cat.key" class="sidebar-stat-row">
              <span :class="cat.cls" style="font-size:11px;font-weight:600;">{{ cat.label }}</span>
              <span style="color:var(--text-dim);">{{ stats.by_category[cat.key] || 0 }}</span>
            </div>
            <div class="sidebar-stat-row" style="margin-top:6px;border-top:1px solid var(--border);padding-top:6px;">
              <span style="color:var(--error);font-size:11px;font-weight:600;">ERROR</span>
              <span style="color:var(--error);">{{ stats.by_level['ERROR'] || 0 }}</span>
            </div>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>
