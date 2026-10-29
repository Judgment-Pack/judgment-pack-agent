/**
 * Receipt quarantine (agent#242; the amendment to ADR-0027): an external read whose receipt does not
 * verify is withheld, and nothing of its result is kept where a context reader, a later request or a
 * Plan step could reach it. Held to the hand-written
 * contract/vectors/tool-result/receipt-quarantine.json, as go/context/receipt_quarantine_test.go and
 * go/adapter/eino/receipt_quarantine_test.go hold the Go: at the boundary, through the API engine
 * (vercel) and the native one (codex) with an in-process model peer (the issue's six requests), and
 * with two sources' calls in flight together and the checkpoint written meanwhile restored.
 */
import {describe,it,expect} from 'vitest'
import vector from '../../contract/vectors/tool-result/receipt-quarantine.json'
import {contextualSession} from './context/session'
import {ChatContext,canonicalJSON} from './context/chat'
import {ContextLedger,contextDigest} from './context/ledger'
import {CONTEXT_DEFAULTS} from './context/policy'
import {MemoryRunStore} from './storage/run'
import {ReceiptAdmission,RECEIPT_WITHHELD,toolListDigest} from './toolSources'
import {evidenceFailure} from './execution/planActions'
import {toolFailureResult} from './failures'
import {vercel} from './engines/vercel'
import {codex} from './engines/codex'
import {runAssistantSession} from './session'
import {normalize} from './thinking'
import {answerWith,type Turn} from './testing/scenarioDriver'
import type {AssistantEvent,AssistantSession,EngineSession,HostTool,McpToolResult} from './engine'

const fixture=vector.fixture,items=vector.items as Record<string,unknown>[]
const RUN='receipt-quarantine'
type Declaration=typeof fixture.declaration
interface Calls {executions:number;verifications:number;entered?:()=>void;release?:Promise<void>}

/** A declaration of the vector's as a host tool whose executor answers result with receipt and whose verifier answers as verifier says. */
async function sourceTool(declaration:Declaration,result:unknown,receipt:string|undefined,verifier:string,calls:Calls):Promise<HostTool>{
 const tool:HostTool={name:declaration.name,description:declaration.description,inputSchema:structuredClone(declaration.inputSchema),taxonomy:{...declaration.taxonomy} as HostTool['taxonomy'],
  execute:async()=>{calls.executions++;return {...structuredClone(result) as McpToolResult,...(receipt===undefined?{}:{receipt})}}}
 const toolListDigestOf=await toolListDigest([{name:tool.name,description:tool.description,inputSchema:tool.inputSchema,taxonomy:tool.taxonomy}])
 tool.source={...declaration.source,toolListDigest:toolListDigestOf,verifyReceipt:async(e,signal)=>{
  calls.verifications++;calls.entered?.();await calls.release
  if(verifier==='accepts its receipt for its result')return e.receipt===receipt&&e.result===canonicalJSON(result)
  if(verifier==='answers true after the deadline'){await new Promise(resolve=>signal.aborted?resolve(undefined):signal.addEventListener('abort',()=>resolve(undefined),{once:true}));return true}
  return false
 }} as HostTool['source']
 return tool
}

/** The id the run's chat would have retained the host's answer under (ChatTurn.keep: the answer as the executor gave it). */
async function retainedAs(scope:string,wire:string,answered:unknown){
 const digest=await contextDigest(JSON.stringify(answered))
 const id='result-'+wire+'-'+digest,value=await contextDigest(JSON.stringify(answered))
 return scope+':'+id+':'+value.slice(0,16)
}

/** A reader's arguments with $retained replaced by id. */
const readerArguments=(reader:{arguments:Record<string,unknown>},id:string)=>Object.fromEntries(Object.entries(reader.arguments).map(([k,v])=>[k,v==='$retained'?id:v]))

/** The tool_result event a call answered with, as the items state it: its callId and source record left out. */
function event(name:string,outcome:{result?:McpToolResult;error?:unknown}){
 const result=outcome.error===undefined?outcome.result!:toolFailureResult(outcome.error)
 const text=(result.content??[]).filter(p=>p.type==='text').map(p=>(p as {text:string}).text).join('\n')
 return {type:'tool_result',name,isError:result.isError===true,text,...(result.structuredContent===undefined?{}:{structured:result.structuredContent})}
}
const settle=async(work:Promise<McpToolResult>)=>{try{return {result:await work}}catch(error){return {error}}}

/** A projected event, its listing's own operation records taken out (the run records each call it observes; the rejected call stays inspectable that way, its result the gate's refusal). */
function projected(raw:Extract<AssistantEvent,{type:'tool_result'}>,scope:string){
 const {callId:_callId,source:_source,version:_version,...rest}=raw as typeof raw&{version?:number}
 const out=structuredClone(rest) as Record<string,unknown>&{structured?:{items?:{kind:string;contextId:string;label:string}[]}}
 const operations:{label:string}[]=[]
 if(raw.name==='list_context'&&!raw.isError&&out.structured?.items){
  expect(JSON.parse(raw.text)).toEqual(out.structured)
  out.structured.items=out.structured.items.filter(item=>{const operation=item.kind==='history'&&item.contextId.startsWith(scope+':operation-');if(operation)operations.push(item);return !operation})
  out.text=JSON.stringify(out.structured)
 }
 return {event:out,operations}
}

const decode=(bytes:Uint8Array|null|undefined)=>bytes?new TextDecoder().decode(bytes):''
async function storedText(store:MemoryRunStore,id=RUN){
 let out=decode((await store.load(id))?.checkpoint),after:string|null=null
 for(;;){const page=await store.readEvents(id,after,100);for(const item of page.items){out+='\n'+decode(item.bytes);after=item.cursor}if(!page.hasMore)return out}
}

function session(chat:ChatContext,store:MemoryRunStore,tools:HostTool[],expired:boolean,id=RUN):AssistantSession{
 return {prompt:'Read record e0.',testPrompt:'',tools:[],hostTools:tools,callTool:async()=>{throw Error('No Runtime tool was offered')},model:{family:'openai-compatible',model:'fixture',call:async()=>{throw Error('unused')}},thinking:normalize('off','openai-compatible'),signal:new AbortController().signal,allowConversation:true,interactive:true,critic:'off',
  ...(expired?{policy:{budget:{sourceCallMilliseconds:fixture.sourceCallMilliseconds}}}:{}),
  context:{currentRequest:{message:'Read record e0.',input:'e0'},chat,runStore:{store,id}}} as AssistantSession
}

/** The rejected call is recorded once, as an uncertain outcome whose failure is the gate's refusal, with no result; it is never offered or accepted as Plan evidence. */
async function withheldRow(ready:Awaited<ReturnType<typeof contextualSession>>,wire:string){
 const controller=ready.context.execution,proof=controller.proof!()!
 const rows=proof.book.dispatches.filter(d=>d.tool===wire)
 expect(rows).toHaveLength(1)
 const row=rows[0]!
 const tool=(name:string)=>ready.session.values.hostTools.find(t=>t.name===name)!
 await tool('update_plan').execute({explanation:'Read the record',plan:[{step:'Read the record',status:'in_progress'}]},ready.session.signal)
 await expect(tool('complete_plan_step').execute({id:controller.state.plan[0]!.id,expectedRevision:controller.state.planState!.revision,outcome:'completed',evidence:{operations:[row.id],artifacts:[]}},ready.session.signal)).rejects.toThrow('unsuccessful-operation')
 expect(evidenceFailure({operations:[row.id],artifacts:[]},proof)).toBe('unsuccessful-operation')
 expect(controller.requestState()).not.toContain(row.id)
 expect({state:row.state,result:row.result,repeat:row.repeatResultJson,failure:row.failure}).toEqual({state:'unknown',result:undefined,repeat:undefined,failure:{version:1,code:'tool-failed',source:'tool',outcome:'unknown',detail:items[0]!.text}})
 expect(proof.book.observations.some(o=>o.dispatchId===row.id)).toBe(false)
}

describe('an external read whose receipt does not verify (agent#242)',()=>{
 for(const held of fixture.cases){
  it(`${held.name}: leaves nothing a reader, a later request or a Plan step can reach`,async()=>{
   const chat=new ChatContext({scope:fixture.scope}),store=new MemoryRunStore(),calls:Calls={executions:0,verifications:0}
   const receipt=(held as {receipt?:string}).receipt
   const tool=await sourceTool(fixture.declaration,held.result,receipt,held.verifier,calls)
   const ready=await contextualSession(session(chat,store,[tool],held.name==='expired-receipt'))
   const find=(name:string)=>ready.session.values.hostTools.find(t=>t.name===name)!
   expect(event(fixture.wireName,await settle(find(fixture.wireName).execute(structuredClone(fixture.arguments),ready.session.signal,'call-e0')))).toEqual(items[0])
   const retained=await retainedAs(chat.scope,fixture.wireName,{...structuredClone(held.result),...(receipt===undefined?{}:{receipt})})
   for(const [at,reader] of fixture.readers.entries())expect(event(reader.tool,await settle(find(reader.tool).execute(readerArguments(reader,retained),ready.session.signal)))).toEqual(items[at+1])
   expect({executions:calls.executions,verifications:calls.verifications}).toEqual({executions:1,verifications:held.verifierCalls})
   expect(new TextDecoder().decode(chat.snapshot())).not.toContain(fixture.sentinel)
   expect(ready.context.execution.requestState()).not.toContain(fixture.sentinel)
   await withheldRow(ready,fixture.wireName)
   expect(await storedText(store)).not.toContain(fixture.sentinel)
   const next=chat.openTurn()
   expect(next.entries.size).toBe(0)
   const index=next.index(CONTEXT_DEFAULTS,'a'.repeat(32))
   expect(index).not.toContain(fixture.sentinel);expect(index).not.toContain(retained)
   next.release();ready.context.close()
  })
 }

 it('keeps only the verified result when two sources are in flight together, and in the checkpoint written meanwhile, restored',async()=>{
  const overlap=fixture.overlap,rejected=fixture.cases[0]!
  const chat=new ChatContext({scope:fixture.scope}),store=new MemoryRunStore()
  let entered!:()=>void,release!:()=>void
  const enteredOnce=new Promise<void>(resolve=>{entered=resolve})
  const held:Calls={executions:0,verifications:0,entered,release:new Promise<void>(resolve=>{release=resolve})},verified:Calls={executions:0,verifications:0}
  const tools=[await sourceTool(fixture.declaration,rejected.result,rejected.receipt,rejected.verifier,held),await sourceTool(overlap.declaration as Declaration,overlap.result,overlap.receipt,overlap.verifier,verified)]
  const ready=await contextualSession(session(chat,store,tools,false))
  const find=(name:string)=>ready.session.values.hostTools.find(t=>t.name===name)!
  const pending=settle(find(fixture.wireName).execute(structuredClone(fixture.arguments),ready.session.signal,'call-e0'))
  await enteredOnce
  // While that receipt check runs, the other source's calls run and are recorded whole.
  for(const [at,args] of overlap.calls.entries())expect(event(overlap.wireName,await settle(find(overlap.wireName).execute(structuredClone(args),ready.session.signal,'call-b'+(at+1))))).toEqual(overlap.event)
  const written=decode((await store.load(RUN))?.checkpoint)
  release()
  expect(event(fixture.wireName,await pending)).toEqual(items[0])
  expect(written).not.toContain(fixture.sentinel)
  const saved=JSON.parse(written) as {context:{scope:string;entries:{id:string}[]}}
  const retained=await retainedAs(chat.scope,fixture.wireName,{...structuredClone(rejected.result),receipt:rejected.receipt})
  const kept=await retainedAs(chat.scope,overlap.wireName,{...structuredClone(overlap.result),receipt:overlap.receipt})
  expect(saved.context.entries.map(e=>e.id)).toEqual([kept])
  // Restored, as a resumed run's ledger is: the verified result is read whole, the rejected one is answered as the items say.
  const restored=new ContextLedger(CONTEXT_DEFAULTS)
  await restored.restore(saved.context as Parameters<ContextLedger['restore']>[0])
  const restoredTools=restored.tools()
  for(const reader of [(name:string)=>restoredTools.find(t=>t.name===name)!,find]){
   expect(JSON.stringify(await reader('read_context').execute({id:kept},ready.session.signal))).toContain('VERIFIED_B1_PAYLOAD')
   const listed=await reader('list_context').execute({},ready.session.signal) as {structuredContent:{items:{contextId:string}[]}}
   expect(listed.structuredContent.items.map(item=>item.contextId)).toEqual([kept])
   for(const [at,item] of fixture.readers.slice(1).entries())expect(event(item.tool,await settle(reader(item.tool).execute(readerArguments(item,retained),ready.session.signal)))).toEqual(items[at+2])
  }
  expect([held.executions,held.verifications,verified.executions,verified.verifications]).toEqual([1,1,2,2])
  await withheldRow(ready,fixture.wireName)
  for(const row of ready.context.execution.proof!()!.book.dispatches.filter(d=>d.tool===overlap.wireName))expect([row.state,row.failure]).toEqual(['result',undefined])
  expect(await storedText(store)).not.toContain(fixture.sentinel)
  ready.context.close()
 })

 it('retains nothing in the read wrapper that its check refuses, whatever ran below it',async()=>{
  const chat=new ChatContext({scope:fixture.scope})
  const ready=await contextualSession(session(chat,new MemoryRunStore(),[],false))
  let ran=0
  const read=(verified:boolean)=>ready.context.execution.wrapRead(fixture.wireName,async()=>{ran++;return {content:[{type:'text',text:fixture.sentinel}]}},{immutable:true},true,{runtime:false},new ReceiptAdmission(async result=>({shown:result,receipt:undefined,verified})))({id:'e0'})
  for(const verified of [false,false,true]){
   const outcome=await settle(read(verified))
   expect(outcome.error===undefined).toBe(verified)
   expect(new TextDecoder().decode(chat.snapshot()).includes(fixture.sentinel)).toBe(verified)
  }
  expect(ran).toBe(3)
  ready.context.close()
 })

 it('checks a receipt once and refuses the result everywhere it is asked',async()=>{
  let checks=0
  const admission=new ReceiptAdmission(async result=>{checks++;return {shown:result,receipt:undefined,verified:false}})
  for(let at=0;at<3;at++)await expect(admission.admit({content:[{type:'text',text:'UNVERIFIED'}]})).rejects.toMatchObject({failure:{version:1,code:'tool-failed',source:'tool',outcome:'unknown',detail:RECEIPT_WITHHELD}})
  expect(checks).toBe(1);expect((await admission.outcome())?.verified).toBe(false)
 })
})

// The issue's sequence: the source call, list_context, read_context, read_window and a search of
// every retained item (the vector's readers 0, 1, 2 and 4, its items 0, 1, 2, 3 and 5), then the
// final answer: six requests, through each engine.
const SEQUENCE=[0,1,2,4],SHOWN=[0,1,2,3,5]
describe.each([['api',vercel],['native',codex]] as const)('the issue’s six requests through the %s engine (agent#242)',(_name,engine)=>{
 for(const held of fixture.cases){
  it(held.name,async()=>{
   const chat=new ChatContext({scope:fixture.scope}),store=new MemoryRunStore(),calls:Calls={executions:0,verifications:0}
   const receipt=(held as {receipt?:string}).receipt
   const tool=await sourceTool(fixture.declaration,held.result,receipt,held.verifier,calls)
   const retained=await retainedAs(chat.scope,fixture.wireName,{...structuredClone(held.result),...(receipt===undefined?{}:{receipt})})
   const referenced=new RegExp(fixture.scope+':result-[A-Za-z0-9_]+-[0-9a-f]{64}:[0-9a-f]{16}')
   const play=async(id:string,peer:(n:number,seen:string)=>Turn)=>{
    const requests:string[]=[],events:AssistantEvent[]=[]
    const base=session(chat,store,[tool],held.name==='expired-receipt',id)
    let input:EngineSession
    if(engine===codex){
     const {model:_model,thinking:_thinking,...rest}=base
     input={...rest,agent:{model:'fixture',run:async(request,callbacks)=>{
      requests.push(JSON.stringify(request))
      for(let n=1;;n++){
       const turn=peer(n,requests.join('\n'))
       if(!('call' in turn)){await callbacks.event({type:'message',id:'final',text:'Done.',phase:'final'});return}
       const offered=request.tools.find(t=>t.description.startsWith(turn.call.tool+':'))
       expect(offered).toBeDefined()
       requests.push(JSON.stringify(await callbacks.tool({id:'call-'+n,name:offered!.name,arguments:turn.call.arguments},callbacks.signal)))
      }
     }}} as EngineSession
    }else input={...base,model:{family:'openai-compatible',model:'fixture',call:async(_path:string,wire:{body:string})=>{requests.push(wire.body);return answerWith(peer(requests.length,wire.body),'stream',requests.length)}}} as EngineSession
    await runAssistantSession(engine,input,e=>{events.push(e)})
    return {requests,events}
   }
   let learned=retained
   const {requests,events}=await play(RUN,(n,seen)=>{
    if(n===1)return {call:{tool:fixture.wireName,arguments:structuredClone(fixture.arguments)}}
    if(n>SEQUENCE.length+1)return {say:'Done.'}
    const found=seen.match(referenced)?.[0]
    if(found!==undefined){expect(found).toBe(retained);learned=found}
    const reader=fixture.readers[SEQUENCE[n-2]!]!
    return {call:{tool:reader.tool,arguments:readerArguments(reader,learned)}}
   })
   expect(events.filter(e=>e.type==='error')).toEqual([])
   expect(requests).toHaveLength(6)
   for(const request of requests)expect(request).not.toContain(fixture.sentinel)
   const results=events.filter((e):e is Extract<AssistantEvent,{type:'tool_result'}>=>e.type==='tool_result')
   expect(results).toHaveLength(SHOWN.length)
   for(const [at,item] of SHOWN.entries()){
    const {event:shown,operations}=projected(results[at]!,fixture.scope)
    expect(shown).toEqual(items[item])
    for(const operation of operations)expect(operation.label).toBe(fixture.wireName+' · failed')
    // Through the API engine the rejected call stays listed as the failed operation the run records for its requests; the native run's one invocation records none.
    if(results[at]!.name==='list_context')expect(operations).toHaveLength(engine===vercel?1:0)
   }
   expect({executions:calls.executions,verifications:calls.verifications}).toEqual({executions:1,verifications:held.verifierCalls})
   const stored=await storedText(store)
   expect(stored).not.toContain(fixture.sentinel)
   const head=JSON.parse(decode((await store.load(RUN))?.checkpoint)) as {durable:{dispatches:{tool:string;state:string;result?:unknown;failure?:{code:string;outcome:string}}[]}}
   const rows=head.durable.dispatches.filter(row=>row.tool===fixture.wireName)
   expect(rows.map(row=>[row.state,row.result,row.failure?.code,row.failure?.outcome])).toEqual([['unknown',undefined,'tool-failed','unknown']])
   // The chat's next run: its first request carries neither the result nor its reference.
   const next=await play(RUN+'-next',()=>({say:'Done.'}))
   expect(next.requests).toHaveLength(1)
   expect(next.requests[0]).not.toContain(fixture.sentinel);expect(next.requests[0]).not.toContain(retained)
   expect(calls.executions).toBe(1)
  },60_000)
 }
})
