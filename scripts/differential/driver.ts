/** Test-only bridge: all assembly, encoding and decoding stay in production entrypoints. */
import { createInterface } from 'node:readline'
import { AIMessage, HumanMessage, ToolMessage } from '@langchain/core/messages'
import { configureHarness } from '../../packages/harness/host'
import { readHostParts, systemText } from '../../packages/harness/prompt'
import { CONTEXT_DEFAULTS } from '../../packages/harness/context/policy'
import { encodeRequest, readAnswer, answerMessage, REPLAY, type WireRequest } from '../../packages/harness/engines/deepagents/wire'

for await (const line of createInterface({ input: process.stdin })) {
  const input = JSON.parse(line)
  try {
    configureHarness({ brief: () => input.brief, projectInstructions: () => input.projectInstructions, memoryFor: () => input.memory })
    const parts = await readHostParts('query', CONTEXT_DEFAULTS, true, '')
    const prompt = systemText(input.guidance, parts, input.toolsText)
    const messages = input.request.messages.map((m: any) => {
      if (m.role === 'user') return new HumanMessage({ content: m.parts.map((p: any) => p.image ? { type: 'image', image: p.image } : { type: 'text', text: p.text }) })
      if (m.role === 'assistant') return new AIMessage({ content: m.text, tool_calls: m.calls, additional_kwargs: m.replay ? { [REPLAY]: m.replay } : {} })
      return new ToolMessage({ content: m.text, tool_call_id: m.callID, name: m.name, status: m.error ? 'error' : 'success', artifact: { images: m.images ?? [] } })
    })
    const request: WireRequest = { ...input.request, system: { ...input.request.system, head: prompt }, messages }
    const encoded = encodeRequest(request)
    const events: string[][] = []
    const listener = { text: async (s: string) => { events.push(['text', s]) }, reasoning: async (s: string) => { events.push(['reasoning', s]) }, reasoningEnd: async (s: string) => { events.push(['reasoningEnd', s]) } }
    const answer = await readAnswer(request.family, new Response(input.response.body, { headers: { 'content-type': input.response.contentType } }), listener, new AbortController().signal, request.reasoningField, () => 'generated-id')
    const replay = encodeRequest({ ...request, messages: [...messages, answerMessage(answer)] })
    console.log(JSON.stringify({ prompt, encoded, text: answer.text, finishReason: answer.finishReason, usage: answer.usage, events, replay }))
  } catch (error) { throw new Error(input.caseID, { cause: error }) }
}
