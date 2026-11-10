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
import {recordDigest} from '../records'
import {readExecution} from '../execution/state'
import {MemoryRunStore,type RunCommit,type RunReceipt,type RunStore} from './run'
import type {RunHistoryCommit,RunHistoryReceipt} from './history'
import {fenceOf,type LeaseFence,type LeaseLifetime} from './lease'
import {parkRun,resumeParked,wakeKeyDigest,wakeRun,type WaitLifetime} from './wait'
import {RunComplete,durableSession,questionAnswers} from './writer'
import {QUESTION_ANSWERED,SuccessorRefused,questionAnswerText,successorRunId,type QuestionAnswer,type RunSuccessor} from './successor'
import vector from '../../../contract/vectors/checkpoint/successor.json'
import members from '../../../contract/vectors/checkpoint/members.json'

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
async function park(s:Scene){
 return parkRun({store:s.store,fence:s.fence,wait:'wait-q',continuation:'continue-q',condition:{event:{kind:'question-answer',subject:'question-1'}},timeoutAt:new Date(T0+2*HOUR).toISOString(),wakeKeyDigest:wakeKeyDigest(KEY),release:[],question:'question-1',model:{provider:'openai',model:'fixture'}})
}
async function claim(s:Scene){
 s.clock.at+=MINUTE
 await wakeRun(s.store,{version:1,resource:PREDECESSOR,wait:'wait-q',evidence:'answer-1',kind:'event',event:{kind:'question-answer',subject:'question-1'},answer:s.answer,actor:'person-reviewer',key:KEY})
 const held=(await s.store.readWait(PREDECESSOR,'wait-q'))!,{model,...rest}=held.pins
 return resumeParked({store:s.store,resource:PREDECESSOR,wait:'wait-q',evidence:'answer-1',owner:'worker-b',durationMs:HOUR,current:{version:1,resource:PREDECESSOR,wait:'wait-q',...rest,model:{...model,status:'available'},policy:{digest:held.pins.policy}}})
}

describe('the shared successor vector (#340)',()=>{
 it('the derived id, each refusal as the host hears it, and the answer block',()=>{
  expect(vector.derivations).toHaveLength(4)
  for(const row of vector.derivations)expect(successorRunId(row.predecessor,row.question),`${row.predecessor}/${row.question}`).toBe(row.id)
  // A storage id holds no control character, so the NUL-separated derivation is one id per pair.
  expect(new Set(vector.derivations.map(row=>row.id)).size).toBe(4)
  expect(vector.reasons).toEqual(['invalid','unbound','succeeded','not-complete','parked'])
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
 const cases:[string,string,((c:Record<string,unknown>)=>void)|undefined,(s:Scene)=>Promise<unknown>][]=[
  ['an empty predecessor','invalid',undefined,s=>open({runStore:{store:s.store,id:SUCCESSOR,successor:successorOf(s,{predecessor:''})}})],
  ['no successor record','invalid',undefined,s=>open({runStore:{store:s.store,id:SUCCESSOR,successor:'question-1'}})],
  ['an execution beside it','invalid',undefined,s=>asSuccessor(s,s.store,false,{},{execution:readExecution(vector.made.checkpoint)})],
  ['a question continuation beside it','invalid',undefined,s=>asSuccessor(s,s.store,false,{},{questionContinuation:'wait-q'})],
  ['a usage budget beside it','invalid',undefined,s=>asSuccessor(s,s.store,false,{},{usageBudget:{}})],
  ['a chat context beside it','invalid',undefined,s=>asSuccessor(s,s.store,false,{},{},{context:{}})],
  ['an id not derived','unbound',undefined,s=>open({runStore:{store:s.store,id:'successor-chosen',successor:successorOf(s)}})],
  ['an earlier question','unbound',undefined,s=>asSuccessor(s,s.store,false,{question:'question-0'})],
  ['a blank answer','unbound',undefined,s=>{s.answer={selected:[],text:'  '};return asSuccessor(s)}],
  ['an option not offered','unbound',undefined,s=>{s.answer={selected:['elsewhere'],text:''};return asSuccessor(s)}],
  ['a predecessor not there','not-complete',undefined,s=>asSuccessor(s,s.store,false,{predecessor:'run-none'})],
  ['a predecessor still running','not-complete',c=>{c.status='running'},s=>asSuccessor(s)],
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
  // Parked, not yet claimed: a complete head is not resumed.
  await expect(continuation('wait-q')).rejects.toBeInstanceOf(RunComplete)
  const resumed=await claim(s)
  expect(answersOf(await continuation('wait-q'))).toEqual([{question:QUESTION,answer:{selected:['risk'],text:''}}])
  // Another wait, or none: refused as before. A continuation needs the store's head.
  await expect(continuation('wait-other')).rejects.toBeInstanceOf(RunComplete)
  await expect(open({runStore:{store:s.store,id:PREDECESSOR,resume:true}})).rejects.toBeInstanceOf(RunComplete)
  await expect(open({questionContinuation:'wait-q'})).rejects.toEqual(waitRefused('invalid'))
  // The question was answered through its wait: no successor of it.
  await expect(asSuccessor(s)).rejects.toEqual(refused('parked'))
  // Once the head moves, the continuation delivered again is refused.
  const head=(await s.store.load(PREDECESSOR))!
  await s.store.commitFenced({runId:PREDECESSOR,expectedRevision:head.revision,commitId:'moved',checkpoint:head.checkpoint!,events:[]},resumed.fence)
  await expect(continuation('wait-q')).rejects.toBeInstanceOf(RunComplete)
 })
})

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
 it('a successor and a question wait made at once both fail closed',async()=>{
  let s:Scene|undefined
  // The question is parked while the successor is being made: after the successor's read of the waits, before its
  // create lands.
  s=await scene(undefined,hooked(async()=>{await park(s!)}))
  await expect(asSuccessor(s)).rejects.toEqual(refused('parked'))
  // Both exist now, and neither continues: the successor is refused on every open, and the wait's continuation because
  // the successor exists.
  expect(await s.store.load(SUCCESSOR)).toBeDefined()
  await expect(asSuccessor(s,s.store,true)).rejects.toEqual(refused('parked'))
  await claim(s)
  await expect(open({runStore:{store:s.store,id:PREDECESSOR,resume:true},questionContinuation:'wait-q'})).rejects.toEqual(refused('succeeded'))
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
  expect(second.events.filter(e=>e.type==='error')).toEqual([vector.items[2]]);expect(second.bodies).toEqual([])
  expect((await store.load(PREDECESSOR))!.revision).toBe(receipt.revision)
 })
})
