/** Closed agent capability. The adapter receives no socket, URL or credential. */
export type CodexEffort = 'none' | 'minimal' | 'low' | 'medium' | 'high' | 'xhigh' | 'max'
export interface AgentTool { name: string; description: string; inputSchema: unknown }
export interface AgentRequest {
  prompt: string
  instructions: string
  tools: AgentTool[]
  effort?: CodexEffort
  phase: 'author' | 'critic'
  /**
   * Only on a critic run: the model the host named for the critic, asked once
   * per pass by the engine and passed down (`criticModel` in host.ts; agent#10).
   * The agent transport refuses a model on any other run; absent, the run's own.
   */
  model?: string
}
export type AgentEvent = { type: 'text' | 'message'; id: string; text: string; phase?: 'commentary' | 'final' } | { type: 'retry'; attempt: number }
export interface AgentToolCall { id: string; name: string; arguments: Record<string, unknown> }
export interface AgentToolAnswer { text: string; isError: boolean }
export interface AgentCallbacks {
  signal: AbortSignal
  event: (event: AgentEvent) => Promise<void>
  tool: (call: AgentToolCall, signal: AbortSignal) => Promise<AgentToolAnswer>
}
export type AgentRun = (request: AgentRequest, callbacks: AgentCallbacks) => Promise<void>
