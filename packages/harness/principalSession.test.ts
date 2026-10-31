// @vitest-environment node
// ADR-0029 at the session boundary: a delegated run's tool events, refusals and checkpoint carry its
// principal under event 3 and checkpoint 4, both readers' versions; a resume continues only under the
// same principals; a run with no delegation keeps its earlier versions and members.
import {describe,it,expect,beforeEach} from 'vitest'
import chains from '../../contract/vectors/principal/chains.json'
import {managedContext} from './context/managed'
import {readRunEvent} from './recordReader'
import {readExecution,type ExecutionCheckpoint} from './execution/state'
import {projectRunReport} from './runReport'
import {readRunReport} from './runReportReader'
import {toolListDigest} from './toolSources'
import {normalize} from './thinking'
import {configureHarness} from './host'
import {testHost} from './testing/host'
import type {RunPrincipal} from './principal'
import type {AssistantEvent,AssistantSession,HostTool} from './engine'

beforeEach(()=>configureHarness(testHost()))
const T='2026-10-30T01:00:00.000Z'
// The vector's chains, by name: root, child, reissued, otherActor, otherSubject.
const CHAINS={root:0,child:1,reissued:2,otherActor:3,otherSubject:4} as const
const principalOf=(chain:keyof typeof CHAINS='root',over:Partial<RunPrincipal>={}):RunPrincipal=>({chain:JSON.stringify(chains.items[CHAINS[chain]]),keys:chains.keys,software:chains.software,audience:chains.audience,clock:()=>Date.parse(T),admission:()=>({admission:'admit'}),...over})

async function hostTools(ran:string[]):Promise<HostTool[]>{
 const group=([['read_note','read'],['delete_note','act']] as const).map(([name,effect]):HostTool=>({name,description:name,inputSchema:{type:'object'},taxonomy:{operation:effect,effect} as HostTool['taxonomy'],...(effect==='act'?{approve:()=>'allow' as const}:{}),execute:async()=>{ran.push(name);return {content:[{type:'text',text:'done'}]}},source:{kind:'custom',id:'notes',trust:'first-party'} as HostTool['source']}))
 const digest=await toolListDigest(group.map(t=>({name:t.name,description:t.description,inputSchema:t.inputSchema,taxonomy:t.taxonomy})) as never)
 return group.map(t=>({...t,source:{...t.source!,toolListDigest:digest}}))
}
const session=(tools:HostTool[],principal?:RunPrincipal,execution?:ExecutionCheckpoint):AssistantSession=>({prompt:'Current request',testPrompt:'',tools:[],hostTools:tools,callTool:async()=>({content:[]}),model:{family:'openai-compatible',model:'small',call:async()=>{throw Error('no model call here')}},thinking:normalize('off','openai-compatible'),signal:new AbortController().signal,context:{currentRequest:{message:'Current request',input:'Current request'},...(principal?{principal}:{}),...(execution?{execution}:{})}} as AssistantSession)

async function run(given:AssistantSession):Promise<AssistantEvent[]>{
 const events:AssistantEvent[]=[]
 for await(const event of managedContext(given,async function*(ready){
  for(const [id,name] of [['c1','notes__read_note'],['c2','notes__delete_note']] as const){
   const tool=ready.values.hostTools.find(t=>t.name===name)!
   yield {type:'tool_call' as const,callId:id,name,args:{}}
   let text='done',isError=false
   try{await tool.execute({},ready.signal,id)}catch(error){text=(error as Error).message;isError=true}
   yield {type:'tool_result' as const,callId:id,name,isError,text}
  }
  yield {type:'end' as const}
 }))events.push(event)
 return events
}
const checkpointOf=(events:AssistantEvent[])=>[...events].reverse().find(e=>e.type==='execution_checkpoint') as Extract<AssistantEvent,{type:'execution_checkpoint'}>|undefined

describe('a delegated run at the boundary (ADR-0029)',()=>{
 it("carries its principal on every tool event, its refusal and its checkpoint 4, which both readers' versions read",async()=>{
  const ran:string[]=[]
  const events=await run(session(await hostTools(ran),principalOf()))
  expect(ran).toEqual(['read_note'])
  const record={actor:{agent:'b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1',host:'desk',software:chains.software},rootActor:'b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1',subject:chains.items[0]![0]!.sub,delegation:{id:'33333333333333333333333333333333',digest:chains.digests.root},chain:[chains.digests.root]}
  const tooled=events.filter(e=>e.type==='tool_call'||e.type==='tool_result')
  expect(tooled.length).toBe(4)
  for(const event of tooled){expect(event).toMatchObject({version:3,principal:record});expect(readRunEvent(JSON.parse(JSON.stringify(event)))).toEqual(event)}
  const refusal=events.find(e=>e.type==='guardrail'&&e.reason==='delegation refused')
  expect(refusal).toMatchObject({version:3,tool:'notes__delete_note',refusal:{name:'out-of-scope',reason:'tool'},principal:record})
  expect(readRunEvent(JSON.parse(JSON.stringify(refusal)))).toEqual(refusal)
  const last=checkpointOf(events)!
  expect(last.version).toBe(3)
  expect(last.checkpoint).toMatchObject({version:4,principal:record})
  expect(readExecution(JSON.parse(JSON.stringify(last.checkpoint)))).toMatchObject({version:4,principal:record})
  // Version 4 is exactly a delegated run's: without its principal it is refused, and version 3 cannot carry one.
  const {principal:_,...bare}=JSON.parse(JSON.stringify(last.checkpoint))
  expect(()=>readExecution(bare)).toThrow()
  expect(()=>readExecution({...JSON.parse(JSON.stringify(last.checkpoint)),version:3})).toThrow()
  const report=projectRunReport({...last.checkpoint,status:'complete'})
  expect(report).toMatchObject({version:3,checkpointVersion:4,principal:record})
  expect(readRunReport(JSON.stringify(report))).toEqual(report)
 })
 it('writes a run with no delegation as before: no principal, no event 3, no checkpoint 4',async()=>{
  const ran:string[]=[]
  const events=await run(session(await hostTools(ran)))
  expect(ran).toEqual(['read_note','delete_note'])
  expect(JSON.stringify(events)).not.toContain('principal')
  expect(events.every(e=>e.version===1||e.version===2)).toBe(true)
  expect(checkpointOf(events)!.checkpoint.version).toBeLessThan(4)
 })
 it("refuses to start a run whose leaf's software is not the run's, before anything is sent",async()=>{
  const events=await run(session(await hostTools([]),principalOf('root',{software:{package:'@judgment-pack/harness',version:'0.12.0'}})))
  expect(events.some(e=>e.type==='tool_call')).toBe(false)
  expect(events.find(e=>e.type==='error')).toMatchObject({failure:{code:'configuration'}})
  expect(JSON.stringify(events)).toContain('software-mismatch')
 })
 it('continues a run only under the subject and root actor it began under (principal-changed)',async()=>{
  const delegated=checkpointOf(await run(session(await hostTools([]),principalOf())))!.checkpoint
  const undelegated=checkpointOf(await run(session(await hostTools([]))))!.checkpoint
  // A resume under the same subject and root actor from another issuing host is another principal.
  const otherHost={...delegated,principal:{...delegated.principal!,actor:{...delegated.principal!.actor,host:'other-host'}}}
  for(const [saved,principal] of [[delegated,principalOf('otherActor')],[delegated,principalOf('otherSubject')],[delegated,undefined],[undelegated,principalOf()],[otherHost,principalOf()]] as const){
   const ran:string[]=[]
   const events=await run(session(await hostTools(ran),principal,{...structuredClone(saved),status:'running'}))
   expect(ran).toEqual([])
   expect(events.find(e=>e.type==='error')).toMatchObject({failure:{code:'stale-state'}})
   expect(JSON.stringify(events)).toContain('principal-changed')
  }
  // A chain re-issued for the same principals replaces the record; so does one narrowed from the same root.
  for(const [chain,id] of [['root','33333333333333333333333333333333'],['reissued','3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a'],['child','44444444444444444444444444444444']] as const){
   const ran:string[]=[]
   const events=await run(session(await hostTools(ran),principalOf(chain),{...structuredClone(delegated),status:'running'}))
   expect(ran).toEqual(['read_note'])
   expect(checkpointOf(events)!.checkpoint.principal!.delegation.id).toBe(id)
  }
 })
})
