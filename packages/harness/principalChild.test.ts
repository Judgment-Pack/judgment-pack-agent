// @vitest-environment node
// ADR-0029: a delegated child gets the link the issuing host narrowed from its parent's, never its parent's whole reach:
// a link wider than its parent, a chain that is not the parent's with one link below it, and a parent that permits none
// are refused by name; the chain is recorded. The leases, fences and resume pins that keep a run's principals are held
// here as well as by the store kits.
import {describe,it,expect,vi,beforeEach} from 'vitest'
import chains from '../../contract/vectors/principal/chains.json'
import children from '../../contract/vectors/principal/child.json'
import bindings from '../../contract/vectors/principal/binding.json'
import {narrowChild,openChildPrincipal,openPrincipal,principalBinding,sameBinding,validPrincipalBinding,DelegationRefused,type ChildLinkRequest,type RunPrincipal} from './principal'
import {withDelegation} from './delegation'
import {DELEGATE_TOOL} from './delegationContract'
import {MemoryRunStore} from './storage/run'
import {LeaseCapabilityRefused,LeaseRefused,LeaseTable,ReferenceEffectHost,effectAdmission,fenceOf,fencedRunStore,type LeaseFence} from './storage/lease'
import {runStoreLeaseConformance,StorageConformanceFailure} from './storage/conformance'
import {parkRun,resumeParked,wakeKeyDigest,wakeRun,type WaitClaim,type WaitLifetime} from './storage/wait'
import {canonicalJSON} from './context/chat'
import recordVectors from '../../contract/vectors/principal/record.json'
import pauseVector from '../../contract/vectors/approval/pause.json'
import {managedContext} from './context/managed'
import {contextualSession} from './context/session'
import {durableSession} from './storage/writer'
import {readRunEvent} from './recordReader'
import {toolListDigest} from './toolSources'
import {normalize} from './thinking'
import {configureHarness} from './host'
import {testHost} from './testing/host'
import type {AssistantEvent,AssistantSession,Engine,EngineSession,HostTool} from './engine'

beforeEach(()=>configureHarness(testHost()))
const LINKS:Record<string,unknown>={root:chains.items[0]![0],child:chains.items[1]![1],widened:chains.refused[0]!.chain[1],grandchild:chains.refused[1]!.chain[2],otherActor:chains.items[3]![0],otherSubject:chains.items[4]![0],relay:chains.items[6]![1],relayLeaf:chains.items[7]![2],otherActorChild:chains.items[8]![1],otherSubjectChild:chains.items[9]![1]}
const text=(names:readonly string[])=>JSON.stringify(names.map(name=>LINKS[name]))
const T='2026-10-30T01:00:00.000Z'
const input=(names:readonly string[],child?:RunPrincipal['child']):RunPrincipal=>({chain:text(names),keys:chains.keys,software:chains.software,audience:chains.audience,clock:()=>Date.parse(T),admission:()=>({admission:'admit'}),...(child?{child}:{})})
const asked={task:'task-1',profile:'review',tools:[{source:'notes',tool:'read_note'}]}
const answerOf=(raw:unknown)=>{
 if(raw&&typeof raw==='object'&&Array.isArray((raw as {chain?:unknown}).chain)){const {chain,...rest}=raw as {chain:string[]};return {chain:text(chain),...rest}}
 return raw
}

describe('a delegated child (handwritten cases)',()=>{
 it('a port that never answers is admission-unknown once its time has passed',async()=>{
  const parent=await openPrincipal(input(['root'],()=>new Promise<never>(()=>{})))
  vi.useFakeTimers()
  try{
   const refused=expect(narrowChild(parent,asked)).rejects.toMatchObject({name:'DelegationRefused',refusal:{name:'admission-unknown'}})
   await vi.advanceTimersByTimeAsync(5000)
   await refused
  }finally{vi.useRealTimers()}
 })
 for(const c of children.cases)it(c.name,async()=>{
  const requests:ChildLinkRequest[]=[]
  const port=c.answer==='none'?undefined:(request:ChildLinkRequest)=>{requests.push(request);if(c.answer==='throw')throw Error('port down');return answerOf(c.answer) as never}
  const parent=await openPrincipal(input(c.parent,port))
  const opened=narrowChild(parent,asked).then(child=>openChildPrincipal(parent,child))
  if(typeof c.expect==='string')await expect(opened).rejects.toMatchObject({name:'DelegationRefused',refusal:{name:c.expect}})
  else{
   const record=(await opened).record
   expect({agent:record.actor.agent,rootActor:record.rootActor,links:record.chain.length}).toEqual(c.expect)
   expect(record.chain.slice(0,parent.record.chain.length)).toEqual(parent.record.chain)
  }
  expect(requests).toEqual(c.asked?[children.items[0]]:[])
 })
 it('reads each binding, and only the same principals are the same',()=>{
  for(const item of bindings.items)expect(validPrincipalBinding(structuredClone(item))).toBe(true)
  for(const row of bindings.refused)expect(validPrincipalBinding(structuredClone(row.binding)),row.problem).toBe(false)
  const [same,...others]=bindings.items as never[]
  expect(sameBinding(same,structuredClone(same))).toBe(true)
  for(const other of others)expect(sameBinding(same,other)).toBe(false)
  expect(sameBinding(same,undefined)).toBe(false)
  expect(sameBinding(undefined,undefined)).toBe(true)
 })
})

// Through the real coordinator: its parent run delegates one task.
const task={title:'Check',profile:'review',objective:'Check the note.'}
async function readTool(ran:string[]):Promise<HostTool>{
 const tool={name:'read_note',description:'read_note',inputSchema:{type:'object'},taxonomy:{operation:'read',effect:'read'} as const}
 return {...tool,delegation:'read',execute:async()=>{ran.push('read_note');return {content:[{type:'text',text:'note'}]}},source:{kind:'custom',id:'notes',trust:'first-party',toolListDigest:await toolListDigest([tool] as never)}} as unknown as HostTool
}
const session=(hostTools:HostTool[],principal?:RunPrincipal,extra:Record<string,unknown>={}):AssistantSession=>({prompt:'Parent',testPrompt:'',tools:[],callTool:vi.fn(),hostTools,signal:new AbortController().signal,allowConversation:true,interactive:true,critic:'off',model:{family:'openai-compatible',model:'small',call:async()=>{throw Error('no model call here')}},thinking:normalize('off','openai-compatible'),context:{currentRequest:{message:'Parent',input:'Parent'},...(principal?{principal}:{}),...extra}} as AssistantSession)
const engineFor=(childStarted:EngineSession[]):Engine=>({id:'vercel',start(s){return managedContext(s,async function*(ready){
 const delegate=ready.values.hostTools.find(t=>t.name===DELEGATE_TOOL)
 if(delegate){await delegate.execute({tasks:[task]},ready.signal);yield {type:'message' as const,text:'done'};yield {type:'end' as const};return}
 childStarted.push(s)
 const tool=ready.values.hostTools.find(t=>t.name==='notes__read_note')!
 yield {type:'tool_call' as const,callId:'c1',name:tool.name,args:{}}
 await tool.execute({},ready.signal,'c1')
 yield {type:'tool_result' as const,callId:'c1',name:tool.name,isError:false,text:'note'}
 yield {type:'message' as const,text:'One finding.'};yield {type:'end' as const}
})}})
async function drive(given:AssistantSession,started:EngineSession[]=[]){const events:AssistantEvent[]=[];for await(const event of withDelegation(engineFor(started)).start(given))events.push(event);return events}
const lastTask=(events:AssistantEvent[])=>[...events].reverse().find(e=>e.type==='delegated_task') as Extract<AssistantEvent,{type:'delegated_task'}>

describe('the coordinator under a delegation',()=>{
 it("starts a child under the narrowed link, records the chain, and refuses what the link does not admit",async()=>{
  const ran:string[]=[],requests:ChildLinkRequest[]=[],started:EngineSession[]=[]
  const events=await drive(session([await readTool(ran)],input(['root'],request=>{requests.push(request);return {chain:text(['root','child'])}})),started)
  expect(requests.map(r=>[r.task.length>0,r.profile,r.tools])).toEqual([[true,'review',[{source:'notes',tool:'read_note'}]]])
  expect(started[0]!.context?.principal?.chain).toBe(text(['root','child']))
  expect(ran).toEqual(['read_note'])
  const last=lastTask(events)
  expect(last).toMatchObject({version:3,task:{state:'complete'},principal:{actor:{agent:'b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2'},rootActor:'b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1',chain:[chains.digests.root,chains.digests.child]}})
  expect(readRunEvent(JSON.parse(JSON.stringify(last)))).toEqual(last)
 })
 it('refuses a child whose link widens its parent: nothing of it starts, and the refusal and the failed task are recorded',async()=>{
  const ran:string[]=[],started:EngineSession[]=[]
  const events=await drive(session([await readTool(ran)],input(['root'],()=>({chain:text(['root','widened'])}))),started)
  expect(started).toEqual([])
  expect(ran).toEqual([])
  const refusal=events.find(e=>e.type==='guardrail'&&e.reason==='delegation refused')
  expect(refusal).toMatchObject({version:3,tool:DELEGATE_TOOL,refusal:{name:'widened'},principal:{chain:[chains.digests.root]}})
  expect(readRunEvent(JSON.parse(JSON.stringify(refusal)))).toEqual(refusal)
  expect(lastTask(events).task).toMatchObject({state:'failed',problem:expect.stringContaining('widened'),failure:{code:'permission',outcome:'not-started'}})
 })
 it('starts no child with no child port, or below a link that permits none',async()=>{
  for(const principal of [input(['root']),input(['root','child'],()=>{throw Error('never asked')})]){
   const ran:string[]=[],started:EngineSession[]=[]
   const events=await drive(session([await readTool(ran)],principal),started)
   expect(started).toEqual([])
   expect(ran).toEqual([])
   expect(lastTask(events).task).toMatchObject({state:'failed',problem:expect.stringContaining('delegation-not-permitted')})
  }
 })
 it('a run with no delegation delegates as before, with no principal on its records',async()=>{
  const ran:string[]=[],started:EngineSession[]=[]
  const events=await drive(session([await readTool(ran)]),started)
  expect(started.length).toBe(1)
  expect(JSON.stringify(events)).not.toContain('"principal"')
 })
})

describe("a lease holds a delegated run's principals",()=>{
 const binding=bindings.items[0] as never,otherActor=bindings.items[1] as never,otherSubject=bindings.items[2] as never,otherKind=bindings.items[3] as never
 const request=(owner:string,principal?:never)=>({resource:'run-delegated',owner,expectedRevision:null,durationMs:1000,...(principal?{principal}:{})})
 const refused=(run:()=>unknown)=>{try{run()}catch(error){return error instanceof LeaseRefused?error.reason:error};return undefined}
 it('binds at the first lease; a takeover is a new epoch of the same principals, any other is principal-changed',()=>{
  let at=0
  const table=new LeaseTable(()=>new Date(Date.UTC(2026,0,1)+at))
  const first=table.acquire(request('worker-a',binding),null)
  expect(first).toMatchObject({version:2,epoch:'1',principal:binding})
  expect(fenceOf(first)).toMatchObject({version:2,principal:binding})
  at=1000
  for(const other of [otherActor,otherSubject,otherKind,undefined])expect(refused(()=>table.acquire(request('worker-b',other),null))).toBe('principal-changed')
  const taken=table.acquire(request('worker-b',binding),null)
  expect(taken).toMatchObject({epoch:'2',owner:'worker-b'})
  expect(refused(()=>table.check({...fenceOf(taken),principal:otherActor}))).toBe('principal-changed')
  expect(refused(()=>table.check({version:1,resource:taken.resource,owner:taken.owner,epoch:taken.epoch}))).toBe('principal-changed')
  table.release(fenceOf(taken))
  expect(refused(()=>table.acquire(request('worker-c',otherActor),null))).toBe('principal-changed')
 })
 it('a version 1 lease cannot host a delegated run, and names no principal',()=>{
  let at=0
  const table=new LeaseTable(()=>new Date(Date.UTC(2026,0,1)+at))
  const plain=table.acquire(request('worker-a'),null)
  expect(plain.version).toBe(1)
  expect(Object.hasOwn(plain,'principal')).toBe(false)
  at=1000
  expect(refused(()=>table.acquire(request('worker-b',binding),null))).toBe('principal-changed')
 })
 it('the effect host refuses a dispatch under other principals, performing nothing',async()=>{
  const store=new MemoryRunStore({now:()=>new Date(Date.UTC(2026,0,1))})
  const lease=await store.acquireLease(request('worker-a',binding))
  const host=new ReferenceEffectHost(store),operation={run:'run-delegated',operation:'op-1',source:'notes',tool:'write_note',requestDigest:'sha256:'+'a'.repeat(64)}
  const perform=vi.fn(async()=>'done')
  await expect(host.dispatch(effectAdmission({...fenceOf(lease),principal:otherActor},operation),perform)).rejects.toMatchObject({reason:'principal-changed'})
  expect(perform).not.toHaveBeenCalled()
  expect(host.refusals).toEqual([{version:1,reason:'principal-changed'}])
  expect(await host.dispatch(effectAdmission(fenceOf(lease),operation),perform)).toBe('done')
 })
 it('a store that keeps no principals cannot host a delegated lease, and fails the kit by name',async()=>{
  class Plain extends MemoryRunStore {override readonly leasePrincipals=undefined as never}
  const fence={version:2 as const,resource:'run-delegated',owner:'worker-a',epoch:'1',principal:binding}
  expect(()=>fencedRunStore(new Plain(),fence)).toThrow(LeaseCapabilityRefused)
  expect(fencedRunStore(new MemoryRunStore(),fence)).toBeDefined()
  // A store that declares it and ignores what it was given.
  class Ignores extends MemoryRunStore {override acquireLease(acquire:Parameters<MemoryRunStore['acquireLease']>[0],signal?:AbortSignal){const {principal:_,...rest}=acquire;return super.acquireLease(rest,signal)}}
  await expect(runStoreLeaseConformance(now=>new Ignores({now}))).rejects.toEqual(expect.objectContaining({property:'lease.principal.version'}))
  expect(StorageConformanceFailure).toBeDefined()
 })
 it('a store that keeps the member in any other form fails the kit by name',async()=>{
  // The form a reader of Go's interface might write: a method, where TypeScript's declaration is the property true.
  class Method extends MemoryRunStore {override readonly leasePrincipals=(()=>true) as never}
  await expect(runStoreLeaseConformance(now=>new Method({now}))).rejects.toEqual(expect.objectContaining({property:'lease.principal.declared'}))
 })
 it("a store that forgives the fence's principals on its history commits fails the kit by name",async()=>{
  const rowFence=async(store:MemoryRunStore,fence:LeaseFence):Promise<LeaseFence>=>{const row=await store.readLease(fence.resource);return row?{...fence,version:row.version,...(row.principal?{principal:row.principal}:{})}:fence}
  class History extends MemoryRunStore {override async commitHistoryFenced(given:Parameters<MemoryRunStore['commitHistoryFenced']>[0],fence:LeaseFence,signal?:AbortSignal){return super.commitHistoryFenced(given,await rowFence(this,fence),signal)}}
  class Snapshot extends MemoryRunStore {override async commitContextSnapshotFenced(given:Parameters<MemoryRunStore['commitContextSnapshotFenced']>[0],commitId:string,fence:LeaseFence,signal?:AbortSignal){return super.commitContextSnapshotFenced(given,commitId,await rowFence(this,fence),signal)}}
  await expect(runStoreLeaseConformance(now=>new History({now}))).rejects.toEqual(expect.objectContaining({property:expect.stringMatching(/^lease\.principal\.history\./)}))
  await expect(runStoreLeaseConformance(now=>new Snapshot({now}))).rejects.toEqual(expect.objectContaining({property:expect.stringMatching(/^lease\.principal\.snapshot\./)}))
 })
 it('an acquisition with an unreadable binding is refused, and takes nothing',()=>{
  const table=new LeaseTable(()=>new Date(T))
  expect(()=>table.acquire({resource:'run-delegated',owner:'worker-a',expectedRevision:null,durationMs:1000,principal:{rootActor:'x'} as never},null)).toThrow(LeaseRefused)
  expect(()=>table.acquire({resource:'run-delegated',owner:'worker-a',expectedRevision:null,durationMs:1000,principal:{...binding as object,token:'x'} as never},null)).toThrow(LeaseRefused)
  expect(table.acquire({resource:'run-delegated',owner:'worker-a',expectedRevision:null,durationMs:1000,principal:binding},null).epoch).toBe('1')
 })
 it("a session on a fenced store is held to it even past a durable root's hold",async()=>{
  const at=()=>new Date(T)
  const opened=await openPrincipal(input(['root'])),store=new MemoryRunStore({now:at})
  const lease=await store.acquireLease({resource:'run-held',owner:'worker-a',expectedRevision:null,durationMs:60_000,principal:principalBinding(opened.record)})
  const ready=await durableSession(session([],input(['root']),{runStore:{store:fencedRunStore(store,fenceOf(lease)),id:'run-held'}}))
  const {principal:_,...rest}=ready.context!
  await expect(contextualSession({...ready,context:rest})).rejects.toThrow(/principal-changed/)
 })
 it("a session's child chain is held to its parent at the boundary, whoever issued it",async()=>{
  const parent=await openPrincipal(input(['root']))
  const given=session([],input(['otherActor']),{link:{attach(){},principal:()=>parent}})
  await expect(contextualSession(given)).rejects.toThrow(/delegation is refused \(chain-broken\)/)
 })
 it("a session's fenced store must hold the run's principals",async()=>{
  const at=()=>new Date(T)
  const opened=await openPrincipal(input(['root']))
  const holds=principalBinding(opened.record)
  const runWith=async(fenceBinding:unknown,principal?:RunPrincipal)=>{
   const store=new MemoryRunStore({now:at}),ran:string[]=[]
   const lease=await store.acquireLease({resource:'run-1',owner:'worker-a',expectedRevision:null,durationMs:60_000,...(fenceBinding?{principal:fenceBinding as never}:{})})
   const given=session([await readTool(ran)],principal)
   ;(given.context as Record<string,unknown>).runStore={store:fencedRunStore(store,fenceOf(lease)),id:'run-1'}
   const events:AssistantEvent[]=[]
   for await(const event of managedContext(given,async function*(){yield {type:'end' as const}}))events.push(event)
   return events.find(e=>e.type==='error')
  }
  expect(await runWith(holds,input(['root']))).toBeUndefined()
  expect(await runWith(undefined)).toBeUndefined()
  for(const [fence,principal] of [[undefined,input(['root'])],[bindings.items[1],input(['root'])],[holds,undefined]] as const)expect(JSON.stringify(await runWith(fence,principal)),JSON.stringify(fence)).toContain('principal-changed')
 })
})
void DelegationRefused

// Through the real driver: a delegated run parks, wakes and resumes.
describe("a delegated run's wait holds its principals",()=>{
 const HOUR=3_600_000,KEY='wk.tests.0001.Q7z2Xm9LpV4cR8tNb3Yh6Df1Gj5K',at={now:Date.parse('2026-01-01T00:00:00.000Z')}
 const record=recordVectors.items[0] as unknown as Parameters<typeof principalBinding>[0],binding=principalBinding(record)
 const leaseLifetime='durable' as const,waitLifetime:WaitLifetime='durable'
 class Durable extends MemoryRunStore {override readonly leaseLifetime=leaseLifetime;override readonly waitLifetime=waitLifetime}
 class DropsPrincipal extends Durable {override async resumeWait(claim:WaitClaim,signal?:AbortSignal){const answer=await super.resumeWait(claim,signal),{principal:_,...lease}=answer.lease;return {...answer,lease:{...lease,version:1 as const}}}}
 async function parked(store:MemoryRunStore){
  const lease=await store.acquireLease({resource:'run-approval',owner:'worker-a',expectedRevision:null,durationMs:HOUR,principal:binding})
  const checkpoint={...pauseVector.items[1]!.checkpoint,version:4,principal:record}
  await store.commitFenced({runId:'run-approval',expectedRevision:null,commitId:'pause',checkpoint:new TextEncoder().encode(canonicalJSON(checkpoint)),events:[]},fenceOf(lease))
  const {wait}=await parkRun({model:{provider:'openai',model:'fixture'},limits:{approvalTtlMs:7*24*HOUR},store,fence:fenceOf(lease),wait:'wait-1',continuation:'continue-1',condition:{event:{kind:'approval-decision',subject:'call_2_1'}},wakeKeyDigest:wakeKeyDigest(KEY),release:[]})
  expect(wait.pins.principal).toEqual(binding)
  await wakeRun(store,{version:1,resource:'run-approval',wait:'wait-1',evidence:'decision-1',kind:'event',event:{kind:'approval-decision',subject:'call_2_1'},key:KEY})
  const {model,...rest}=wait.pins,current={version:1 as const,resource:'run-approval',wait:'wait-1',...rest,model:{...model,status:'available' as const},policy:{digest:wait.pins.policy}}
  return {current,input:{store,resource:'run-approval',wait:'wait-1',evidence:'decision-1',owner:'worker-b',durationMs:HOUR,approval:{callId:'call_2_1',approved:true}}}
 }
 it('a park pins them, a resume needs them in its evidence, and its claim lease keeps them',async()=>{
  const store=new Durable({now:()=>new Date(at.now)}),{current,input}=await parked(store)
  const {principal:_,...none}=current
  await expect(resumeParked({...input,current:none})).rejects.toMatchObject({name:'ResumeRefused',reason:'evidence-missing'})
  await expect(resumeParked({...input,current:{...current,principal:{...binding,rootActor:'b2'.repeat(16)}}})).rejects.toMatchObject({name:'ResumeRefused',reason:'principal-changed'})
  expect(await store.readLease('run-approval')).toMatchObject({state:'released',epoch:'1'})
  const resumed=await resumeParked({...input,current})
  expect(resumed.lease).toMatchObject({version:2,epoch:'2',owner:'worker-b',principal:binding})
  expect(resumed.fence.principal).toEqual(binding)
 })
 it('a claim the store answers without them is refused',async()=>{
  const store=new DropsPrincipal({now:()=>new Date(at.now)}),{current,input}=await parked(store)
  await expect(resumeParked({...input,current})).rejects.toMatchObject({name:'WaitRefused',reason:'invalid'})
 })
})
