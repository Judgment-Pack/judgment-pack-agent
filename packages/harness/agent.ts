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
   * Only on a critic run or a delegated child's run: the model the host named
   * for the critic or for children (`modelsFor` in host.ts, asked once per run;
   * agent#10, agent#15), passed down by the engine or the delegation
   * coordinator. The agent transport refuses a model on any other run; absent,
   * the run's own.
   */
  model?: string
  /** A delegated child's run, marked by the delegation coordinator (agent#15); never sent to the bridge. */
  delegated?: true
}
export type AgentEvent = { type: 'text' | 'message'; id: string; text: string; phase?: 'commentary' | 'final' } | { type: 'retry'; attempt: number }
export interface AgentToolCall { id: string; name: string; arguments: Record<string, unknown> }
export interface AgentToolAnswer { text: string; isError: boolean }
export interface AgentCallbacks {
  /** Live invocation-scoped host release acknowledgement; never serialized or inferred from a settled promise. */
  providerRelease?:import('./storage/release').ProviderRelease
  signal: AbortSignal
  event: (event: AgentEvent) => Promise<void>
  tool: (call: AgentToolCall, signal: AbortSignal) => Promise<AgentToolAnswer>
  /** Harness-internal delivery seam: a failover event is written before the replacement dispatch. */
  connectionSwitched?: (event: {type:'connection_switched';role:string;from:string;to:string;reason:'unavailable'|'rate-limited'|'timeout'}) => Promise<void>
}
export type AgentRun = (request: AgentRequest, callbacks: AgentCallbacks) => Promise<void>
