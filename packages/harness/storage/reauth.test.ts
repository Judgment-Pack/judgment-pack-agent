import {describe,it,expect,vi} from 'vitest'
import type {AssistantEvent,EngineSession,HostTool} from '../engine'
import {vercel} from '../engines/vercel'
import {answerWith} from '../testing/scenarioDriver'
import {toolListDigest} from '../toolSources'
import {canonicalJSON} from '../context/chat'
import {sha256} from '../records'
import {MemoryRunStore,type RunStore} from './run'
import {AIFailureError,failure} from '../failures'
import {ReauthorizationRequired,ReferenceEffectHost,certifyEffectHost,effectAdmission,fenceOf,fencedRunStore,reauthorizationOf,reauthorizationRequired,unattendedRunStore,type CredentialRefusal,type EffectAdmission,type EffectHost,type LeaseFence,type LeaseLifetime} from './lease'
import {ResumeRefused} from './resume'
import {REAUTH_REFUSAL_REASONS,ReauthorizationRefused,readReauthAction,readReauthDisposition,readReauthRefusal,readReauthorized,reauthActionDigest,reauthDispositionDigest,reauthorize} from './reauth'
import {exportWaits,holdsNeverAdmitted,parkRun,reauthorizedRunStore,resumeParked,wakeKeyDigest,wakeRun,waitReport,type WaitClaim,type WaitLifetime,type WakeEvidence} from './wait'
import dispositionVector from '../../../contract/vectors/reauth/disposition.json'
import actionVector from '../../../contract/vectors/reauth/action.json'
import refusalVector from '../../../contract/vectors/reauth/refusal.json'

/* #226: re-authorization waits, through the real Vercel engine, the package's writer and the reference effect host. The
 * Go twin is go/context/run_reauth_test.go with go/adapter/run_reauth_test.go. */
const T0=Date.parse('2026-01-01T00:00:00.000Z'),MINUTE=60_000,HOUR=60*MINUTE
const iso=(ms:number)=>new Date(ms).toISOString()
const reauthRefusal=(reason:string)=>expect.objectContaining({name:'ReauthorizationRefused',reason})
const resumeRefusal=(reason:string)=>expect.objectContaining({name:'ResumeRefused',reason})
const waitRefusal=(reason:string)=>expect.objectContaining({name:'WaitRefused',reason})
/** Stands for a host store kind whose leases and waits outlive its process. */
class DurableWaitStore extends MemoryRunStore {override readonly leaseLifetime:LeaseLifetime='durable';override readonly waitLifetime:WaitLifetime='durable'}
const MODEL={provider:'openai',model:'fixture'}
const KEY='wk.reauth.0001.Q7z2Xm9LpV4cR8tNb3Yh6Df1Gj5K'
/* Sentinel secrets the host holds: none of them may appear in anything the package writes, reports or sends. */
const TOKEN='SENTINEL-ACCESS-TOKEN-4f1c9e7a2b'
const REFRESHED='SENTINEL-REFRESHED-TOKEN-8d3b6a0e5c'
const PRINCIPAL='sentinel-person@example.invalid'
const PROVIDER_ERROR='SENTINEL-PROVIDER-ERROR invalid_grant: token expired for sentinel-person'
const principal=`sha256:${sha256(PRINCIPAL)}`
const REFUSED:CredentialRefusal={cause:'expired',connection:'tracker-conn',principal,permissions:['issues:read','issues:write'],requires:['issues:write']}
const ACT={name:'create_issue',description:'Create one issue.',inputSchema:{type:'object'},taxonomy:{operation:'act',effect:'act'} as const}
async function actTool(execute:HostTool['execute'],approve:'allow'|'ask'='allow'):Promise<HostTool>{
 return {...ACT,effectClass:'act',source:{kind:'custom',id:'tracker',trust:'person',version:'1',toolListDigest:await toolListDigest([ACT] as never),identity:{kind:'connection',label:'tracker'}},approve:()=>approve,execute} as HostTool
}
function provider(turns:Parameters<typeof answerWith>[0][]){
 const state={requests:0,bodies:[] as string[]}
 return {state,call:async(_path?:unknown,init?:{body?:unknown})=>{state.bodies.push(String(init?.body??''));const turn=turns[state.requests];state.requests++;if(!turn)throw Error('no scripted turn');return answerWith(turn,'stream',state.requests-1)}}
}
async function drive(store:RunStore,id:string,tools:HostTool[],call:(...args:never[])=>Promise<Response>,extra:{resume?:boolean;approval?:{callId:string;approved:boolean}}={}):Promise<AssistantEvent[]>{
 const session:EngineSession={prompt:'File the issue.',testPrompt:'',tools:[],callTool:vi.fn(async()=>({content:[]})),hostTools:tools,signal:new AbortController().signal,allowConversation:true,critic:'off',
  context:{currentRequest:{message:'File the issue.',input:'issue'},runStore:{store,id,...(extra.resume?{resume:true}:{})},...(extra.approval?{approval:extra.approval}:{})},model:{family:'openai-compatible',model:'fixture',call}} as unknown as EngineSession
 const out:AssistantEvent[]=[];for await(const event of vercel.start(session))out.push(event);return out
}
const CALL={call:{tool:'tracker__create_issue',arguments:{title:'Mileage'}}}
/** The host's credential store: it holds the token and knows when the connection's credentials are refused. */
function credentialHost(store:DurableWaitStore){
 const state:{refusal:CredentialRefusal|undefined;token:string;seen:string[]}={refusal:REFUSED,token:TOKEN,seen:[]}
 const host=new ReferenceEffectHost(store,{credentials:async operation=>{state.seen.push(`${operation.operation}:${state.token}`);return state.refusal}})
 return {state,host}
}
async function unchanged(store:RunStore,resource:string,wait:string){
 const held=(await MemoryRunStore.prototype.readWait.call(store,resource,wait))!
 const {model,...rest}=held.pins
 return {version:1,resource,wait,...rest,model:{...model,status:'available'},policy:{digest:held.pins.policy}}
}
const action=(over:Record<string,unknown>={})=>({version:1,resource:'run-r',wait:'wait-r',connection:'tracker-conn',action:'refresh',principal,permissions:['issues:write'],actor:'person-owner',at:iso(T0+2*MINUTE),...over})
/** A run whose act call the effect host refused, never admitted, because the connection's credentials expired. */
async function refused(make:(now:()=>Date)=>DurableWaitStore=now=>new DurableWaitStore({now})){
 const clock={at:T0},store=make(()=>new Date(clock.at)),{state,host}=credentialHost(store)
 const execute=vi.fn(async()=>({content:[{type:'text' as const,text:'created'}]}))
 const a=await store.acquireLease({resource:'run-r',owner:'worker-a',expectedRevision:null,durationMs:HOUR})
 const view=unattendedRunStore({store,fence:fenceOf(a),effectHost:host}),first=provider([CALL,{say:'Could not file it.'}])
 const events=await drive(view,'run-r',[await actTool(execute)],first.call)
 return {clock,store,state,host,execute,fence:fenceOf(a),view,events,first}
}
const parkInput=(store:RunStore,fence:LeaseFence,reauthorization:ReauthorizationRequired|undefined)=>({store,fence,wait:'wait-r',continuation:'k-r',condition:{event:{kind:'reauthorized',subject:'tracker-conn'}},model:MODEL,wakeKeyDigest:wakeKeyDigest(KEY),release:[],...(reauthorization?{reauthorization}:{})})
const wake:WakeEvidence={version:1,resource:'run-r',wait:'wait-r',evidence:'refreshed-1',kind:'event',event:{kind:'reauthorized',subject:'tracker-conn'},key:KEY}

// These drive the real engine several times in one test, and a shared runner can take tens of seconds over that.
vi.setConfig({testTimeout:120_000})

describe('shared re-authorization vectors (#226)',()=>{
 it('dispositions: readers, digests, and the heads that hold their operation as never admitted',()=>{
  dispositionVector.items.forEach((item,i)=>{expect(readReauthDisposition(structuredClone(item))).toEqual(item);expect(reauthDispositionDigest(item as never)).toBe(dispositionVector.digests[i])})
  for(const item of [...dispositionVector.refused,...dispositionVector.readerRefused])expect(()=>readReauthDisposition(structuredClone(item.disposition)),item.problem).toThrow(reauthRefusal('invalid'))
  for(const item of dispositionVector.heads)expect(holdsNeverAdmitted(new TextEncoder().encode(canonicalJSON(item.checkpoint)),dispositionVector.items[item.disposition] as never),item.case).toBe(item.holds)
 })
 it('actions and recorded refreshes: readers and digests, and what both languages decide for each action',()=>{
  actionVector.items.forEach((item,i)=>{expect(readReauthAction(structuredClone(item))).toEqual(item);expect(reauthActionDigest(item as never)).toBe(actionVector.digests[i])})
  for(const item of [...actionVector.refused,...actionVector.readerRefused])expect(()=>readReauthAction(structuredClone(item.action)),item.problem).toThrow(reauthRefusal('invalid'))
  for(const item of actionVector.reauthorized.items)expect(readReauthorized(structuredClone(item))).toEqual(item)
  for(const item of [...actionVector.reauthorized.refused,...actionVector.reauthorized.readerRefused])expect(()=>readReauthorized(structuredClone(item.reauthorized)),item.problem).toThrow(reauthRefusal('invalid'))
  for(const item of actionVector.cases){
   const wait={resource:item.wait.resource,wait:item.wait.wait,parked:{at:item.wait.parkedAt},pending:{kind:'reauthorization',connection:item.wait.connection},reauthorization:structuredClone(item.wait.reauthorization) as never}
   const run=()=>reauthorize(wait,structuredClone(item.action))
   const outcome=item.outcome as {reauthorized?:number;refused?:string;by?:string}
   if(outcome.reauthorized!==undefined)expect(run(),item.case).toEqual(actionVector.reauthorized.items[outcome.reauthorized])
   else expect(run,item.case).toThrow(outcome.by==='resume'?resumeRefusal(outcome.refused!):reauthRefusal(outcome.refused!))
  }
 })
 it('refusals: every reason both languages give, and nothing else',()=>{
  expect(refusalVector.items.map(item=>readReauthRefusal(item).reason)).toEqual([...REAUTH_REFUSAL_REASONS])
  expect(refusalVector.items.map(item=>new ReauthorizationRefused(item.reason as never).record())).toEqual(refusalVector.items)
  for(const item of refusalVector.refused)expect(()=>readReauthRefusal(item)).toThrow(reauthRefusal('invalid'))
 })
})

describe('#226: the actual host boundary tells never-admitted from an unknown dispatch',()=>{
 it('a refusal at the effect host\'s admission boundary stops the run there: nothing admitted, nothing performed, the model never told',async()=>{
  const {store,execute,view,events,first}=await refused()
  expect(execute).not.toHaveBeenCalled()
  // The writer recorded the typed never-admitted failure and stopped: no second model request, nothing after the call.
  expect(first.state.requests).toBe(1)
  // The call's result is the typed refusal; the run then ends, its progress after the refusal deliberately not saved.
  const result=events.find(e=>e.type==='tool_result') as Extract<AssistantEvent,{type:'tool_result'}>
  expect(result).toMatchObject({isError:true,structured:{failure:{code:'authentication',source:'tool',outcome:'not-started'}}})
  expect(events.at(-1)!.type).toBe('end');expect(events.some(e=>e.type==='error')).toBe(true)
  const head=JSON.parse(new TextDecoder().decode((await store.load('run-r'))!.checkpoint!))
  const last=head.durable.dispatches.at(-1)
  expect(last).toMatchObject({kind:'host',state:'unknown',read:false,failure:{code:'authentication',source:'tool',outcome:'not-started'}})
  // The proof is the live refusal the package trusted for this view.
  const proof=reauthorizationOf(view)!
  expect(proof).toBeInstanceOf(ReauthorizationRequired)
 })
 it('an error the tool returns after admission (a raw 401) is no proof: the run goes on, the outcome is unknown and nothing parks',async()=>{
  const clock={at:T0},store=new DurableWaitStore({now:()=>new Date(clock.at)}),host=new ReferenceEffectHost(store)
  const execute=vi.fn(async()=>{throw Object.assign(new Error(PROVIDER_ERROR),{status:401})})
  const a=await store.acquireLease({resource:'run-u',owner:'worker-a',expectedRevision:null,durationMs:HOUR})
  const view=unattendedRunStore({store,fence:fenceOf(a),effectHost:host}),model=provider([CALL,{say:'It failed.'}])
  await drive(view,'run-u',[await actTool(execute)],model.call)
  expect(execute).toHaveBeenCalledOnce();expect(model.state.requests).toBe(2)
  expect(reauthorizationOf(view)).toBeUndefined()
  // A refusal object made by anyone is no proof, and parks nothing.
  await expect(parkRun({...parkInput(store,fenceOf(a),new ReauthorizationRequired()),wait:'wait-u'})).rejects.toEqual(reauthRefusal('untrusted'))
  expect(await store.readWait('run-u','wait-u')).toBeUndefined()
 })
 it('a refusal is trusted only from the run\'s proven effect host, minted once for the live admission, before anything is performed, without a credential in it',async()=>{
  type Mint=(admission:import('./lease').EffectAdmission,perform:()=>Promise<unknown>)=>Promise<unknown>
  const cases:[string,Mint,boolean][]=[
   ['a well-formed refusal for the live admission',async admission=>{throw reauthorizationRequired(admission,REFUSED)},true],
   ['a copy of the admission',async admission=>{throw reauthorizationRequired(structuredClone(admission),REFUSED)},false],
   ['minted twice',async admission=>{reauthorizationRequired(admission,REFUSED);throw reauthorizationRequired(admission,REFUSED)},false],
   ['performed, then refused',async(admission,perform)=>{await perform();throw reauthorizationRequired(admission,REFUSED)},false],
   ['refused, then performed',async(admission,perform)=>{const error=reauthorizationRequired(admission,REFUSED);await perform();throw error},false],
   ['a token in the refusal',async admission=>{throw reauthorizationRequired(admission,{...REFUSED,token:TOKEN} as never)},false],
   ['a raw provider error in the refusal',async admission=>{throw reauthorizationRequired(admission,{...REFUSED,error:PROVIDER_ERROR} as never)},false],
   ['a principal that is not a digest',async admission=>{throw reauthorizationRequired(admission,{...REFUSED,principal:PRINCIPAL})},false],
   ['a connection reference that is not bounded',async admission=>{throw reauthorizationRequired(admission,{...REFUSED,connection:`Bearer ${TOKEN}`})},false],
  ]
  for(const [name,mint,trusted] of cases){
   const store=new DurableWaitStore()
   // A host certified for the store (#253): the reference host's own admission for the kit's operations (source host),
   // and the case's credential refusal in front of the run's.
   const create=(authority:import('./lease').LeaseAuthority):EffectHost=>{const inner=new ReferenceEffectHost(authority);return {effectFencing:'enforced',dispatch:<T,>(admission:import('./lease').EffectAdmission,perform:()=>Promise<T>)=>admission.operation.source!=='host'?mint(admission,perform) as Promise<T>:inner.dispatch(admission,perform)}}
   const certified=await certifyEffectHost(create,store)
   const a=await store.acquireLease({resource:'run-m',owner:'worker-a',expectedRevision:null,durationMs:HOUR})
   const view=unattendedRunStore({store,fence:fenceOf(a),effectHost:certified}),model=provider([CALL,{say:'.'}])
   await drive(view,'run-m',[await actTool(vi.fn(async()=>({content:[{type:'text' as const,text:'created'}]})))],model.call)
   expect(reauthorizationOf(view)!==undefined,name).toBe(trusted)
   expect(model.state.requests,name).toBe(trusted?1:2)
   // The same well-formed mint from a host nobody proved for the store is no proof.
   if(trusted){
    const other=new DurableWaitStore(),b=await other.acquireLease({resource:'run-m',owner:'worker-a',expectedRevision:null,durationMs:HOUR})
    const unproven=fencedRunStore(other,fenceOf(b),create(other))
    await drive(unproven,'run-m',[await actTool(vi.fn(async()=>({content:[{type:'text' as const,text:'created'}]})))],provider([CALL,{say:'.'}]).call)
    expect(reauthorizationOf(unproven),`${name}: an unproven host`).toBeUndefined()
   }
  }
  // Through the proven reference host, each well-formed and malformed refusal its credential check gives.
  for(const [name,refusal,trusted] of [['well formed',REFUSED,true],['a token in it',{...REFUSED,token:TOKEN},false],['a raw provider error in it',{...REFUSED,error:PROVIDER_ERROR},false],['an undigested principal',{...REFUSED,principal:PRINCIPAL},false],['unsorted permissions',{...REFUSED,permissions:['issues:write','issues:read']},false]] as const){
   const store=new DurableWaitStore(),host=new ReferenceEffectHost(store,{credentials:async()=>refusal as CredentialRefusal})
   const a=await store.acquireLease({resource:'run-p',owner:'worker-a',expectedRevision:null,durationMs:HOUR})
   const view=unattendedRunStore({store,fence:fenceOf(a),effectHost:host}),model=provider([CALL,{say:'.'}])
   const execute=vi.fn(async()=>({content:[{type:'text' as const,text:'created'}]}))
   await drive(view,'run-p',[await actTool(execute)],model.call)
   expect(execute,name).not.toHaveBeenCalled()
   expect(reauthorizationOf(view)!==undefined,name).toBe(trusted)
   // Untrusted: an ordinary failure; the run went on to the model, which was told.
   expect(model.state.requests,name).toBe(trusted?1:2)
  }
 },120_000)
 it('a refusal minted after the dispatch ended is not trusted',async()=>{
  const store=new DurableWaitStore(),reference=new ReferenceEffectHost(store)
  let kept:import('./lease').EffectAdmission|undefined
  const host:EffectHost=Object.freeze({effectFencing:'enforced' as const,dispatch:<T,>(admission:import('./lease').EffectAdmission,perform:()=>Promise<T>)=>{kept=admission;return reference.dispatch(admission,perform)}})
  const a=await store.acquireLease({resource:'run-l',owner:'worker-a',expectedRevision:null,durationMs:HOUR})
  const view=fencedRunStore(store,fenceOf(a),host)
  await drive(view,'run-l',[await actTool(vi.fn(async()=>({content:[{type:'text' as const,text:'created'}]})))],provider([CALL,{say:'Filed.'}]).call)
  const late=reauthorizationRequired(kept!,REFUSED)
  await expect(parkRun({...parkInput(store,fenceOf(a),late),wait:'wait-l'})).rejects.toEqual(reauthRefusal('untrusted'))
 })
})

describe('#226: a trusted refusal parks the run as needs_reauthorization; the resume revalidates; the continuation sends the operation once',()=>{
 it('same-identity refresh: park, wake, the person\'s refresh, the revalidation, the claim, and the never-admitted call sent once',async()=>{
  const {clock,store,state,host,execute,fence,view}=await refused()
  const proof=reauthorizationOf(view)!
  const {wait,attention}=await parkRun(parkInput(store,fence,proof))
  expect(wait).toMatchObject({reason:'reauthorization',pending:{kind:'reauthorization',connection:'tracker-conn',cause:'expired'},reauthorization:{cause:'expired',connection:'tracker-conn',principal,fence,permissions:['issues:read','issues:write'],requires:['issues:write']},state:'parked'})
  const head=JSON.parse(new TextDecoder().decode((await store.load('run-r'))!.checkpoint!)).durable.dispatches.at(-1)
  expect(wait.pending).toEqual({kind:'reauthorization',operation:head.id,source:head.source,tool:head.tool,connection:'tracker-conn',cause:'expired'})
  expect(wait.reauthorization!.operation).toEqual({run:'run-r',operation:head.id,source:head.source,tool:head.tool,requestDigest:head.requestDigest})
  expect(attention).toMatchObject({reason:'reauthorization',needs:'reauthorization',subject:'tracker-conn'})
  expect((await store.readLease('run-r'))!.state).toBe('released')
  // The park closes the run; the proof parks one wait.
  await expect(store.acquireLease({resource:'run-r',owner:'worker-x',expectedRevision:wait.checkpoint.revision,durationMs:HOUR})).rejects.toEqual(waitRefusal('wait-pending'))
  await expect(parkRun({...parkInput(store,fence,proof),wait:'wait-again'})).rejects.toEqual(reauthRefusal('used'))
  expect(reauthorizationOf(view)).toBeUndefined()
  clock.at=T0+3*MINUTE
  await wakeRun(store,wake)
  const input={store,resource:'run-r',wait:'wait-r',evidence:'refreshed-1',owner:'worker-b',durationMs:HOUR,current:await unchanged(store,'run-r','wait-r')}
  // A wake is never the re-authorization; a replacement or another principal is a changed identity, never a refresh.
  await expect(resumeParked(input)).rejects.toEqual(waitRefusal('decision-required'))
  await expect(resumeParked({...input,reauthorization:action({action:'replacement'})})).rejects.toEqual(resumeRefusal('identity-changed'))
  await expect(resumeParked({...input,reauthorization:action({principal:`sha256:${sha256('another-person@example.invalid')}`})})).rejects.toEqual(resumeRefusal('identity-changed'))
  // Narrower permissions after the refresh: they no longer cover what the operation requires.
  await expect(resumeParked({...input,reauthorization:action({permissions:['issues:read']})})).rejects.toEqual(reauthRefusal('permission-insufficient'))
  // An action from before the park is stale.
  await expect(resumeParked({...input,reauthorization:action({at:iso(T0-MINUTE)})})).rejects.toEqual(reauthRefusal('stale'))
  await expect(resumeParked({...input,reauthorization:action({wait:'wait-x'})})).rejects.toEqual(reauthRefusal('invalid'))
  expect((await store.readWait('run-r','wait-r'))!.state).toBe('woken')
  // A narrower set that still covers the operation continues.
  const resumed=await resumeParked({...input,reauthorization:action()})
  expect(resumed.reauthorized).toMatchObject({actor:'person-owner',principal,permissions:['issues:write']})
  expect((await store.readWait('run-r','wait-r'))!.reauthorized).toEqual(resumed.reauthorized)
  // The host refreshed the credentials: the continuation re-sends the exact never-admitted request once.
  state.refusal=undefined;state.token=REFRESHED
  const continued=await reauthorizedRunStore({store,effectHost:host,resumed})
  const second=provider([CALL,{say:'Filed.'}])
  const events=await drive(continued,'run-r',[await actTool(execute)],second.call,{resume:true})
  expect(events.some(e=>e.type==='error')).toBe(false)
  expect(execute).toHaveBeenCalledOnce()
  const report=await waitReport(store,'run-r')
  expect(report.entries.map(e=>[e.decision,e.reason])).toEqual([['park','reauthorization'],['wake','reauthorization'],['resume','reauthorization']])
 })
 it('re-authorization is never retry permission: the grant sends the one never-admitted operation once, and nothing that may have run',async()=>{
  const {clock,store,state,host,execute,fence,view}=await refused()
  await parkRun(parkInput(store,fence,reauthorizationOf(view)!))
  clock.at=T0+3*MINUTE;await wakeRun(store,wake)
  const resumed=await resumeParked({store,resource:'run-r',wait:'wait-r',evidence:'refreshed-1',owner:'worker-b',durationMs:HOUR,current:await unchanged(store,'run-r','wait-r'),reauthorization:action()})
  state.refusal=undefined
  // The re-sent operation's outcome is lost (the effect ran; its result was not acknowledged): asked again, it is unknown.
  execute.mockImplementationOnce(async()=>{throw new Error('the connection dropped')})
  const continued=await reauthorizedRunStore({store,effectHost:host,resumed})
  await drive(continued,'run-r',[await actTool(execute)],provider([CALL,CALL,{say:'.'}]).call,{resume:true})
  expect(execute).toHaveBeenCalledOnce()
  // Its effect host admitted it once; a second admission of the same identity is refused, whatever the lease.
  await expect(host.dispatch(effectAdmission(resumed.fence,(await store.readWait('run-r','wait-r'))!.reauthorization!.operation),async()=>'again')).rejects.toEqual(expect.objectContaining({name:'LeaseRefused',reason:'already-admitted'}))
 })
 it('without a grant nothing the run holds as unknown is sent again, fenced or not: re-authorization is the only way back, and only for the never-admitted operation',async()=>{
  // Unfenced: the call's outcome is unknown (the connection dropped); asked again on the continuation, it is not run.
  const store=new MemoryRunStore(),execute=vi.fn(async():Promise<{content:{type:'text';text:string}[]}>=>{throw new Error('the connection dropped')})
  await drive(store,'run-n',[await actTool(execute)],provider([CALL,{say:'It may have run.'}]).call)
  expect(execute).toHaveBeenCalledOnce()
  execute.mockImplementation(async()=>({content:[{type:'text' as const,text:'created'}]}))
  await drive(store,'run-n',[await actTool(execute)],provider([CALL,{say:'.'}]).call,{resume:true})
  expect(execute).toHaveBeenCalledOnce()
  // Fenced, after a trusted refusal: with the credentials current, a continuation on a view without a resumed wait's
  // grant does not send the never-admitted call again either. The Go twin is
  // TestReauthorizationNoUnknownDispatchIsSentAgainWithoutAGrant.
  const {store:held,state,host,execute:refusedCall,fence,view}=await refused()
  expect(reauthorizationOf(view)).toBeDefined()
  state.refusal=undefined
  await drive(unattendedRunStore({store:held,fence,effectHost:host}),'run-r',[await actTool(refusedCall)],provider([CALL,{say:'.'}]).call,{resume:true})
  expect(refusedCall).not.toHaveBeenCalled()
 })
 it('a stale proof: minted under another lease than the park\'s, or for a head that moved, parks nothing',async()=>{
  const {clock,store,view,fence}=await refused()
  const proof=reauthorizationOf(view)!
  // The lease the proof was minted under expired; another worker leases the run.
  clock.at=T0+2*HOUR
  const b=await store.acquireLease({resource:'run-r',owner:'worker-b',expectedRevision:(await store.load('run-r'))!.revision,durationMs:HOUR})
  await expect(parkRun(parkInput(store,fenceOf(b),proof))).rejects.toEqual(reauthRefusal('stale'))
  await expect(parkRun(parkInput(store,fence,proof))).rejects.toEqual(expect.objectContaining({name:'LeaseRefused',reason:'stale-epoch'}))
  // The head moved past the refused operation: the proof no longer names the head's never-admitted dispatch.
  const head=(await store.load('run-r'))!,moved=JSON.parse(new TextDecoder().decode(head.checkpoint!))
  moved.durable.dispatches.at(-1).state='result'
  await store.commitFenced({runId:'run-r',expectedRevision:head.revision,commitId:'moved',checkpoint:new TextEncoder().encode(canonicalJSON(moved)),events:[]},fenceOf(b))
  const c=fenceOf(b)
  await expect(parkRun(parkInput(store,c,proof))).rejects.toEqual(reauthRefusal('stale'))
  expect(await store.readWait('run-r','wait-r')).toBeUndefined()
  expect(ResumeRefused).toBeDefined();expect(ReauthorizationRefused).toBeDefined()
 })
})

describe('#226: credentials, tokens and raw provider errors are never published',()=>{
 it('sentinel secrets reach no checkpoint, event, wait, attention, export, report, prompt or error',async()=>{
  const {clock,store,state,host,execute,fence,view,events,first}=await refused()
  await parkRun(parkInput(store,fence,reauthorizationOf(view)!))
  clock.at=T0+3*MINUTE;await wakeRun(store,wake)
  const resumed=await resumeParked({store,resource:'run-r',wait:'wait-r',evidence:'refreshed-1',owner:'worker-b',durationMs:HOUR,current:await unchanged(store,'run-r','wait-r'),reauthorization:action()})
  state.refusal=undefined;state.token=REFRESHED
  const second=provider([CALL,{say:'Filed.'}])
  const later=await drive(await reauthorizedRunStore({store,effectHost:host,resumed}),'run-r',[await actTool(execute)],second.call,{resume:true})
  // The host's credential check saw its token: the package never did.
  expect(state.seen.some(s=>s.includes(TOKEN))).toBe(true)
  const pages:string[]=[]
  for(let after:string|null=null;;){const page=await store.readEvents('run-r',after,100);pages.push(...page.items.map(i=>new TextDecoder().decode(i.bytes)));if(!page.hasMore||page.nextCursor===null)break;after=page.nextCursor}
  const published=[
   new TextDecoder().decode((await store.load('run-r'))!.checkpoint!),...pages,
   JSON.stringify(await store.readWait('run-r','wait-r')),JSON.stringify(await exportWaits(store,'run-r')),JSON.stringify(await waitReport(store,'run-r',new Date(T0+HOUR))),
   JSON.stringify(events),JSON.stringify(later),...first.state.bodies,...second.state.bodies,
  ].join('\n')
  for(const secret of [TOKEN,REFRESHED,PRINCIPAL,PROVIDER_ERROR,'invalid_grant'])expect(published.includes(secret),secret).toBe(false)
 })
})

describe('#226: an approved call refused for its credentials: the approval\'s ceiling and binding hold across the reauthorization wait',()=>{
 /** An act call asked, parked with a one-hour approval, approved and claimed; its dispatch then refused for credentials. */
 async function approvedThenRefused(make:(now:()=>Date)=>DurableWaitStore=now=>new DurableWaitStore({now})){
  const clock={at:T0},store=make(()=>new Date(clock.at)),{state,host}=credentialHost(store)
  state.refusal=undefined
  const execute=vi.fn(async()=>({content:[{type:'text' as const,text:'created'}]}))
  const a=await store.acquireLease({resource:'run-r',owner:'worker-a',expectedRevision:null,durationMs:HOUR})
  const events=await drive(unattendedRunStore({store,fence:fenceOf(a),effectHost:host}),'run-r',[await actTool(execute,'ask')],provider([CALL]).call)
  const asked=events.find(e=>e.type==='approval_requested') as Extract<AssistantEvent,{type:'approval_requested'}>
  await parkRun({store,fence:fenceOf(a),wait:'wait-a',continuation:'k-a',condition:{event:{kind:'approval-decision',subject:asked.callId}},model:MODEL,limits:{approvalTtlMs:HOUR},wakeKeyDigest:wakeKeyDigest('wk.reauth.appr.Hs8Pq3Lz6Wd1Rm9Tc4Vb7Nx2'),release:[]})
  clock.at=T0+MINUTE
  await wakeRun(store,{version:1,resource:'run-r',wait:'wait-a',evidence:'decision-1',kind:'event',event:{kind:'approval-decision',subject:asked.callId},key:'wk.reauth.appr.Hs8Pq3Lz6Wd1Rm9Tc4Vb7Nx2'})
  const approved=await resumeParked({store,resource:'run-r',wait:'wait-a',evidence:'decision-1',owner:'worker-b',durationMs:HOUR,approval:{callId:asked.callId,approved:true},current:await unchanged(store,'run-r','wait-a')})
  // The credentials expired meanwhile: the approved call is refused at the effect host's admission boundary.
  state.refusal=REFUSED
  const view=unattendedRunStore({store,fence:approved.fence,effectHost:host})
  await drive(view,'run-r',[await actTool(execute,'ask')],provider([{say:'Filed.'}]).call,{resume:true,approval:approved.approval!})
  expect(execute).not.toHaveBeenCalled()
  return {clock,store,state,host,execute,view,approved,asked}
 }
 it('the wait carries the unsettled approval\'s binding and expiry, the claim is bounded by it, and ADR-0008 never sends an approved call again',async()=>{
  const {clock,store,state,host,execute,view,approved}=await approvedThenRefused()
  const {wait}=await parkRun(parkInput(store,approved.fence,reauthorizationOf(view)!))
  expect(wait.approval).toEqual(approved.wait.approval)
  expect(wait.deadline).toMatchObject({at:iso(T0+HOUR),by:'approval',approval:iso(T0+HOUR)})
  clock.at=T0+10*MINUTE;await wakeRun(store,wake)
  const current=await unchanged(store,'run-r','wait-r')
  // A policy changed under a trusted layer is accepted for a run, but not for a call the person reviewed under the old one.
  await expect(resumeParked({store,resource:'run-r',wait:'wait-r',evidence:'refreshed-1',owner:'worker-c',durationMs:24*HOUR,current:{...current,policy:{digest:'sha256:'+'b'.repeat(64),layer:'sha256:'+'c'.repeat(64)}},reauthorization:action({at:iso(T0+9*MINUTE)})})).rejects.toEqual(resumeRefusal('approval-changed'))
  const resumed=await resumeParked({store,resource:'run-r',wait:'wait-r',evidence:'refreshed-1',owner:'worker-c',durationMs:24*HOUR,current,reauthorization:action({at:iso(T0+9*MINUTE)})})
  expect(resumed.lease.expiry).toBe(iso(T0+HOUR))
  state.refusal=undefined
  // ADR-0008: an approved call whose result was never written is restored as executed with an unknown outcome and is
  // not sent again, whatever the re-authorization; the person decides again in a new request.
  await drive(await reauthorizedRunStore({store,effectHost:host,resumed}),'run-r',[await actTool(execute,'ask')],provider([{say:'Not filed.'}]).call,{resume:true})
  expect(execute).not.toHaveBeenCalled()
  const head=JSON.parse(new TextDecoder().decode((await store.load('run-r'))!.checkpoint!))
  expect(['executed','answered']).toContain(head.approval.state)
  expect(head.approval.result).toMatchObject({isError:true,structuredContent:{failure:{code:'unknown',outcome:'unknown'}}})
 })
 it('the approved call\'s descriptor is pinned: changed, it needs an admission, and an admission of it cannot carry the reviewed call',async()=>{
  const {clock,store,view,approved}=await approvedThenRefused()
  const {wait}=await parkRun(parkInput(store,approved.fence,reauthorizationOf(view)!))
  clock.at=T0+10*MINUTE;await wakeRun(store,wake)
  const pin=wait.pins.sources.find(s=>s.source==='tracker')!,other='sha256:'+'d'.repeat(64)
  expect(pin.descriptorDigest).toBeDefined()
  const current=await unchanged(store,'run-r','wait-r')
  const sources=current.sources.map(s=>s.source==='tracker'?{...s,descriptorDigest:other}:s)
  const input={store,resource:'run-r',wait:'wait-r',evidence:'refreshed-1',owner:'worker-c',durationMs:HOUR,reauthorization:action({at:iso(T0+9*MINUTE)})}
  await expect(resumeParked({...input,current:{...current,sources}})).rejects.toEqual(resumeRefusal('reference-changed'))
  await expect(resumeParked({...input,current:{...current,sources,admissions:[{kind:'reference',subject:'source:tracker',from:pin.descriptorDigest!,to:other,actor:'person-owner',at:iso(T0+9*MINUTE)}]}})).rejects.toEqual(resumeRefusal('approval-changed'))
  expect((await store.readWait('run-r','wait-r'))!.state).toBe('woken')
 })
 it('a claim whose lease outlives the attached approval is refused invalid, nothing handed to the host',async()=>{
  class Forged extends DurableWaitStore {override async resumeWait(claim:WaitClaim){const out=structuredClone(await super.resumeWait(claim));if(claim.reauthorized)out.lease.expiry=iso(T0+24*HOUR);return out}}
  const {clock,store,view,approved}=await approvedThenRefused(now=>new Forged({now}))
  await parkRun(parkInput(store,approved.fence,reauthorizationOf(view)!))
  clock.at=T0+10*MINUTE;await wakeRun(store,wake)
  await expect(resumeParked({store,resource:'run-r',wait:'wait-r',evidence:'refreshed-1',owner:'worker-c',durationMs:HOUR,current:await unchanged(store,'run-r','wait-r'),reauthorization:action({at:iso(T0+9*MINUTE)})})).rejects.toEqual(waitRefusal('invalid'))
 })
 it('at the approval\'s expiry the reauthorization wait is not claimed, and its deadline wake ends it',async()=>{
  const {clock,store,view,approved}=await approvedThenRefused()
  await parkRun(parkInput(store,approved.fence,reauthorizationOf(view)!))
  clock.at=T0+10*MINUTE;await wakeRun(store,wake)
  clock.at=T0+HOUR
  await expect(resumeParked({store,resource:'run-r',wait:'wait-r',evidence:'refreshed-1',owner:'worker-c',durationMs:HOUR,current:await unchanged(store,'run-r','wait-r'),reauthorization:action({at:iso(T0+9*MINUTE)})})).rejects.toEqual(waitRefusal('deadline-passed'))
  expect(await wakeRun(store,{version:1,resource:'run-r',wait:'wait-r',evidence:'deadline-1',kind:'deadline',key:KEY})).toMatchObject({kind:'deadline',at:iso(T0+HOUR)})
  expect((await store.readWait('run-r','wait-r'))!.state).toBe('expired')
  await expect(store.acquireLease({resource:'run-r',owner:'worker-x',expectedRevision:(await store.load('run-r'))!.revision,durationMs:HOUR})).rejects.toEqual(waitRefusal('closed'))
 })
})

/** A run refused, parked, woken and claimed: the credentials current again, the continuation not yet run. */
async function claimed(make?:(now:()=>Date)=>DurableWaitStore,proofs:ReauthorizationRequired[]=[]){
 const r=await refused(make)
 proofs.push(reauthorizationOf(r.view)!)
 await parkRun(parkInput(r.store,r.fence,proofs[0]!))
 r.clock.at=T0+3*MINUTE;await wakeRun(r.store,wake)
 const resumed=await resumeParked({store:r.store,resource:'run-r',wait:'wait-r',evidence:'refreshed-1',owner:'worker-b',durationMs:HOUR,current:await unchanged(r.store,'run-r','wait-r'),reauthorization:action()})
 r.state.refusal=undefined
 return {...r,resumed}
}
const hostRows=async(store:RunStore,id='run-r')=>JSON.parse(new TextDecoder().decode((await store.load(id))!.checkpoint!)).durable.dispatches.filter((row:{kind:string})=>row.kind==='host') as {id:string;state:string;failure?:Record<string,unknown>}[]
const UNKNOWN_FAILURE={version:1,code:'authentication',source:'tool',outcome:'unknown'}
/** A host certified for the store: the reference host's admission for the kit's operations (source host), the case's body for the run's. */
const certifiedAs=(body:(admission:EffectAdmission,perform:()=>Promise<unknown>,inner:EffectHost)=>Promise<unknown>)=>(authority:import('./lease').LeaseAuthority):EffectHost=>{
 const inner=new ReferenceEffectHost(authority)
 return {effectFencing:'enforced',dispatch:<T,>(admission:EffectAdmission,perform:()=>Promise<T>)=>admission.operation.source!=='host'?body(admission,perform,inner) as Promise<T>:inner.dispatch(admission,perform)}
}

describe('#226 H1: the typed failure means a refusal the package trusted, so nothing admitted is sent again',()=>{
 it('performed, then refused: the row and the model hear an unknown outcome, with nothing said about admission',async()=>{
  const store=new DurableWaitStore(),execute=vi.fn(async()=>({content:[{type:'text' as const,text:'created'}]}))
  const certified=await certifyEffectHost(certifiedAs(async(admission,perform)=>{await perform();throw reauthorizationRequired(admission,REFUSED)}),store)
  const a=await store.acquireLease({resource:'run-m',owner:'worker-a',expectedRevision:null,durationMs:HOUR})
  const view=unattendedRunStore({store,fence:fenceOf(a),effectHost:certified}),model=provider([CALL,{say:'.'}])
  const events=await drive(view,'run-m',[await actTool(execute)],model.call)
  expect(execute).toHaveBeenCalledOnce();expect(reauthorizationOf(view)).toBeUndefined()
  const rows=await hostRows(store,'run-m')
  expect(rows).toHaveLength(1);expect(rows[0]).toMatchObject({state:'unknown',failure:UNKNOWN_FAILURE});expect(rows[0]!.failure!.detail).toBeUndefined()
  expect(events.find(e=>e.type==='tool_result')).toMatchObject({isError:true,structured:{failure:UNKNOWN_FAILURE}})
  expect(model.state.requests).toBe(2);expect(model.state.bodies.join('\n').includes('not admitted')).toBe(false)
  expect(holdsNeverAdmitted((await store.load('run-m'))!.checkpoint,dispositionVector.items[0] as never)).toBe(false)
 })
 const thrown:[string,(proof:ReauthorizationRequired)=>Error][]=[
  ['the exported class',()=>new ReauthorizationRequired()],
  ['a typed failure of the same shape',()=>new AIFailureError({...failure('authentication','tool','not-started'),detail:'SENTINEL-HOST-DETAIL the operation was not admitted'})],
  ['the run\'s own proof, kept and thrown again',proof=>proof],
 ]
 for(const [name,fails] of thrown)it(`the re-send is admitted, then fails with ${name}: no later view sends it again, whatever the effect host remembers`,async()=>{
  const proofs:ReauthorizationRequired[]=[]
  const {store,host,execute,resumed}=await claimed(undefined,proofs)
  // The park used the proof; the object stays in the host's hands.
  execute.mockImplementationOnce(async()=>{throw fails(proofs[0]!)})
  const v1=await reauthorizedRunStore({store,effectHost:host,resumed}),first=provider([CALL,{say:'.'}])
  const events=await drive(v1,'run-r',[await actTool(execute)],first.call,{resume:true})
  expect(execute).toHaveBeenCalledOnce();expect(reauthorizationOf(v1)).toBeUndefined()
  // Admitted: the row says only that its outcome is unknown, and the run went on to the model.
  const row=(await hostRows(store)).at(-1)!
  expect(row.state).toBe('unknown');expect(row.failure).toEqual(UNKNOWN_FAILURE)
  expect(events.find(e=>e.type==='tool_result')).toMatchObject({isError:true,structured:{failure:UNKNOWN_FAILURE}})
  expect(first.state.requests).toBe(2);expect(first.state.bodies.join('\n').includes('SENTINEL-HOST-DETAIL')).toBe(false)
  // A second view of the claim, over the same host: not sent, so the host is not even asked.
  await drive(await reauthorizedRunStore({store,effectHost:host,resumed}),'run-r',[await actTool(execute)],provider([CALL,{say:'.'}]).call,{resume:true})
  expect(execute).toHaveBeenCalledOnce();expect(host.refusals).toEqual([])
  // Over a new reference host, as after a restart under the held lease: its admissions are gone, and it is still not sent.
  await drive(await reauthorizedRunStore({store,effectHost:new ReferenceEffectHost(store),resumed}),'run-r',[await actTool(execute)],provider([CALL,{say:'.'}]).call,{resume:true})
  expect(execute).toHaveBeenCalledOnce()
 })
 it('a proof kept from another dispatch and thrown by a tool stops nothing and proves nothing',async()=>{
  const kept=reauthorizationOf((await refused()).view)!
  const store=new MemoryRunStore(),execute=vi.fn(async()=>{throw kept}),model=provider([CALL,{say:'It may have run.'}])
  await drive(store,'run-n',[await actTool(execute)],model.call)
  expect(model.state.requests).toBe(2)
  expect((await hostRows(store,'run-n'))[0]).toMatchObject({state:'unknown',failure:UNKNOWN_FAILURE})
 })
 it('a crash after the re-send is claimed and performed: the head holds a claim with no failure, and no later view runs it',async()=>{
  const {store,host,execute,resumed}=await claimed()
  // The process dies as the head takes the re-send's claim: every commit after it is lost, the effect having been performed.
  const commit=store.commitFenced.bind(store),history=store.commitHistoryFenced.bind(store)
  let dead=false
  const dying=(bytes:Uint8Array|null)=>{if(dead)throw new Error('the process died');if(bytes&&(JSON.parse(new TextDecoder().decode(bytes)).durable?.dispatches??[]).some((row:{kind:string;state:string})=>row.kind==='host'&&row.state==='claimed'))dead=true}
  store.commitFenced=async(input,fence,signal)=>{dying(input.checkpoint);return commit(input,fence,signal)}
  store.commitHistoryFenced=async(input,fence,signal)=>{dying(input.run.checkpoint);return history(input,fence,signal)}
  await drive(await reauthorizedRunStore({store,effectHost:host,resumed}),'run-r',[await actTool(execute)],provider([CALL,{say:'.'}]).call,{resume:true})
  expect(dead).toBe(true);expect(execute).toHaveBeenCalledOnce()
  const row=(await hostRows(store)).at(-1)!
  expect(row.state).toBe('claimed');expect(row.failure).toBeUndefined()
  store.commitFenced=commit;store.commitHistoryFenced=history
  // A new process: a new reference host, whose admissions are gone, over the same claim.
  await drive(await reauthorizedRunStore({store,effectHost:new ReferenceEffectHost(store),resumed}),'run-r',[await actTool(execute)],provider([CALL,{say:'.'}]).call,{resume:true})
  expect(execute).toHaveBeenCalledOnce()
 })
})

describe('#226 M1: the credential check does not reopen the fence',()=>{
 const operation={run:'run-t',operation:'op-1',source:'tracker',tool:'create_issue',requestDigest:'sha256:'+'2'.repeat(64)}
 it('the lease is lost during the check: nothing is admitted or performed, through the real engine and at the host',async()=>{
  const clock={at:T0},store=new DurableWaitStore({now:()=>new Date(clock.at)}),execute=vi.fn(async()=>({content:[{type:'text' as const,text:'created'}]}))
  const host=new ReferenceEffectHost(store,{credentials:async()=>{
   clock.at=T0+2*HOUR
   await store.acquireLease({resource:'run-t',owner:'worker-b',expectedRevision:(await store.load('run-t'))!.revision,durationMs:HOUR})
   return undefined
  }})
  const a=await store.acquireLease({resource:'run-t',owner:'worker-a',expectedRevision:null,durationMs:HOUR})
  await drive(unattendedRunStore({store,fence:fenceOf(a),effectHost:host}),'run-t',[await actTool(execute)],provider([CALL,{say:'.'}]).call)
  expect(execute).not.toHaveBeenCalled()
  expect(host.refusals).toEqual([{version:1,reason:'stale-epoch'}])
 })
 it('the identity stays free: the takeover\'s own dispatch of it is admitted',async()=>{
  const clock={at:T0},store=new DurableWaitStore({now:()=>new Date(clock.at)})
  let lose=true
  const host=new ReferenceEffectHost(store,{credentials:async()=>{if(lose){clock.at=T0+2*HOUR;await store.acquireLease({resource:'run-t',owner:'worker-b',expectedRevision:null,durationMs:HOUR});lose=false}return undefined}})
  const a=await store.acquireLease({resource:'run-t',owner:'worker-a',expectedRevision:null,durationMs:HOUR})
  let performed=0
  await expect(host.dispatch(effectAdmission(fenceOf(a),operation),async()=>{performed++})).rejects.toEqual(expect.objectContaining({name:'LeaseRefused',reason:'stale-epoch'}))
  expect(performed).toBe(0)
  await host.dispatch(effectAdmission({...fenceOf(a),owner:'worker-b',epoch:'2'},operation),async()=>{performed++})
  expect(performed).toBe(1)
 })
 it('the run aborts during the check: nothing is admitted or performed, and a refusal is not minted, whatever its authority looks at',async()=>{
  const store=new DurableWaitStore(),blind={checkFence:(fence:Parameters<DurableWaitStore['checkFence']>[0])=>store.checkFence(fence)}
  for(const authority of [store,blind])for(const answer of [undefined,REFUSED]){
   const controller=new AbortController()
   const host=new ReferenceEffectHost(authority,{credentials:async()=>{controller.abort();return answer}})
   const a=await store.acquireLease({resource:'run-a',owner:'worker-a',expectedRevision:null,durationMs:HOUR})
   let performed=0
   await expect(host.dispatch(effectAdmission(fenceOf(a),{...operation,run:'run-a'}),async()=>{performed++},controller.signal)).rejects.toEqual(expect.objectContaining({name:'AbortError'}))
   expect(performed).toBe(0)
   await store.releaseLease(fenceOf(a))
  }
 })
 it('the run aborts and the check throws: the answer is the abort',async()=>{
  const store=new DurableWaitStore(),controller=new AbortController()
  const host=new ReferenceEffectHost(store,{credentials:async()=>{controller.abort();throw new Error('the check failed too')}})
  const a=await store.acquireLease({resource:'run-a',owner:'worker-a',expectedRevision:null,durationMs:HOUR})
  let performed=0
  await expect(host.dispatch(effectAdmission(fenceOf(a),{...operation,run:'run-a'}),async()=>{performed++},controller.signal)).rejects.toEqual(expect.objectContaining({name:'AbortError'}))
  expect(performed).toBe(0)
 })
})

describe('#226 M2: a perform after the dispatch ended',()=>{
 it('a refusal followed by a late perform: nothing runs, the refusal is no proof, and no re-send can run it twice',async()=>{
  const store=new DurableWaitStore(),execute=vi.fn(async()=>({content:[{type:'text' as const,text:'created'}]}))
  let late:Promise<unknown>|undefined
  const certified=await certifyEffectHost(certifiedAs(async(admission,perform)=>{const error=reauthorizationRequired(admission,REFUSED);setTimeout(()=>{late=perform().catch(reason=>reason)},0);throw error}),store)
  const a=await store.acquireLease({resource:'run-r',owner:'worker-a',expectedRevision:null,durationMs:HOUR})
  const view=unattendedRunStore({store,fence:fenceOf(a),effectHost:certified})
  await drive(view,'run-r',[await actTool(execute)],provider([CALL,{say:'.'}]).call)
  await new Promise(resolve=>setTimeout(resolve,20))
  await expect(late).resolves.toEqual(expect.objectContaining({name:'LeaseRefused',reason:'invalid'}))
  expect(execute).not.toHaveBeenCalled();expect(reauthorizationOf(view)).toBeUndefined()
 })
 it('after the park, a late perform still runs nothing: the re-send is the one run',async()=>{
  const clock={at:T0},store=new DurableWaitStore({now:()=>new Date(clock.at)}),execute=vi.fn(async()=>({content:[{type:'text' as const,text:'created'}]}))
  let perform:(()=>Promise<unknown>)|undefined,refuse=true
  const certified=await certifyEffectHost(certifiedAs(async(admission,given,inner)=>{if(!refuse)return inner.dispatch(admission,given);perform=given;throw reauthorizationRequired(admission,REFUSED)}),store)
  const a=await store.acquireLease({resource:'run-r',owner:'worker-a',expectedRevision:null,durationMs:HOUR})
  const view=unattendedRunStore({store,fence:fenceOf(a),effectHost:certified})
  await drive(view,'run-r',[await actTool(execute)],provider([CALL,{say:'.'}]).call)
  await parkRun(parkInput(store,fenceOf(a),reauthorizationOf(view)!))
  await expect(perform!()).rejects.toEqual(expect.objectContaining({name:'LeaseRefused',reason:'invalid'}))
  expect(execute).not.toHaveBeenCalled()
  clock.at=T0+3*MINUTE;await wakeRun(store,wake)
  const resumed=await resumeParked({store,resource:'run-r',wait:'wait-r',evidence:'refreshed-1',owner:'worker-b',durationMs:HOUR,current:await unchanged(store,'run-r','wait-r'),reauthorization:action()})
  refuse=false
  await drive(await reauthorizedRunStore({store,effectHost:certified,resumed}),'run-r',[await actTool(execute)],provider([CALL,{say:'.'}]).call,{resume:true})
  expect(execute).toHaveBeenCalledOnce()
 })
})

describe('#226 M6, M3: the re-send\'s own safeguards',()=>{
 it('M3: a re-send refused again stops the run and cannot park; on the same view the grant is spent, and another view of the claim sends it once',async()=>{
  const {store,state,host,execute,resumed}=await claimed()
  state.refusal=REFUSED
  const v1=await reauthorizedRunStore({store,effectHost:host,resumed}),second=provider([CALL,{say:'.'}])
  await drive(v1,'run-r',[await actTool(execute)],second.call,{resume:true})
  expect(execute).not.toHaveBeenCalled();expect(second.state.requests).toBe(1)
  const proof=reauthorizationOf(v1)!
  expect(proof).toBeDefined()
  // The continuation's own model row follows the refused row, so the head no longer holds it as its last dispatch.
  await expect(parkRun({...parkInput(store,resumed.fence,proof),wait:'wait-r2',wakeKeyDigest:wakeKeyDigest('wk.reauth.0002.Hs8Pq3Lz6Wd1Rm9Tc4Vb7Nx2Ab7Q')})).rejects.toEqual(reauthRefusal('unbound'))
  expect((await hostRows(store)).at(-1)).toMatchObject({state:'unknown',failure:{code:'authentication',source:'tool',outcome:'not-started'}})
  state.refusal=undefined
  await drive(v1,'run-r',[await actTool(execute)],provider([CALL,{say:'.'}]).call,{resume:true})
  expect(execute).not.toHaveBeenCalled()
  await drive(await reauthorizedRunStore({store,effectHost:host,resumed}),'run-r',[await actTool(execute)],provider([CALL,{say:'.'}]).call,{resume:true})
  expect(execute).toHaveBeenCalledOnce()
 })
 it('a write with an immutable hint has no read cache (#271): the re-send runs once and a repeat is answered from its record',async()=>{
  const {store,host,execute,resumed}=await claimed()
  const view=await reauthorizedRunStore({store,effectHost:host,resumed})
  await drive(view,'run-r',[{...await actTool(execute),contextPolicy:{immutable:true}} as HostTool],provider([CALL,CALL,{say:'.'}]).call,{resume:true})
  expect(execute).toHaveBeenCalledOnce();expect((await hostRows(store)).map(row=>row.state)).toEqual(['result'])
 })
 it('a takeover gets no grant: its fence is not the claim\'s, and the old fence no longer holds the lease',async()=>{
  const {clock,store,host,execute,resumed}=await claimed()
  clock.at=T0+3*MINUTE+2*HOUR
  const x=await store.acquireLease({resource:'run-r',owner:'worker-x',expectedRevision:(await store.load('run-r'))!.revision,durationMs:HOUR})
  await expect(reauthorizedRunStore({store,effectHost:host,resumed:{fence:fenceOf(x),wait:{resource:'run-r',wait:'wait-r'}}})).rejects.toEqual(waitRefusal('invalid'))
  await expect(reauthorizedRunStore({store,effectHost:host,resumed})).rejects.toEqual(expect.objectContaining({name:'LeaseRefused',reason:'stale-epoch'}))
  await drive(unattendedRunStore({store,fence:fenceOf(x),effectHost:host}),'run-r',[await actTool(execute)],provider([CALL,{say:'.'}]).call,{resume:true})
  expect(execute).not.toHaveBeenCalled()
 })
 it('the view needs the claim\'s lease held: released, it is refused',async()=>{
  const {store,host,resumed}=await claimed()
  await store.releaseLease(resumed.fence)
  await expect(reauthorizedRunStore({store,effectHost:host,resumed})).rejects.toEqual(expect.objectContaining({name:'LeaseRefused',reason:'stale-epoch'}))
 })
 it('an admitted identity is already-admitted before the credential check is asked: an admitted operation gets no refusal',async()=>{
  const store=new DurableWaitStore(),state:{refusal:CredentialRefusal|undefined}={refusal:undefined}
  const asked=vi.fn(async()=>state.refusal),host=new ReferenceEffectHost(store,{credentials:asked})
  const a=await store.acquireLease({resource:'run-a',owner:'worker-a',expectedRevision:null,durationMs:HOUR})
  const operation={run:'run-a',operation:'op-1',source:'tracker',tool:'create_issue',requestDigest:'sha256:'+'2'.repeat(64)}
  await host.dispatch(effectAdmission(fenceOf(a),operation),async()=>'performed')
  state.refusal=REFUSED
  await expect(host.dispatch(effectAdmission(fenceOf(a),operation),async()=>'again')).rejects.toEqual(expect.objectContaining({name:'LeaseRefused',reason:'already-admitted'}))
  expect(asked).toHaveBeenCalledOnce()
 })
 it('a claim that did not record the refresh it was given is refused invalid',async()=>{
  class Forged extends DurableWaitStore {override async resumeWait(claim:WaitClaim){const out=structuredClone(await super.resumeWait(claim));if(out.wait.reauthorized)out.wait.reauthorized={...out.wait.reauthorized,permissions:['issues:read','issues:write']};return out}}
  const r=await refused(now=>new Forged({now}))
  await parkRun(parkInput(r.store,r.fence,reauthorizationOf(r.view)!))
  r.clock.at=T0+3*MINUTE;await wakeRun(r.store,wake)
  await expect(resumeParked({store:r.store,resource:'run-r',wait:'wait-r',evidence:'refreshed-1',owner:'worker-b',durationMs:HOUR,current:await unchanged(r.store,'run-r','wait-r'),reauthorization:action()})).rejects.toEqual(waitRefusal('invalid'))
 })
})

describe('#226 M9: host-authored text stays out of the records',()=>{
 const SECRET='SENTINEL-CHECK-DETAIL-token-4f1c'
 it('a credential check that throws, a typed failure with a detail included: nothing of it is recorded, sent or reported',async()=>{
  for(const fails of [new Error(SECRET),new AIFailureError({...failure('authentication','tool','not-started'),detail:SECRET}),new AIFailureError({...failure('unavailable','tool','unknown'),detail:SECRET}),Object.assign(new Error(SECRET),{status:401,responseBody:SECRET})]){
   const store=new DurableWaitStore(),host=new ReferenceEffectHost(store,{credentials:async()=>{throw fails}}),execute=vi.fn(async()=>({content:[{type:'text' as const,text:'created'}]}))
   const a=await store.acquireLease({resource:'run-s',owner:'worker-a',expectedRevision:null,durationMs:HOUR})
   const view=unattendedRunStore({store,fence:fenceOf(a),effectHost:host}),model=provider([CALL,{say:'.'}])
   const events=await drive(view,'run-s',[await actTool(execute)],model.call)
   expect(execute).not.toHaveBeenCalled();expect(reauthorizationOf(view)).toBeUndefined()
   const row=(await hostRows(store,'run-s'))[0]!
   expect(row.state).toBe('unknown');expect(row.failure).toMatchObject({code:'unknown',source:'tool',outcome:'unknown'})
   const pages:string[]=[]
   for(let after:string|null=null;;){const page=await store.readEvents('run-s',after,100);pages.push(...page.items.map(i=>new TextDecoder().decode(i.bytes)));if(!page.hasMore||page.nextCursor===null)break;after=page.nextCursor}
   const published=[new TextDecoder().decode((await store.load('run-s'))!.checkpoint!),...pages,JSON.stringify(events),...model.state.bodies].join('\n')
   expect(published.includes(SECRET)).toBe(false)
  }
 })
 it('an actor that is free text, an address or a bearer string is refused: nothing is claimed or exported',async()=>{
  const r=await refused()
  await parkRun(parkInput(r.store,r.fence,reauthorizationOf(r.view)!))
  r.clock.at=T0+3*MINUTE;await wakeRun(r.store,wake)
  const input={store:r.store,resource:'run-r',wait:'wait-r',evidence:'refreshed-1',owner:'worker-b',durationMs:HOUR,current:await unchanged(r.store,'run-r','wait-r')}
  for(const actor of [`Bearer ${TOKEN} ${PRINCIPAL}`,PRINCIPAL,'a'.repeat(65)])await expect(resumeParked({...input,reauthorization:action({actor})})).rejects.toEqual(reauthRefusal('invalid'))
  expect(JSON.stringify(await exportWaits(r.store,'run-r')).includes(TOKEN)).toBe(false)
  expect((await r.store.readWait('run-r','wait-r'))!.state).toBe('woken')
 })
})
