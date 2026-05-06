export interface SSEEvent {
  domain: string
  status: string
  plan?: {
    plan_id: string
    complexity: string
    tasks: Array<{
      task_id: string
      description: string
      matched_skill: string | null
      depends_on: string[]
    }>
    execution_order: string[][]
  }
  tool_event?: {
    phase: string
    task_id: string
    tool_name: string
    params: Record<string, unknown>
    result: unknown
    error: string | null
    duration_ms: number
  }
  total_duration_ms?: number
  anomalies?: string[]
  overall_pass?: boolean
  action?: string
  failed_checks?: Array<{ name: string; detail: string }>
  final_text?: string
  error?: string
}

export type EventCallback = (event: SSEEvent, eventType: string) => void

export function useSSE() {
  let abortController: AbortController | null = null

  async function sendQuery(
    query: string,
    onEvent: EventCallback,
  ): Promise<void> {
    abortController = new AbortController()

    try {
      const response = await fetch('/api/query', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ query }),
        signal: abortController.signal,
      })

      if (!response.ok) {
        throw new Error(`HTTP ${response.status}`)
      }

      const reader = response.body?.getReader()
      if (!reader) throw new Error('No readable stream')

      const decoder = new TextDecoder()
      let buffer = ''

      while (true) {
        const { done, value } = await reader.read()
        if (done) break

        buffer += decoder.decode(value, { stream: true })
        const lines = buffer.split('\n')
        buffer = lines.pop() || ''

        let eventType = ''
        let dataStr = ''

        for (const line of lines) {
          if (line.startsWith('event: ')) {
            eventType = line.slice(7).trim()
          } else if (line.startsWith('data: ')) {
            dataStr = line.slice(6).trim()
          } else if (line === '' && dataStr) {
            try {
              const data = JSON.parse(dataStr) as SSEEvent
              onEvent(data, eventType)
            } catch {
              // skip malformed JSON
            }
            eventType = ''
            dataStr = ''
          }
        }
      }
    } catch (err: unknown) {
      if (err instanceof DOMException && err.name === 'AbortError') return
      throw err
    }
  }

  function cancel() {
    abortController?.abort()
    abortController = null
  }

  return { sendQuery, cancel }
}
