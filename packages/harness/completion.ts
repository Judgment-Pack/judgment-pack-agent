/** Provider-neutral completion metadata. Never retain raw provider text or reasoning. */
export const COMPLETION_REASONS = ['stop','length','content-filter','tool-calls','error','other'] as const
export interface ModelCompletion {
 finishReason:typeof COMPLETION_REASONS[number]
 inputTokens?:number
 outputTokens?:number
 reasoningTokens?:number
}
const object=(v:unknown):v is Record<string,unknown>=>!!v&&typeof v==='object'&&!Array.isArray(v)
const tokens=(v:unknown):v is number=>Number.isSafeInteger(v)&&Number(v)>=0
export function readCompletion(value:unknown):ModelCompletion|undefined {
 if(!object(value)||!COMPLETION_REASONS.includes(value.finishReason as ModelCompletion['finishReason']))return
 for(const key of ['inputTokens','outputTokens','reasoningTokens'])if(value[key]!==undefined&&!tokens(value[key]))return
 return {finishReason:value.finishReason as ModelCompletion['finishReason'],...Object.fromEntries(['inputTokens','outputTokens','reasoningTokens'].filter(key=>value[key]!==undefined).map(key=>[key,value[key]]))}
}
/** SDK adapters translate into this contract; future providers use the same policy. */
export function modelCompletion(finishReason:unknown,usage:unknown):ModelCompletion {
 const data=object(usage)?usage:{},output=object(data.outputTokenDetails)?data.outputTokenDetails:{}
 return readCompletion({finishReason:COMPLETION_REASONS.includes(finishReason as ModelCompletion['finishReason'])?finishReason:'other',...Object.fromEntries(Object.entries({inputTokens:data.inputTokens,outputTokens:data.outputTokens,reasoningTokens:output.reasoningTokens}).filter(([,value])=>tokens(value)))})!
}
/** A new, tool-free synthesis request, never a retry of tools or uncertain transport. */
export function canFinishResponse(completion:ModelCompletion|undefined,text:string,toolCalls:number,remaining:number,attempted:boolean):boolean {
 return !attempted&&remaining>0&&toolCalls===0&&(completion?.finishReason==='length'||completion?.finishReason==='stop'&&!text.trim())
}
export function completionFailureCode(completion:ModelCompletion|undefined):'output-limit'|'content-filter'|'invalid-response' {
 return completion?.finishReason==='length'?'output-limit':completion?.finishReason==='content-filter'?'content-filter':'invalid-response'
}
export const FINISH_RESPONSE_INSTRUCTIONS = 'The previous model response ended without a complete answer. Complete the current response using only the supplied request and completed results. Tools are unavailable. Do not restart discovery, repeat actions, or claim an unsaved change or an unperformed check succeeded. Keep the answer concise. If a complete supported proposal is not possible, explain the verified findings and the exact remaining work. Return a complete answer, not a continuation of a partial JSON block.'
