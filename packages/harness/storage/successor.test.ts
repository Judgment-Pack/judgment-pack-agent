// @vitest-environment node
// #340: a successor run of a complete run's last question carries the allowance the person approved, is made once, and
// hands its first request the person's answer. #341: a claimed question wait continues a complete head. The Go twins are
// go/context/run_successor_test.go and go/adapter/completetests/successor_test.go.
import {describe,expect,it,vi} from 'vitest'
import type {AssistantEvent,EngineSession,ModelRequest} from '../engine'
import {vercel} from '../engines/vercel'
import {answerWith} from '../testing/scenarioDriver'
import {canonicalJSON} from '../context/chat'
import {questionTool,type QuestionDraft,type UserQuestion} from '../questions'
import {CHECKPOINT_SCHEMA,RECORD_DEFAULTS,RecordsExpired,recordDigest,writeRecord} from '../records'
import {readExecution} from '../execution/state'
import {MemoryRunStore,checkpointCallbackRunStore,type RunCommit,type RunReceipt,type RunStore} from './run'
import type {RunHistoryCommit,RunHistoryReceipt} from './history'
import {fenceOf,fencedRunStore,type LeaseFence,type LeaseLifetime} from './lease'
import {parkRun,resumeParked,wakeKeyDigest,wakeRun,type WaitLifetime} from './wait'
import {RunComplete,durableSession,questionAnswers} from './writer'
import {QUESTION_ANSWERED,SuccessorRefused,makeSuccessor,questionAnswerText,successorRunId,successorVersion,type QuestionAnswer,type RunSuccessor} from './successor'
import vector from '../../../contract/vectors/checkpoint/successor.json'
import members from '../../../contract/vectors/checkpoint/members.json'
import nativeCheckpoints from '../../../contract/vectors/plan/native-checkpoints.json'

const T0=Date.parse('2026-01-01T00:00:00.000Z'),MINUTE=60_000,HOUR=60*MINUTE
const KEY='wk.tests.0001.Q7z2Xm9LpV4cR8tNb3Yh6Df1Gj5K'
const PREDECESSOR='run-asks',SUCCESSOR=successorRunId(PREDECESSOR,'question-1')
const QUESTION=vector.answer.question as UserQuestion
/** Stands for a host store kind whose leases and waits outlive its process. */
class DurableWaitStore extends MemoryRunStore {override readonly leaseLifetime:LeaseLifetime='durable';override readonly waitLifetime:WaitLifetime='durable'}
const encode=(value:unknown)=>new TextEncoder().encode(canonicalJSON(value))
const decode=(bytes:Uint8Array)=>new TextDecoder().decode(bytes)
const refused=(reason:string)=>expect.objectContaining({name:'SuccessorRefused',reason})
const waitRefused=(reason:string)=>expect.objectContaining({name:'WaitRefused',reason})

interface Scene {clock:{at:number};store:DurableWaitStore;fence:LeaseFence;revision:string;answer:QuestionDraft}
/** The vector's predecessor: a complete head whose log ends with question-1, an earlier question-0 before it, and an
 * allowance raised by a grant past 20 requests. */
async function scene(edit?:(checkpoint:Record<string,unknown>)=>void,Store:new(options:{now:()=>Date})=>DurableWaitStore=DurableWaitStore):Promise<Scene>{
 const clock={at:T0},store=new Store({now:()=>new Date(clock.at)})
 const lease=await store.acquireLease({resource:PREDECESSOR,owner:'worker-a',expectedRevision:null,durationMs:HOUR})
 const checkpoint=structuredClone(vector.made.predecessor) as Record<string,unknown>
 edit?.(checkpoint)
 const envelope=(id:string,question:unknown)=>encode({version:1,scope:{execution:PREDECESSOR,role:'author'},event:{type:'user_question',id,question}})
 const receipt=await store.commitFenced({runId:PREDECESSOR,expectedRevision:null,commitId:'asked',checkpoint:encode(checkpoint),events:[envelope('question-0',{...QUESTION,title:'Earlier'}),envelope('question-1',QUESTION)]},fenceOf(lease))
 // The run that asked has ended: its worker's lease is released.
 await store.releaseLease(fenceOf(lease))
 return {clock,store,fence:fenceOf(lease),revision:receipt.revision,answer:{selected:['risk'],text:''}}
}
const open=(context:Record<string,unknown>)=>durableSession({prompt:'',testPrompt:'',tools:[],callTool:vi.fn(),hostTools:[],signal:new AbortController().signal,context} as unknown as EngineSession)
const successorOf=(s:Scene,over:Partial<RunSuccessor>={}):RunSuccessor=>({predecessor:PREDECESSOR,question:'question-1',answer:s.answer,...over})
const asSuccessor=(s:Scene,store:RunStore=s.store,resume=false,over:Partial<RunSuccessor>={},extra:Record<string,unknown>={},runStore:Record<string,unknown>={})=>{
 const asked=successorOf(s,over)
 return open({runStore:{store,id:successorRunId(asked.predecessor,asked.question),...(resume?{resume:true}:{}),successor:asked,...runStore},...extra})
}
const answersOf=(session:EngineSession)=>(session.context as Record<symbol,QuestionAnswer[]|undefined>)[questionAnswers]
const logged=async(store:RunStore,run:string)=>(await store.readEvents(run,null,100)).items.length
/** Parks the question under a worker's lease of the head, as the run that asked it would. */
async function park(s:Scene,wait='wait-q',question='question-1',store:RunStore=s.store,fence?:LeaseFence){
 s.fence=fence??fenceOf(await s.store.acquireLease({resource:PREDECESSOR,owner:'worker-a',expectedRevision:s.revision,durationMs:HOUR}))
 return parkRun({store,fence:s.fence,wait,continuation:'continue-q',condition:{event:{kind:'question-answer',subject:question}},timeoutAt:new Date(s.clock.at+2*HOUR).toISOString(),wakeKeyDigest:wakeKeyDigest(KEY),release:[],question,model:{provider:'openai',model:'fixture'}})
}
/** Delivers the person's answer to the question wait: answered, not yet claimed. */
async function wake(s:Scene){
 s.clock.at+=MINUTE
 await wakeRun(s.store,{version:1,resource:PREDECESSOR,wait:'wait-q',evidence:'answer-1',kind:'event',event:{kind:'question-answer',subject:'question-1'},answer:s.answer,actor:'person-reviewer',key:KEY})
}
async function claim(s:Scene){
 if((await s.store.readWait(PREDECESSOR,'wait-q'))?.state==='parked')await wake(s)
 const held=(await s.store.readWait(PREDECESSOR,'wait-q'))!,{model,...rest}=held.pins
 return resumeParked({store:s.store,resource:PREDECESSOR,wait:'wait-q',evidence:'answer-1',owner:'worker-b',durationMs:HOUR,current:{version:1,resource:PREDECESSOR,wait:'wait-q',...rest,model:{...model,status:'available'},policy:{digest:held.pins.policy}}})
}

describe('the shared successor vector (#340)',()=>{
 it('the derived id, each refusal as the host hears it, and the answer block',()=>{
  expect(vector.derivations).toHaveLength(4)
  for(const row of vector.derivations)expect(successorRunId(row.predecessor,row.question),`${row.predecessor}/${row.question}`).toBe(row.id)
  // A storage id holds no control character, so the NUL-separated derivation is one id per pair.
  expect(new Set(vector.derivations.map(row=>row.id)).size).toBe(4)
  expect(vector.reasons).toEqual(['invalid','unbound','budgeted','succeeded','not-complete','exhausted','parked','moved'])
  vector.reasons.forEach((reason,i)=>{const error=new SuccessorRefused(reason as never);expect({version:1,type:'error',message:error.message,failure:error.failure}).toEqual(vector.items[i])})
  expect(vector.items.at(-1)).toEqual({version:1,type:'end'})
  expect(questionAnswerText(QUESTION,vector.answer.answered.answer,vector.answer.marker)).toBe(vector.answer.answered.text)
  expect(questionAnswerText(QUESTION,undefined,vector.answer.marker)).toBe(vector.answer.unanswered.text)
 })
})

describe('a successor run (#340)',()=>{
 it('carries the predecessor\'s allowance and nothing else, is made once, and continues as a successor',async()=>{
  const s=await scene(),before=await logged(s.store,PREDECESSOR)
  const made=await asSuccessor(s)
  const head=(await s.store.load(SUCCESSOR))!
  expect(canonicalJSON(JSON.parse(decode((await s.store.load(PREDECESSOR))!.checkpoint!)))).toBe(canonicalJSON(vector.made.predecessor))
  expect(decode(head.checkpoint!)).toBe(canonicalJSON(vector.made.checkpoint))
  expect(made.context?.execution).toEqual(readExecution(vector.made.checkpoint))
  expect(answersOf(made)).toEqual([{question:QUESTION,answer:{selected:['risk'],text:''}}])
  // The predecessor's head and log are read, never written.
  expect((await s.store.load(PREDECESSOR))!.revision).toBe(s.revision);expect(await logged(s.store,PREDECESSOR)).toBe(before)
  // A second successor of the same question is the one already made.
  await expect(asSuccessor(s)).rejects.toEqual(refused('succeeded'))
  // The successor continues as a successor; the predecessor resumes no other way, and its question is not parked.
  expect(answersOf(await asSuccessor(s,s.store,true))).toHaveLength(1)
  await expect(open({runStore:{store:s.store,id:PREDECESSOR,resume:true}})).rejects.toBeInstanceOf(RunComplete)
  await expect(park(s)).rejects.toEqual(waitRefused('nothing-pending'))
 })
 it('is made under the predecessor\'s lease, released after the create',async()=>{
  let s:Scene|undefined
  s=await scene(undefined,hooked(async()=>{
   expect(await s!.store.readLease(PREDECESSOR)).toMatchObject({owner:SUCCESSOR,state:'held'})
   // A worker that would park the question cannot lease the head meanwhile.
   await expect(s!.store.acquireLease({resource:PREDECESSOR,owner:'worker-a',expectedRevision:s!.revision,durationMs:HOUR})).rejects.toMatchObject({name:'LeaseRefused',reason:'held'})
  }))
  await asSuccessor(s)
  expect(await s.store.readLease(PREDECESSOR)).toMatchObject({owner:SUCCESSOR,state:'released'})
 })
 const cases:[string,string,((c:Record<string,unknown>)=>void)|undefined,(s:Scene)=>Promise<unknown>][]=[
  ['an empty predecessor','invalid',undefined,s=>open({runStore:{store:s.store,id:SUCCESSOR,successor:successorOf(s,{predecessor:''})}})],
  ['no successor record','invalid',undefined,s=>open({runStore:{store:s.store,id:SUCCESSOR,successor:'question-1'}})],
  ['an execution beside it','invalid',undefined,s=>asSuccessor(s,s.store,false,{},{execution:readExecution(vector.made.checkpoint)})],
  ['a question continuation beside it','invalid',undefined,s=>asSuccessor(s,s.store,false,{},{questionContinuation:'wait-q'})],
  ['a job continuation beside it','invalid',undefined,s=>asSuccessor(s,s.store,false,{},{jobContinuation:{job:'job-1'}})],
  ['a usage budget beside it','invalid',undefined,s=>asSuccessor(s,s.store,false,{},{usageBudget:{}})],
  ['a chat context beside it','invalid',undefined,s=>asSuccessor(s,s.store,false,{},{},{context:{}})],
  ['a store that is one writer\'s callback','invalid',undefined,s=>asSuccessor(s,checkpointCallbackRunStore(async()=>{},()=>undefined))],
  ['an id not derived','unbound',undefined,s=>open({runStore:{store:s.store,id:'successor-chosen',successor:successorOf(s)}})],
  ['an earlier question','unbound',undefined,s=>asSuccessor(s,s.store,false,{question:'question-0'})],
  ['a blank answer','unbound',undefined,s=>{s.answer={selected:[],text:'  '};return asSuccessor(s)}],
  ['an option not offered','unbound',undefined,s=>{s.answer={selected:['elsewhere'],text:''};return asSuccessor(s)}],
  ['a predecessor not there','not-complete',undefined,s=>asSuccessor(s,s.store,false,{predecessor:'run-none'})],
  ['a predecessor still running','not-complete',c=>{c.status='running'},s=>asSuccessor(s)],
  ['a predecessor head of another run','not-complete',c=>{c.id='run-other'},s=>asSuccessor(s)],
  ['a budgeted predecessor','budgeted',c=>{c.version=3;c.usageBudget=structuredClone((members.items as Record<string,unknown>[]).find(item=>item.usageBudget!==undefined)!.usageBudget)},s=>asSuccessor(s)],
  ['an exhausted predecessor','exhausted',c=>{c.used=30},s=>asSuccessor(s)],
  ['an exhausted provider-managed predecessor','exhausted',c=>{c.unit='provider-managed';c.used=1;c.limit=1;c.grants=[]},s=>asSuccessor(s)],
  ['a question with its own wait','parked',undefined,async s=>{await park(s);return asSuccessor(s)}],
 ]
 for(const [name,reason,edit,make] of cases)it(`is refused by name, and nothing is written: ${name}`,async()=>{
  const s=await scene(edit)
  const error=await make(s).then(()=>undefined,error=>error)
  expect(error).toEqual(refused(reason));expect(error.message).toBe(`SuccessorRefused: ${reason}.`)
  for(const id of [SUCCESSOR,successorRunId(PREDECESSOR,'question-0'),successorRunId('run-none','question-1'),'successor-chosen'])expect(await s.store.load(id)).toBeUndefined()
 })
 const capped=(c:Record<string,unknown>)=>{c.used=3;c.limit=5;c.requestCap=5;c.grants=[]}
 for(const [name,edit,requestCap,taken] of [['none for none',undefined,undefined,true],['the same cap',capped,5,true],['a cap where none was',undefined,5,false],['none where one was',capped,undefined,false],['a higher cap',capped,6,false],['a lower cap',capped,4,false]] as const)it(`is made only under the predecessor's request cap: ${name}`,async()=>{
  const s=await scene(edit)
  const error=await asSuccessor(s,s.store,false,{},requestCap===undefined?{}:{requestCap}).then(()=>undefined,error=>error)
  expect(!!await s.store.load(SUCCESSOR)).toBe(taken)
  if(taken)expect(error).toBeUndefined()
  else expect(error?.message).toBe('The fixed request cap must match the admitted API execution and cannot change after initialization.')
 })
 it('carries a delegated predecessor\'s principal, and is not made under another principal or none',async()=>{
  const principal=(members.items[0] as Record<string,unknown>).principal
  const s=await scene(c=>{c.version=4;c.principal=principal})
  const error=await asSuccessor(s).then(()=>undefined,error=>error)
  expect(error?.message).toBe('The run continues under another principal than it began under (principal-changed). Nothing was sent.')
  expect(await s.store.load(SUCCESSOR)).toBeUndefined()
 })
})

describe('a claimed question wait continues a complete head (#341)',()=>{
 it('only as the claimed wait\'s continuation at the head it was parked at, with the wait\'s own answer',async()=>{
  const s=await scene()
  await park(s)
  const continuation=(wait:string)=>open({runStore:{store:s.store,id:PREDECESSOR,resume:true},questionContinuation:wait})
  // Parked, or answered and not yet claimed: a complete head is not resumed.
  await expect(continuation('wait-q')).rejects.toBeInstanceOf(RunComplete)
  await wake(s)
  await expect(continuation('wait-q')).rejects.toBeInstanceOf(RunComplete)
  const resumed=await claim(s)
  // #355: through the plain store, whose commits no lease stops, the claim admits nothing: the head is complete.
  await expect(continuation('wait-q')).rejects.toBeInstanceOf(RunComplete)
  const view=fencedRunStore(s.store,resumed.fence),through=(wait:string)=>open({runStore:{store:view,id:PREDECESSOR,resume:true},questionContinuation:wait})
  expect(answersOf(await through('wait-q'))).toEqual([{question:QUESTION,answer:{selected:['risk'],text:''}}])
  // Another wait, or none: refused as before. A continuation needs the store's head.
  await expect(through('wait-other')).rejects.toBeInstanceOf(RunComplete)
  await expect(open({runStore:{store:view,id:PREDECESSOR,resume:true}})).rejects.toBeInstanceOf(RunComplete)
  await expect(open({questionContinuation:'wait-q'})).rejects.toEqual(waitRefused('invalid'))
  // The question was answered through its wait: no successor of it.
  await expect(asSuccessor(s)).rejects.toEqual(refused('parked'))
  // Once the head moves, the continuation delivered again is refused.
  const head=(await s.store.load(PREDECESSOR))!
  await view.commit({runId:PREDECESSOR,expectedRevision:head.revision,commitId:'moved',checkpoint:head.checkpoint!,events:[]})
  await expect(through('wait-q')).rejects.toBeInstanceOf(RunComplete)
 })
})

describe('a question continuation of a running head (#341)',()=>{
 it('names a storage id, and continues only as its claimed wait',async()=>{
  const s=await scene(c=>{c.status='running'})
  const continuation=(wait:string)=>open({runStore:{store:s.store,id:PREDECESSOR,resume:true},questionContinuation:wait})
  await expect(continuation('wait-q')).rejects.toEqual(waitRefused('mismatch'))
  await expect(continuation('wait\nq')).rejects.toEqual(waitRefused('invalid'))
  // #355: claimed, it continues only through the claim's own view.
  await park(s)
  const resumed=await claim(s)
  await expect(continuation('wait-q')).rejects.toEqual(waitRefused('mismatch'))
  expect(answersOf(await open({runStore:{store:fencedRunStore(s.store,resumed.fence),id:PREDECESSOR,resume:true},questionContinuation:'wait-q'}))).toHaveLength(1)
 })
})

/** A store with no lease or wait port, as a host's single store kind may be: nothing leases its runs. */
const plain=(store:RunStore):RunStore=>({durability:store.durability,load:(...args)=>store.load(...args),commit:(...args)=>store.commit(...args),readEvents:(...args)=>store.readEvents(...args)})
/** Runs before (and can stand in for) the successor's create commit. */
function hooked(before:(run:string)=>Promise<'land'|'lose'|'refuse'|void>){
 return class extends DurableWaitStore {
  override async commitHistory(input:RunHistoryCommit,signal?:AbortSignal):Promise<RunHistoryReceipt>{return this.hook(input.run.runId,()=>super.commitHistory(input,signal))}
  override async commit(input:RunCommit,signal?:AbortSignal):Promise<RunReceipt>{return this.hook(input.runId,()=>super.commit(input,signal))}
  async hook<T>(run:string,commit:()=>Promise<T>):Promise<T>{
   if(!run.startsWith('successor-'))return commit()
   const outcome=await before(run)
   if(outcome==='refuse')throw Error('the process stopped')
   const receipt=await commit()
   if(outcome==='lose')throw Error('the process stopped')
   return receipt
  }
 }
}

describe('a successor made while something else happens (#340)',()=>{
 for(const landed of [false,true])it(`resumes after a crash while it is made: the create ${landed?'landed':'did not land'}`,async()=>{
  let crash=true
  const s=await scene(undefined,hooked(async()=>crash?landed?'lose':'refuse':undefined))
  await expect(asSuccessor(s)).rejects.toThrow('the process stopped')
  crash=false
  expect(!!await s.store.load(SUCCESSOR)).toBe(landed)
  if(!landed){await asSuccessor(s);expect(await s.store.load(SUCCESSOR)).toBeDefined();return}
  // Made: making it again is refused by name, and the host continues the one made, with the answer.
  await expect(asSuccessor(s)).rejects.toEqual(refused('succeeded'))
  const again=await asSuccessor(s,s.store,true)
  expect(answersOf(again)).toHaveLength(1);expect(again.context?.execution).toMatchObject({used:20,limit:30})
 })
 it('two successors of one question made at once are one run',async()=>{
  // Another process makes the successor after this one read that it was not there, before this one's create lands.
  // Under the predecessor's lease the other is refused held, and this one is made.
  let s:Scene|undefined,raced=false,inner:unknown
  s=await scene(undefined,hooked(async()=>{if(!raced){raced=true;inner=await asSuccessor(s!).then(()=>undefined,error=>error)}}))
  await asSuccessor(s)
  expect(inner).toMatchObject({name:'LeaseRefused',reason:'held'})
  // On a store with no leases the create is the one: the later is refused succeeded.
  raced=false
  s=await scene(undefined,hooked(async()=>{if(!raced){raced=true;await asSuccessor(s!,plain(s!.store))}}))
  await expect(asSuccessor(s,plain(s.store))).rejects.toEqual(refused('succeeded'))
  expect(await s.store.load(SUCCESSOR)).toBeDefined()
 })
 it('a successor and a question wait made at once both fail closed',async()=>{
  let s:Scene|undefined
  // A park that lands while the successor is made all the same (here under the successor's own lease, as a store that
  // does not serialize them would let it): after its create, the successor is refused, and the wait's continuation
  // because the successor exists.
  s=await scene(undefined,hooked(async()=>{await park(s!,'wait-q','question-1',s!.store,fenceOf((await s!.store.readLease(PREDECESSOR))!))}))
  await expect(asSuccessor(s)).rejects.toEqual(refused('parked'))
  // Both exist now, and neither continues: the successor is refused on every open, and the wait's continuation because
  // the successor exists.
  expect(await s.store.load(SUCCESSOR)).toBeDefined()
  await expect(asSuccessor(s,s.store,true)).rejects.toEqual(refused('parked'))
  const resumed=await claim(s)
  await expect(open({runStore:{store:fencedRunStore(s.store,resumed.fence),id:PREDECESSOR,resume:true},questionContinuation:'wait-q'})).rejects.toEqual(refused('succeeded'))
 })
})

describe('a successor run on an engine (#340)',()=>{
 const NOTE={title:'Note',question:'Which note should be written?',selection:'single',options:[{id:'a',label:'The first',description:'One.'},{id:'b',label:'The second',description:'Two.'}],recommendedOptionId:null,recommendation:null}
 const run=async(store:RunStore,runStore:Record<string,unknown>,ask:boolean)=>{
  const bodies:string[]=[]
  const call=vi.fn(async(_suffix:string,request:ModelRequest)=>{bodies.push(request.body);return answerWith(ask?{call:{tool:'ask_user',arguments:NOTE}}:{say:'The second note.'},'stream',bodies.length-1)})
  const session={prompt:'Write the note.',testPrompt:'',tools:[],callTool:vi.fn(async()=>({content:[]})),hostTools:[questionTool],signal:new AbortController().signal,allowConversation:true,critic:'off',interactive:true,
   context:{currentRequest:{message:'Write the note.',input:'note'},runStore:{store,...runStore}},model:{family:'openai-compatible',model:'fixture',call}} as unknown as EngineSession
  const events:AssistantEvent[]=[];for await(const event of vercel.start(session))events.push(event)
  return {events,bodies}
 }
 it('answers in one request under the carried grant, without a pause, and is made once',async()=>{
  const store=new MemoryRunStore()
  const first=await run(store,{id:PREDECESSOR},true)
  const asked=first.events.find(e=>e.type==='user_question') as Extract<AssistantEvent,{type:'user_question'}>
  const head=(await store.load(PREDECESSOR))!
  expect(asked).toBeDefined();expect(readExecution(JSON.parse(decode(head.checkpoint!)))?.status).toBe('complete')
  // The predecessor stands for one the person granted more requests: at the default allowance's end, raised by one
  // grant, its 20th request the one that asked.
  const granted={...JSON.parse(decode(head.checkpoint!)),used:20,limit:30,grants:[{from:20,to:30,at:'2026-01-01T00:00:00.000Z'}]}
  if(granted.digest!==undefined)granted.digest=recordDigest(granted)
  expect(readExecution(granted)).toBeDefined()
  const receipt=await store.commit({runId:PREDECESSOR,expectedRevision:head.revision,commitId:'granted',checkpoint:encode(granted),events:[]})
  const id=successorRunId(PREDECESSOR,asked.id),successor=(answer:QuestionDraft)=>({id,successor:{predecessor:PREDECESSOR,question:asked.id,answer}})
  // An option the question did not offer is no answer: refused by name, nothing asked, nothing written.
  const unoffered=await run(store,successor({selected:['c'],text:''}),false)
  expect(unoffered.events.filter(e=>e.type==='error')).toEqual([vector.items[1]]);expect(unoffered.events.at(-1)).toEqual(vector.items.at(-1))
  expect(unoffered.bodies).toEqual([]);expect(await store.load(id)).toBeUndefined()
  const answered=await run(store,successor({selected:['b'],text:''}),false)
  // The one request holds the answer once, and is not paused: the carried grant still holds.
  expect(answered.bodies).toHaveLength(1)
  const sent=JSON.stringify(JSON.parse(answered.bodies[0]!))
  expect(sent.split(JSON.stringify(QUESTION_ANSWERED).slice(1,-1)).length-1).toBe(1)
  expect(sent).toContain(JSON.stringify('{"answer":{"selected":["b"],"text":""},"question":{').slice(1,-1))
  expect(answered.events.filter(e=>e.type==='budget_paused'||e.type==='error')).toEqual([])
  const carried=readExecution(JSON.parse(decode((await store.load(id))!.checkpoint!)))!
  expect(carried).toMatchObject({id,status:'complete',used:21,limit:30,grants:[{from:20,to:30,at:'2026-01-01T00:00:00.000Z'}]})
  // Made once: a second is refused by name, and the predecessor never moved.
  const second=await run(store,successor({selected:['a'],text:''}),false)
  expect(second.events.filter(e=>e.type==='error')).toEqual([vector.items[3]]);expect(second.bodies).toEqual([])
  expect((await store.load(PREDECESSOR))!.revision).toBe(receipt.revision)
 })
})

/** Stands for a store that holds a claimed job wait of the predecessor at its head (#330) where job is the head's
 * revision, on the reads of the continuation's admission only (the wait is not a full record), or an open objective of
 * it (#330) where goal is set. */
class Admitting extends DurableWaitStore {
 job?:string;resumed?:{owner:string;epoch:string;at:string};goal?:unknown;admitting=false
 override async exportWaits(resource:string,signal?:AbortSignal){
  const held=await super.exportWaits(resource,signal)
  return this.admitting&&this.job!==undefined&&resource===PREDECESSOR?{...held,waits:[...held.waits,{resource,wait:'wait-job',reason:'job',state:'resumed',pending:{kind:'job',job:'job-1'},checkpoint:{revision:this.job},...(this.resumed?{resumed:this.resumed}:{})}]} as typeof held:held
 }
 override async readGoal(resource:string,signal?:AbortSignal){return this.goal!==undefined&&resource===PREDECESSOR?this.goal as Awaited<ReturnType<DurableWaitStore['readGoal']>>:super.readGoal(resource,signal)}
}
async function admitted<T>(store:Admitting,open:()=>Promise<T>):Promise<T>{store.admitting=true;try{return await open()}finally{store.admitting=false}}
/** A job wait of the predecessor claimed under worker-b's lease of the head, as resumeParked leaves it, and the claim's own
 * view: the store that bears the continuation out, and the store it is opened through. */
async function claimedJob(s:Scene){
 const store=s.store as Admitting,fence=fenceOf(await store.acquireLease({resource:PREDECESSOR,owner:'worker-b',expectedRevision:s.revision,durationMs:MINUTE}))
 store.job=s.revision;store.resumed={owner:fence.owner,epoch:fence.epoch,at:new Date(s.clock.at).toISOString()}
 return fencedRunStore(store,fence)
}
const jobContinuation=(store:Admitting,through:RunStore)=>admitted(store,()=>open({runStore:{store:through,id:PREDECESSOR,resume:true},jobContinuation:{job:'job-1'}}))
/** A complete head holding a step of its accepted Plan unfinished, as an objective's leg finds it. */
const objectiveCheckpoint=(c:Record<string,unknown>)=>{
 const {pending:_,...planState}=nativeCheckpoints.items[1]!.planState as Record<string,unknown>
 Object.assign(c,{version:3,plan:nativeCheckpoints.items[1]!.plan,planState})
}
const STAMP={command:'bind',actor:'person-reviewer',at:'2026-10-01T00:00:00.000Z'}
const OBJECTIVE_GOAL={version:1,kind:'goal',resource:PREDECESSOR,revision:1,journal:[],objectives:[{id:'objective-1',state:'active',created:STAMP,plan:{id:'accepted-plan',revision:'sha256:a8e181bf218df2f98469173636720266f4e0da8fe4f55d8f13d9eb80e5891233',steps:[{step:'Inspect',index:0}],bound:STAMP}}]}

describe('what a successor is made of, and what it is not (#340 review)',()=>{
 // MEDIUM-1: a `successor-` id is a successor's, made or opened only with its successor input.
 it('a successor\'s id is opened only as a successor',async()=>{
  const s=await scene()
  for(const resume of [false,true])await expect(open({runStore:{store:s.store,id:SUCCESSOR,...(resume?{resume:true}:{})}})).rejects.toEqual(refused('unbound'))
  expect(await s.store.load(SUCCESSOR)).toBeUndefined()
  // So the question is still the predecessor's to park.
  await park(s)
 })
 // LOW-2: the controller's own refusal of another request or phase, before anything is written.
 for(const [name,over] of [['another prompt',{prompt:'Write another note.'}],['a purpose',{purpose:'brief'}],['another phase',{phase:'synthesis'}]] as const)it(`is made only for the predecessor's request: ${name}`,async()=>{
  const s=await scene(),asked=successorOf(s),{phase,...session}=over as {phase?:string;prompt?:string;purpose?:string}
  const error=await durableSession({prompt:'',testPrompt:'',tools:[],callTool:vi.fn(),hostTools:[],signal:new AbortController().signal,...session,context:{runStore:{store:s.store,id:SUCCESSOR,successor:asked},...(phase?{phase}:{})}} as unknown as EngineSession).then(()=>undefined,error=>error)
  expect(error?.message).toBe('The request or execution phase changed. Start a new response with the current context.')
  expect(await s.store.load(SUCCESSOR)).toBeUndefined()
 })
 // HIGH-1 and #355: an admitted continuation of the predecessor and its successor do not both spend its allowance. The
 // continuation is a claimed job wait's, admitted only through its claim's own view, so each of its commits is fenced by
 // the claim's lease of the head.
 it('an admitted continuation and a successor do not both spend the allowance',async()=>{
  const s=await scene(undefined,Admitting),store=s.store as Admitting,view=await claimedJob(s)
  // Through the plain store, whose commits no lease stops, the claim admits nothing: the head is complete.
  await expect(jobContinuation(store,store)).rejects.toBeInstanceOf(RunComplete)
  await jobContinuation(store,view)
  // While the continuation holds the head's lease, no successor is made.
  const error=await asSuccessor(s).then(()=>undefined,error=>error)
  expect(error).toMatchObject({name:'LeaseRefused',reason:'held'});expect(error.message).toBe('LeaseRefused: held.')
  expect(await s.store.load(SUCCESSOR)).toBeUndefined()
  // Once its lease has lapsed, another lease of the head, the claim's owner's at a new epoch or another owner's, is not
  // the claim's, and admits nothing either.
  s.clock.at+=MINUTE
  for(const owner of ['worker-b','worker-c']){
   const other=fenceOf(await store.acquireLease({resource:PREDECESSOR,owner,expectedRevision:(await store.load(PREDECESSOR))!.revision,durationMs:MINUTE}))
   await expect(jobContinuation(store,fencedRunStore(store,other)),owner).rejects.toBeInstanceOf(RunComplete)
   await store.releaseLease(other)
  }
  // The successor is made at a new epoch: the continuation commits nothing more through its own store, and is not
  // opened again.
  await asSuccessor(s)
  const head=(await s.store.load(PREDECESSOR))!
  await expect(view.commit({runId:PREDECESSOR,expectedRevision:head.revision,commitId:'spent',checkpoint:head.checkpoint!,events:[]})).rejects.toMatchObject({name:'LeaseRefused',reason:'stale-epoch'})
  await expect(jobContinuation(store,view)).rejects.toEqual(refused('succeeded'))
 })
 // LOW-4: once the head's last question has its successor, the head resumes no other way, whichever admission would
 // have continued it: a claimed job wait's continuation, or a leg of an open objective (the claimed question wait's is
 // 'a successor and a question wait made at once both fail closed').
 for(const objective of [false,true])it(`a head whose question has its successor resumes no other way: ${objective?'a leg of an open objective':'a claimed job wait\'s continuation'}`,async()=>{
  const s=await scene(objective?objectiveCheckpoint:undefined,Admitting),store=s.store as Admitting
  let through:RunStore=store
  if(objective)store.goal=OBJECTIVE_GOAL;else through=await claimedJob(s)
  const continuation=()=>admitted(store,()=>open({runStore:{store:through,id:PREDECESSOR,resume:true},...(objective?{}:{jobContinuation:{job:'job-1'}})}))
  await continuation()
  s.clock.at+=MINUTE
  await asSuccessor(s)
  await expect(continuation()).rejects.toEqual(refused('succeeded'))
 })
 // HIGH-1: the predecessor is read again after the create; a commit that moved it meanwhile refuses the successor. Every
 // continuation of a complete head is fenced (#355), so only a commit made outside this package, as here, can.
 it('a successor whose predecessor moved while it was made is refused',async()=>{
  let s:Scene|undefined
  s=await scene(undefined,hooked(async()=>{const head=(await s!.store.load(PREDECESSOR))!;await s!.store.commit({runId:PREDECESSOR,expectedRevision:head.revision,commitId:'unfenced',checkpoint:head.checkpoint!,events:[]})}))
  await expect(asSuccessor(s)).rejects.toEqual(refused('moved'))
 })
 // MEDIUM-2: a predecessor past its deadline leases to no one, so it has no successor; nothing is written.
 it('is not made of a run past its deadline',async()=>{
  const s=await scene()
  await s.store.limitRun({version:1,resource:PREDECESSOR,run:new Date(s.clock.at+MINUTE).toISOString()})
  s.clock.at+=MINUTE
  const error=await asSuccessor(s).then(()=>undefined,error=>error)
  expect(error).toEqual(waitRefused('deadline-passed'));expect(error.message).toBe('WaitRefused: deadline-passed.')
  expect(await s.store.load(SUCCESSOR)).toBeUndefined()
 })
 // MEDIUM-2: a predecessor's records keep their retainUntil in the successor, and expired records have no successor.
 it('keeps the predecessor\'s retention, and expired records have none',async()=>{
  const record=(until:string)=>(c:Record<string,unknown>)=>{const written=writeRecord(CHECKPOINT_SCHEMA,{...c},RECORD_DEFAULTS,undefined,new Date(),until);for(const key of Object.keys(c))delete c[key];Object.assign(c,written)}
  const until=new Date(Date.now()+48*HOUR).toISOString()
  let s=await scene(record(until))
  const made=await asSuccessor(s),saved=readExecution(JSON.parse(decode((await s.store.load(SUCCESSOR))!.checkpoint!)))
  expect(saved).toMatchObject({schema:CHECKPOINT_SCHEMA,retainUntil:until});expect(made.context?.execution).toMatchObject({retainUntil:until})
  s=await scene(record('2020-01-01T00:00:00.000Z'))
  await expect(asSuccessor(s)).rejects.toBeInstanceOf(RecordsExpired)
  expect(await s.store.load(SUCCESSOR)).toBeUndefined()
 })
 // LOW-1: a park reads the successor again after its commit.
 it('a question parked while its successor is made is refused',async()=>{
  const s=await scene()
  class Raced extends DurableWaitStore{override async parkWait(...args:Parameters<DurableWaitStore['parkWait']>){await s.store.commit({runId:SUCCESSOR,expectedRevision:null,commitId:'made',checkpoint:encode({}),events:[]});return s.store.parkWait(...args)}}
  const raced=Object.assign(new Raced({now:()=>new Date(s.clock.at)}),{})
  // The raced view is the scene's store: its reads and commits are the scene's.
  for(const name of ['load','readEvents','readWait','exportWaits','commit','commitFenced','acquireLease','releaseLease','readLease','checkFence'] as const)(raced as unknown as Record<string,unknown>)[name]=(s.store as unknown as Record<string,(...args:unknown[])=>unknown>)[name]!.bind(s.store)
  await expect(park(s,'wait-q','question-1',raced)).rejects.toEqual(refused('succeeded'))
  // The wait stands, and its continuation is refused because the successor exists.
  expect(await s.store.readWait(PREDECESSOR,'wait-q')).toBeDefined()
 })
 // LOW-3: the answers a run is handed are only those its open read from the store.
 it('hands on no answers a host set',async()=>{
  const planted=[{question:QUESTION,answer:{selected:['risk'],text:''}}]
  for(const context of [{},{runStore:{store:new MemoryRunStore(),id:'run-planted'}}]){
   const opened=await open({...context,[questionAnswers]:planted})
   expect(answersOf(opened)??[]).toEqual([])
  }
 })
 // LOW-4: the members a successor carries are read at their versions; a kept approval text is not carried.
 it('is written at the version of what it carries, and its first turn counts its own test calls',async()=>{
  expect([1,2,3,4,5,6].map(version=>successorVersion(version as never))).toEqual([2,2,3,4,3,4])
  const s=await scene(c=>{c.version=3;c.testCalls=2})
  const made=await makeSuccessor(s.store,SUCCESSOR,successorOf(s),{prompt:''})
  expect(made.checkpoint.version).toBe(3);expect(made.checkpoint).not.toHaveProperty('testCalls')
 })
})
