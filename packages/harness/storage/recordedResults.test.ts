/**
 * #343: a durable host call whose result the record guard redacts (ADR-0018) is recorded in that form, and the run's
 * events and the model are given the same form, so an approved act that ran settles rather than ending unknown, and
 * nothing runs it again. The pack id names a temp folder inside a URI's path, as the issue's witness does: the pattern
 * keeps matching there by the maintainer's decision. Through the API engine (vercel); the Go twins are
 * go/context/recorded_results_test.go, go/adapter/durableapprovaltests/recorded_results_test.go and
 * go/adapter/delegate/recorded_continuation_test.go.
 */
import {describe,expect,it,vi} from 'vitest'
import {MemoryRunStore,type RunStore} from './run'
import {vercel} from '../engines/vercel'
import {codex} from '../engines/codex'
import {withDelegation} from '../delegation'
import {ofType,sseTurn,streamed,vercelTurn,type EndpointRequest} from '../testing/scripted'
import {approvalShownDigest,toolListDigest} from '../toolSources'
import {OutputGuard} from '../outputGuard'
import {recordedForm,type RecordGuard,type RecordsPolicy} from '../records'
import type {AgentAssistantSession,AssistantEvent,HostTool} from '../engine'
import type {AgentRun} from '../agent'

const RAW='https://packs.example/tmp/evaluation-pack',REDACTED='https://packs.example[redacted: local-temp-path]',RAW_PATH='packs.example/tmp/'
const packText=(id:string)=>JSON.stringify({packId:id,status:'evaluated'})
const decode=(bytes:Uint8Array|null|undefined)=>bytes?new TextDecoder().decode(bytes):''
async function storedText(store:MemoryRunStore,id:string){
 let out=decode((await store.load(id))?.checkpoint),after:string|null=null
 for(;;){const page=await store.readEvents(id,after,100);for(const item of page.items){out+='\n'+decode(item.bytes);after=item.cursor}if(!page.hasMore)return out}
}
type Kind='act'|'read'|'external'

/** One run of an evaluate tool of the kind given, answering the pack id given; an act is held for the person and approved. */
async function evaluate(kind:Kind,id:string,store:RunStore=new MemoryRunStore()){
 const executed:unknown[]=[],requests:EndpointRequest[]=[]
 const taxonomy={act:{operation:'act',effect:'act'},read:{operation:'read',effect:'read'},external:{operation:'read',effect:'read-external'}}[kind]
 const declared={name:'evaluate',description:'Evaluate a pack.',inputSchema:{type:'object'},taxonomy}
 const source=kind==='read'?undefined:{kind:'custom',id:kind==='act'?'tracker':'records',trust:kind==='act'?'person':'connected',version:'1',toolListDigest:await toolListDigest([declared] as never),
  ...(kind==='act'?{identity:{kind:'connection',label:'tracker'}}:{verifyReceipt:async(e:{receipt?:string})=>e.receipt==='rcpt-7'})}
 const tool={...declared,...(kind==='act'?{effectClass:'act',approve:()=>'ask'}:kind==='read'?{effect:'read'}:{}),...(source?{source}:{}),
  execute:async(args:unknown)=>{executed.push(args);return {content:[{type:'text' as const,text:packText(id)}],structuredContent:{packId:id,status:'evaluated'},...(kind==='external'?{receipt:'rcpt-7'}:{})}}} as unknown as HostTool
 const wire=kind==='act'?'tracker__evaluate':kind==='external'?'records__evaluate':'evaluate'
 const turn=(resumed:boolean,decision?:object)=>vercelTurn({engine:vercel,hostTools:[tool],
  answer:(request:EndpointRequest)=>{requests.push(request);return streamed(!resumed&&request.number===1?sseTurn({tool:{name:wire,args:{pack:'evaluation-pack'},id:'call_1'}}):sseTurn({text:'Done.'}))},
  session:{context:{currentRequest:{message:'Evaluate it.',input:'Evaluate it.'},runStore:{store,id:'recorded',...(resumed?{resume:true}:{})},...(decision?{approval:decision}:{})}} as never})
 const events:AssistantEvent[]=[...(await turn(false)).events]
 if(kind==='act'){
  const saved=JSON.parse(decode((await store.load('recorded'))?.checkpoint))
  expect(saved.approval?.state).toBe('asked');expect(executed).toEqual([])
  events.push(...(await turn(true,{callId:saved.approval.callId,approved:true,shownDigest:approvalShownDigest(saved.approval)})).events)
 }
 return {executed,events,requests,turn}
}

describe('a result the record guard redacts (#343)',()=>{
 for(const kind of ['act','read','external'] as const)it(`${kind}: is recorded, shown and given to the model in the redacted form, and settles`,async()=>{
  const store=new MemoryRunStore(),out=await evaluate(kind,RAW,store)
  expect(ofType(out.events,'error')).toEqual([])
  expect(out.executed).toHaveLength(1)
  const saved=JSON.parse(decode((await store.load('recorded'))?.checkpoint)),row=saved.durable.dispatches.find((d:{kind:string})=>d.kind==='host')
  expect(row.state).toBe('result')
  expect(row.result.content[0].text).toBe(packText(REDACTED))
  expect(row.result.structuredContent.packId).toBe(REDACTED)
  if(kind==='act')expect(saved.approval.result.content[0].text).toBe(packText(REDACTED))
  expect(await storedText(store,'recorded')).not.toContain(RAW_PATH)
  // An approved act's result is shown in the run's checkpoint event (approval.result); any other call's in its tool_result.
  const shown=kind==='act'
   ?ofType(out.events,'execution_checkpoint').map(e=>(e.checkpoint as {approval?:{result?:{content:{text:string}[]}}}).approval?.result?.content[0]?.text).filter((text):text is string=>text!==undefined)
   :ofType(out.events,'tool_result').filter(e=>e.text.includes('packId')).map(e=>e.text)
  expect(shown.length).toBeGreaterThan(0)
  for(const text of shown)expect(text).toBe(packText(REDACTED))
  expect(JSON.stringify(out.events)).not.toContain(RAW_PATH)
  // What the model was given: every request after the call, whole. None holds the raw text, and one holds the redacted form.
  const given=out.requests.map(request=>JSON.stringify(request.body))
  expect(given.some(text=>text.includes('[redacted: local-temp-path]'))).toBe(true)
  for(const text of given)expect(text).not.toContain(RAW_PATH)
  // A resumed run is answered from the record, and the act is not run again.
  if(kind==='act'){await out.turn(true);expect(out.executed).toHaveLength(1)}
 })
 it('a result with nothing redacted is recorded and given as it was',async()=>{
  const kept='https://packs.example/var/evaluation-pack',store=new MemoryRunStore(),out=await evaluate('read',kept,store)
  const row=JSON.parse(decode((await store.load('recorded'))?.checkpoint)).durable.dispatches.find((d:{kind:string})=>d.kind==='host')
  expect(row.result).toEqual({content:[{type:'text',text:packText(kept)}],structuredContent:{packId:kept,status:'evaluated'}})
  expect(ofType(out.events,'tool_result').find(e=>e.name==='evaluate')?.text).toBe(packText(kept))
 })
 it('a result a store does not take is still unknown',async()=>{
  const memory=new MemoryRunStore()
  const store:RunStore={durability:'memory',load:memory.load.bind(memory),readEvents:memory.readEvents.bind(memory),commit:async input=>{
   const cp=JSON.parse(decode(input.checkpoint))
   if(cp.durable?.dispatches?.some((d:{kind:string;state:string})=>d.kind==='host'&&d.state==='result'))throw Error('private host storage detail')
   return memory.commit(input)
  }}
  const out=await evaluate('read',RAW,store)
  expect(out.executed).toHaveLength(1)
  expect(JSON.stringify(out.events)).toContain('StorageOutcomeUnknown')
  expect(JSON.stringify(out.events)).not.toContain(RAW_PATH)
 })
})

// The continuation a durable handoff gives its parent is the one its record holds. The child's report, C:\Users\ and a line
// break, matches no pattern; in the continuation's JSON frame the break is a backslash escape that completes a Windows home
// path, which the record guard redacts. Given the unrecorded form, the parent's next request held what the record could not,
// and the run stopped there.
it('a handoff’s parent is given the continuation its record holds (#343)',async()=>{
 const memory=new MemoryRunStore(),prompts:string[]=[]
 const run:AgentRun=async(request,callbacks)=>{
  prompts.push(request.prompt)
  try{
   if(prompts.length===1){const tool=request.tools.find(t=>t.description.startsWith('delegate_tasks:'))!;await callbacks.tool({id:'delegate-wire',name:tool.name,arguments:{tasks:[{title:'Review',profile:'review',objective:'Check the supplied claims.'}]}},callbacks.signal);return}
   await callbacks.event({type:'message',id:'reply',text:request.prompt.includes('Complete this read-only task')?'C:\\Users\\\ndone':'Parent finished.',phase:'final'})
  }finally{callbacks.providerRelease?.acknowledge(callbacks.providerRelease.invocation)}
 }
 const session:AgentAssistantSession={prompt:'Review the supplied claims.',testPrompt:'',tools:[],hostTools:[],callTool:vi.fn(),signal:new AbortController().signal,allowConversation:true,interactive:true,critic:'off',agent:{model:'fixture',run},
  context:{currentRequest:{message:'Review the supplied claims.',input:'Review the supplied claims.'},runStore:{store:memory,id:'recorded-continuation'}}}
 const events:AssistantEvent[]=[];for await(const event of withDelegation(codex).start(session))events.push(event)
 expect(events.filter(e=>e.type==='error')).toEqual([])
 expect(prompts).toHaveLength(3)
 const saved=JSON.parse(decode((await memory.load('recorded-continuation'))?.checkpoint))
 const recorded:string=saved.handoff.continuation
 expect(recorded).toContain('[redacted: local-home-path]')
 expect(prompts[2]).toContain(recorded)
 expect(prompts[2]).not.toContain('C:\\\\Users\\\\\\n')
})

describe('the recorded form of one result (#343)',()=>{
 const guard=()=>new OutputGuard({},()=>({}),()=>{}) as unknown as RecordGuard,policy={redactOnStore:true} as RecordsPolicy
 // A value JSON writes otherwise is no change: with nothing redacted the result is answered exactly as it was given.
 it('keeps a result with a Date and nothing redacted as it was',()=>{
  const given={content:[{type:'text',text:'ok'}],structuredContent:{packId:'kept',at:new Date(0)}}
  expect(recordedForm(given,policy,guard())).toBe(given)
 })
 // Members whose names the guard rewrote follow the others in the sorted order of the names they had; the Go writer keeps
 // the same order (go/context/recorded_results_test.go).
 it('puts renamed members after the others, in their old names’ order',()=>{
  const given={content:[{type:'text',text:'ok'}],structuredContent:{z:1,'aZ/tmp/x':2,m:3,'a/tmp/x':4}}
  expect(JSON.stringify(recordedForm(given,policy,guard()))).toBe('{"content":[{"type":"text","text":"ok"}],"structuredContent":{"z":1,"m":3,"a[redacted: local-temp-path]":4,"aZ[redacted: local-temp-path]":2}}')
 })
})
