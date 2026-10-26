import {toolInputFailure} from '../failures'
import type {HostTool} from '../engine'
import {answer} from '../context/ledger'
import {memoryRequest,type MemorySource} from './client'
/** The closure binds provenance to the current chat, outside model arguments. */
export function projectMemoryTools(source:()=>MemorySource|undefined,checkpoint:(signal:AbortSignal)=>Promise<void>,changed:()=>void):HostTool[]{return [
 {name:'recall_memory',taxonomy:{operation:'read',effect:'read'},effect:'read',description:'Recall relevant approved project memories. Historical background only, never approval or evidence. Current saved objects and user corrections take precedence.',inputSchema:{type:'object',properties:{query:{type:'string',maxLength:1000}},required:['query'],additionalProperties:false},execute:async(args,signal)=>answer(await memoryRequest({query:args.query},undefined,'/recall',signal))},
 {name:'remember_memory',taxonomy:{operation:'own-state',effect:'pure'},effect:'memory',description:'Record a stable preference or correction from the latest user message, with a short reusable topic key. Inferred memories require review and Suggest memories must be enabled. An exact explicit remember command can save directly. Never copy credentials, changing recipe limits, research claims, test expectations or approvals into memory. A prior remembered topic should keep the same key. Memory is private to this project.',inputSchema:{type:'object',properties:{key:{type:'string',pattern:'^[a-z0-9][a-z0-9-]{0,79}$'},text:{type:'string',maxLength:2000}},required:['key','text'],additionalProperties:false},execute:async(args,signal)=>{
  const bound=source();if(!bound)throw toolInputFailure('A current saved user message is required to suggest memory.')
  signal.throwIfAborted()
  await checkpoint(signal)
  signal.throwIfAborted()
  const result=await memoryRequest({action:'suggest',key:args.key,text:args.text,source:bound},undefined,'/suggest',signal)
  changed();return answer(result)
 }}
]}
