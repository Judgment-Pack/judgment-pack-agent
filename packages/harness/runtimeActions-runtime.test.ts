import {describe,it,expect} from 'vitest'
import {createHash,randomUUID} from 'node:crypto'
import {existsSync,mkdirSync,mkdtempSync,readFileSync,readlinkSync,readdirSync,writeFileSync} from 'node:fs'
import {isAbsolute,join,resolve} from 'node:path'
import {StdioClientTransport} from '@modelcontextprotocol/sdk/client/stdio.js'
import type {Transport} from '@modelcontextprotocol/sdk/shared/transport.js'
import type {JSONRPCMessage} from '@modelcontextprotocol/sdk/types.js'
import {openAssistantConnection} from './session'
import {vercel} from './engines/vercel'
import {MemoryRunStore,type RunStore} from './storage/run'
import type {RuntimeExecutionOwner} from './runtimeActions'
import type {AssistantEvent,EngineSession} from './engine'
import {answerWith} from './testing/scenarioDriver'
import {readExecution} from './execution/state'

// Opt-in actual producer evidence. The normal protocol fixture remains in runtimeActions.test.ts.
// Input is the hand-authored Runtime fixture internal/evaluation/testdata/data-request-intake-triage.json;
// these facts select its clarify-return rule. Neither arguments nor expected disposition come from a run.
const enabled=Boolean(process.env.JP_RUNTIME_BINARY)
const hash=(value:string|Buffer)=>createHash('sha256').update(value).digest('hex')
const required=(key:string)=>{const value=process.env[key];if(!value||!isAbsolute(value))throw Error(`An absolute ${key} is required for the session-owned probe.`);return resolve(value)}
const facts='{"request":{"type":"one-time-extract","completeness":"incomplete","appropriateness":"pass","embargoedInformationToUnauthorizedRecipients":false}}'
const evidence='{"intake-form":"present","sponsor-endorsement":"present"}'

describe('actual Runtime producer through exported Agent approval',()=>{
 for(const uncertain of [false,true])it.skipIf(!enabled)(uncertain?'never retries an actual dispatch whose result acknowledgement is lost':'acknowledges a separate decision before one actual rehearsal',async()=>{
  const binary=required('JP_RUNTIME_BINARY'),root=required('JP_RUNTIME_PROBE_ROOT'),temporary=required('TMPDIR')
  const expectedBinary=process.env.JP_RUNTIME_BINARY_SHA256
  expect(expectedBinary).toMatch(/^[0-9a-f]{64}$/)
  expect(hash(readFileSync(binary))).toBe(expectedBinary)
  const pack=readFileSync(required('JP_RUNTIME_PACK'),'utf8')
  expect(hash(pack)).toBe('5bdf53e5c283b5a7c7a04373cfa66a488b021c1e098254424c0de7a963ea8aca')
  const project=mkdtempSync(join(root,uncertain?'uncertain-':'confirmed-')),audit=join(project,'audit'),config=join(project,'jpack.json')
  mkdirSync(audit,{mode:0o700})
  const configText='{"configVersion":"3","audit":{"dir":"audit"},"packs":{}}\n'
  writeFileSync(config,configText,{mode:0o600})
  const raw={pack,facts,evidence,rehearsal:false},exact={pack,facts,evidence,rehearsal:true}
  const frames:JSONRPCMessage[]=[],replies:JSONRPCMessage[]=[],order:string[]=[],notices:AssistantEvent[]=[]
  // env -i execs the exact binary with only these two session paths; no home or cwd fallback.
  const command='/usr/bin/env',args=['-i',`JPACK_CONFIG=${config}`,`TMPDIR=${temporary}`,binary,'mcp']
  const stdio=new StdioClientTransport({command,args,cwd:project,stderr:'pipe'})
  const transport:Transport={start:async()=>{},send:async message=>{
   const frame=message as {method?:string;params?:{name?:string;arguments?:unknown}}
   if(frame.method==='tools/call'){
    expect(frame.params).toEqual({name:'experimental_evaluate',arguments:exact})
    expect(order).toContain('asked-ack');expect(order).toContain('approved-ack')
    expect(frames.filter(f=>(f as {method?:string}).method==='tools/call')).toHaveLength(0)
    order.push('physical-dispatch')
   }
   frames.push(structuredClone(message));await stdio.send(message)
  },close:()=>stdio.close()}
  stdio.onmessage=message=>{replies.push(structuredClone(message));transport.onmessage?.(message)}
  stdio.onerror=error=>transport.onerror?.(error)
  stdio.onclose=()=>transport.onclose?.()
  let stderr='';stdio.stderr?.on('data',chunk=>{stderr+=String(chunk);if(stderr.length>16384)stderr=stderr.slice(-16384)})
  await stdio.start()
  const pid=stdio.pid!;expect(pid).toBeGreaterThan(0)
  const launch=randomUUID(),uid=process.getuid!()
  const owner:RuntimeExecutionOwner={artifactDigest:'sha256:'+expectedBinary,processId:`pid-${pid}-${launch}`,projectId:launch,projectLabel:'Bounded synthetic intake project',sessionId:launch+'-stdio',principalDigest:'sha256:'+hash(`uid:${uid}`),principalLabel:'Local probe owner',scopeDigest:'sha256:'+hash(configText),scopeLabel:'Synthetic rehearsal in the held project'}
  const observe=async()=>{
   expect(stdio.pid).toBe(pid)
   expect(readlinkSync(`/proc/${pid}/exe`)).toBe(binary)
   expect(hash(readFileSync(`/proc/${pid}/exe`))).toBe(expectedBinary)
   expect(readlinkSync(`/proc/${pid}/cwd`)).toBe(project)
   expect(readFileSync(`/proc/${pid}/environ`,'utf8').split('\0').filter(Boolean).sort()).toEqual([`JPACK_CONFIG=${config}`,`TMPDIR=${temporary}`].sort())
   expect(readFileSync(`/proc/${pid}/status`,'utf8')).toMatch(new RegExp(`Uid:\\s+${uid}\\s+${uid}\\s+${uid}\\s+${uid}`))
   expect(readFileSync(config,'utf8')).toBe(configText)
   return {...owner}
  }
  const abort=new AbortController(),timer=setTimeout(()=>abort.abort(),15000)
  const connection=openAssistantConnection({allowed:['experimental_evaluate'],transport,runtimeActions:{owner,current:observe},onEvent:event=>notices.push(event),signal:abort.signal})
  const memory=new MemoryRunStore(),runId='actual-runtime-'+launch
  let lostResult=false
  const store:RunStore={durability:'memory',load:memory.load.bind(memory),readEvents:memory.readEvents.bind(memory),commit:async request=>{
   const checkpoint=JSON.parse(new TextDecoder().decode(request.checkpoint!))
   if(uncertain&&checkpoint.durable?.dispatches?.some((d:{kind:string;state:string})=>d.kind==='runtime'&&d.state==='result')){lostResult=true;order.push('lost-result-ack');throw Error('bounded lost result acknowledgement')}
   const receipt=await memory.commit(request)
   if(checkpoint.approval?.state==='asked')order.push('asked-ack')
   if(checkpoint.approval?.state==='approved')order.push('approved-ack')
   return receipt
  }}
  const events:AssistantEvent[][]=[]
  try{
   const ready=await connection.ready
   expect(ready.tools).toHaveLength(1);expect(ready.tools[0]!._meta).toEqual({operation:'evaluate',effect:'act',experimental:true})
   const collect=async(resume=false,decision?:{callId:string;approved:boolean})=>{
    let requests=0
    const session:EngineSession={prompt:'Rehearse the synthetic intake decision.',testPrompt:'',tools:ready.tools,callTool:ready.callTool,hostTools:[],signal:abort.signal,allowConversation:true,critic:'off',context:{currentRequest:{message:'Rehearse the synthetic intake decision.',input:'fixed synthetic input'},runStore:{store,id:runId,...(resume?{resume:true}:{})},...(decision?{approval:decision}:{})},model:{family:'openai-compatible',model:'deterministic-actual-producer-probe',call:async()=>answerWith(!resume&&requests++===0?{call:{tool:'experimental_evaluate',arguments:raw}}:{say:'The synthetic rehearsal completed.'},'stream',requests-1)}}
    const out:AssistantEvent[]=[];for await(const event of vercel.start(session))out.push(event);events.push(out);return out
   }
   const first=await collect(),asked=first.find(e=>e.type==='approval_requested')
   expect(first.filter(e=>e.type==='error')).toEqual([])
   expect(asked).toMatchObject({type:'approval_requested',args:exact,runtime:{owner}})
   if(!asked||asked.type!=='approval_requested')throw Error('No acknowledged Runtime ask')
   expect(frames.filter(f=>(f as {method?:string}).method==='tools/call')).toHaveLength(0)
   const paused=readExecution(JSON.parse(new TextDecoder().decode((await store.load(runId))!.checkpoint!)))!
   expect(paused.status).toBe('paused');expect(paused.approval?.state).toBe('asked')
   const second=await collect(true,{callId:asked.callId,approved:true})
   const calls=frames.filter(f=>(f as {method?:string}).method==='tools/call')
   expect(calls).toHaveLength(1)
   const call=calls[0] as {id:string|number}
   const actual=replies.find(r=>'id'in r&&r.id===call.id) as {result?:{isError?:boolean;structuredContent?:Record<string,unknown>}}
   expect(actual.result?.isError).not.toBe(true)
   expect(actual.result?.structuredContent).toMatchObject({disposition:{kind:'outcome',outcomeId:'clarify-return'}})
   expect(readdirSync(audit)).toEqual([])
   expect(order.indexOf('asked-ack')).toBeLessThan(order.indexOf('approved-ack'))
   expect(order.indexOf('approved-ack')).toBeLessThan(order.indexOf('physical-dispatch'))
   if(!uncertain){expect(second.filter(e=>e.type==='error')).toEqual([]);expect(lostResult).toBe(false)}
   else expect(lostResult).toBe(true)
   await collect(true)
   expect(frames.filter(f=>(f as {method?:string}).method==='tools/call')).toHaveLength(1)
   expect(readdirSync(audit)).toEqual([])
   writeFileSync(join(project,'evidence.json'),JSON.stringify({binary,binarySha256:expectedBinary,packSha256:hash(pack),pid,command,args,owner,uncertain,order,frames,replies,events,notices,stderr,publishedArtifactProof:false},null,2)+'\n',{mode:0o600})
  }finally{
   clearTimeout(timer);await connection.close()
   expect(stdio.pid).toBeNull();expect(existsSync(`/proc/${pid}`)).toBe(false)
   writeFileSync(join(project,'process-closed.json'),JSON.stringify({pid,closed:true,cleanup:'SDK close ends/kills only its owned child; no file deletion'})+'\n',{mode:0o600})
  }
 },20000)
})
