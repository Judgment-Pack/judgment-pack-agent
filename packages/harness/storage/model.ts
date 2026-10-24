import type {EndpointKind} from '../host'
import {AIFailureError,failure,normalizeFailure} from '../failures'
import {isEventStream,withAbort} from '../engines/contract'

/** Private response bytes are quarantined in memory, never persisted or used as account-release proof. */
export const DURABLE_MODEL_BYTES=4*1024*1024
const incomplete=()=>new AIFailureError({...failure('invalid-response','model','unknown'),detail:'The durable model response has no verified complete terminal outcome.'})
const object=(value:unknown):value is Record<string,any>=>value!==null&&typeof value==='object'&&!Array.isArray(value)
const reasons={
 'openai-compatible':['stop','length','tool_calls','function_call','content_filter'],
 anthropic:['end_turn','stop_sequence','pause_turn','max_tokens','model_context_window_exceeded','tool_use','refusal'],
 gemini:['STOP','MAX_TOKENS','SAFETY','RECITATION','BLOCKLIST','PROHIBITED_CONTENT','SPII','IMAGE_SAFETY']
} as const
/** Positive protocol evidence, independent of EOF and the SDK's synthesized finish events. */
export function verifyModelTerminal(family:EndpointKind,streamed:boolean,text:string):void{
 let terminal=false,marker=false,started=false
 const blocks=new Set<number>()
 const take=(value:unknown,event='')=>{
  if(!object(value)||value.error!==undefined&&value.error!==null)throw incomplete()
  if(family==='openai-compatible'){
   const choices=Array.isArray(value.choices)?value.choices:[]
   if(choices.length>1)throw incomplete()
   if(choices.length===0)return
   const choice=choices[0]
   if(!object(choice)||choice.index!==undefined&&choice.index!==0||terminal&&object(choice.delta)&&Object.keys(choice.delta).length>0)throw incomplete()
   if(choice.finish_reason!==undefined&&choice.finish_reason!==null){if(terminal||!reasons[family].includes(choice.finish_reason))throw incomplete();terminal=true}
  }else if(family==='anthropic'){
   if(!streamed){if(!Array.isArray(value.content)||!reasons[family].includes(value.stop_reason))throw incomplete();terminal=true;marker=true;return}
   const kind=value.type??event,index=value.index
   if(terminal&&['message_start','content_block_start','content_block_delta','content_block_stop'].includes(kind))throw incomplete()
   if(kind==='error')throw incomplete()
   if(kind==='message_start'){if(started)throw incomplete();started=true}
   else if(kind==='content_block_start'){if(!Number.isSafeInteger(index)||index<0||index>2**20||blocks.has(index))throw incomplete();blocks.add(index)}
   else if(kind==='content_block_delta'){if(!blocks.has(index))throw incomplete()}
   else if(kind==='content_block_stop'){if(!blocks.delete(index))throw incomplete()}
   else if(kind==='message_delta'){
    const reason=value.delta?.stop_reason
    if(reason!==undefined&&reason!==null){if(terminal||!reasons[family].includes(reason))throw incomplete();terminal=true}
   }else if(kind==='message_stop'){if(!started||!terminal||blocks.size!==0)throw incomplete();marker=true}
   else if(kind!=='ping')throw incomplete()
  }else if(family==='gemini'){
   if(typeof value.promptFeedback?.blockReason==='string'&&value.promptFeedback.blockReason!=='')terminal=true
   const candidates=Array.isArray(value.candidates)?value.candidates:[]
   if(candidates.length>1)throw incomplete()
   if(candidates.length===0)return
   const candidate=candidates[0]
   if(!object(candidate)||terminal&&candidate.content?.parts?.length>0)throw incomplete()
   if(candidate.finishReason!==undefined&&candidate.finishReason!==null){if(terminal||!reasons[family].includes(candidate.finishReason))throw incomplete();terminal=true}
  }else throw incomplete()
 }
 const parse=(raw:string):unknown=>{try{return JSON.parse(raw)}catch{throw incomplete()}}
 if(streamed){
  let event='',data:string[]=[]
  const flush=()=>{
   if(data.length===0)return
   if(marker)throw incomplete()
   const raw=data.join('\n');data=[]
   if(family==='openai-compatible'&&raw==='[DONE]'){if(!terminal)throw incomplete();marker=true;return}
   take(parse(raw),event)
  }
  for(const line of text.replace(/\r\n/g,'\n').split('\n')){
   if(line===''){flush();event=''}
   else if(line.startsWith('data:'))data.push(line.slice(5).replace(/^ /,''))
   else if(line.startsWith('event:'))event=line.slice(6).trim()
  }
  flush()
 }else{
  const value=parse(text)
  if(family==='gemini'&&Array.isArray(value))for(const item of value)take(item)
  else take(value)
 }
 if(!terminal||streamed&&(family==='openai-compatible'||family==='anthropic')&&!marker)throw incomplete()
}

/** Read the original bounded response before returning anything executable to either SDK. */
export async function quarantineModelResponse(family:EndpointKind,response:Response,signal:AbortSignal):Promise<{response:Response;refusal?:unknown}>{
 if(response.body===null)throw incomplete()
 const reader=response.body.getReader(),chunks:Uint8Array[]=[]
 let length=0
 const stop=()=>void reader.cancel().catch(()=>undefined)
 signal.addEventListener('abort',stop,{once:true})
 try{
  for(;;){
   signal.throwIfAborted()
   const step=await withAbort(()=>reader.read(),signal)
   if(step.done)break
   length+=step.value.byteLength
   if(length>DURABLE_MODEL_BYTES)throw incomplete()
   chunks.push(step.value.slice())
  }
  signal.throwIfAborted()
 }finally{
  signal.removeEventListener('abort',stop)
  void reader.cancel().catch(()=>undefined)
  reader.releaseLock()
 }
 const bytes=new Uint8Array(length);let offset=0
 for(const chunk of chunks){bytes.set(chunk,offset);offset+=chunk.byteLength}
 let text:string;try{text=new TextDecoder('utf-8',{fatal:true}).decode(bytes)}catch{throw incomplete()}
 if(response.ok)verifyModelTerminal(family,isEventStream(response),text)
 else{let value:unknown;try{value=JSON.parse(text)}catch{throw incomplete()};if(!object(value))throw incomplete()}
 const replay=new Response(length?bytes:null,{status:response.status,statusText:response.statusText,headers:new Headers(response.headers)})
 signal.throwIfAborted()
 return {response:replay,...(response.ok?{}:{refusal:new AIFailureError(normalizeFailure({statusCode:response.status,responseBody:text,responseHeaders:response.headers},'model'))})}
}
