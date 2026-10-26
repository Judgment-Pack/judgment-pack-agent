import {readUsageJSON,usageJSONCount} from './usageJSON'
import {UsageBudgetUnknown,type UsageAmounts} from './usageBudget'
const object=(v:unknown):v is Record<string,unknown>=>!!v&&typeof v==='object'&&!Array.isArray(v)
/** Only called after canonical bounded terminal quarantine, before the SDK receives response bytes.
 * Provider totals can repeat in streamed snapshots: the last explicit figure replaces the prior one.
 * No total/reasoning/cache conversion; omitted input/output stays omitted. */
export async function usageFromVerifiedResponse(family:string,response:Response):Promise<UsageAmounts>{
 if(!response.ok)return {}
 const text=await response.clone().text(),out:UsageAmounts={}
 const copy=(from:unknown,input:string,output:string)=>{
  if(!object(from))return
  for(const [source,target] of [[input,'inputTokens'],[output,'outputTokens']] as const)if(Object.hasOwn(from,source)){
   try{out[target]=usageJSONCount(from[source])}catch{throw new UsageBudgetUnknown()}
  }
 }
 const take=(value:unknown)=>{
  if(!object(value))return
  if(family==='openai-compatible')copy(value.usage,'prompt_tokens','completion_tokens')
  else if(family==='anthropic'){copy(object(value.message)?value.message.usage:undefined,'input_tokens','output_tokens');copy(value.usage,'input_tokens','output_tokens')}
  else if(family==='gemini')copy(value.usageMetadata,'promptTokenCount','candidatesTokenCount')
  else throw new UsageBudgetUnknown()
 }
 try{
  if(response.headers.get('content-type')?.includes('text/event-stream')){
   let data:string[]=[]
   const flush=()=>{if(data.length){const raw=data.join('\n');data=[];if(raw!=='[DONE]')take(readUsageJSON(raw))}}
   for(const line of text.replace(/\r\n/g,'\n').split('\n')){if(line==='')flush();else if(line.startsWith('data:'))data.push(line.slice(5).replace(/^ /,''))}flush()
  }else{const value:unknown=readUsageJSON(text);if(Array.isArray(value))for(const row of value)take(row);else take(value)}
 }catch{throw new UsageBudgetUnknown()}
 return out
}
