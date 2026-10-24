import {afterEach,describe,it,expect,vi} from 'vitest'
import {vercel} from '../engines/vercel'
import {deepagents} from '../engines/deepagents'
import {codex} from '../engines/codex'
import {agentRunFrom,answerFrom,runOnAgentEngine,runOnModelEngine,type Turn,type Ran} from '../testing/scenarioDriver'
import {runtimeAnswer} from '../testing/scripted'
import {PROPOSE_PACK_SCHEMA} from '../engines/contract'
import type {Engine} from '../engine'
import type {PolicyLayer} from '../policy'
import eventSchema from '../../../contract/schemas/run-event.json'
import {argumentProblem} from '../sourceArguments'
import type {ExecutionCheckpoint} from '../execution/state'
afterEach(()=>vi.unstubAllGlobals())
const key='AKIAABCDEFGHIJKLMNOP',hole='[redacted: aws-access-key]'
// Saved operation results, including retained copies, are exempt tool-result payloads.
const outputJSON=(value:unknown)=>JSON.stringify(value,(field,value)=>field==='result'?undefined:value)
const saved=(r:Ran)=>r.events.filter(e=>e.type==='execution_checkpoint').at(-1)?.checkpoint
async function play(engine:Engine,turns:Turn[],policy:PolicyLayer={},execution?:ExecutionCheckpoint,interrupt=false,declared=false){
 const written:ExecutionCheckpoint[]=[],allEvents:import('../engine').AssistantEvent[]=[]
 const options={engine,stopAt:(event:import('../engine').AssistantEvent)=>{allEvents.push(event);return interrupt&&event.type==='execution_checkpoint'&&Number(event.checkpoint.outputGuard?.['aws-access-key'])>0},session:{prompt:'Inspect and report.',interactive:true,critic:'off' as const,policy,context:{checkpoint:async(checkpoint:ExecutionCheckpoint)=>{written.push(structuredClone(checkpoint))},currentRequest:{message:'Inspect and report.',input:'Inspect and report.'},...(execution?{execution}:{})}},hostTools:[...(declared?[{name:key,description:'Declared host tool.',inputSchema:{type:'object'},execute:async()=>runtimeAnswer({status:'ok'})}]:[]),{name:'ask_user',description:'Ask.',interaction:'question' as const,inputSchema:{type:'object'},execute:async()=>runtimeAnswer({})},{name:'propose_pack',description:'Propose.',inputSchema:PROPOSE_PACK_SCHEMA,effect:'proposal' as const,execute:async()=>runtimeAnswer({})}],handle:(call:{name:string;args:Record<string,unknown>})=>runtimeAnswer(call.name==='validate'?{status:'valid'}:{key})}
 const answers:{text:string;isError:boolean}[]=[]
 const ran=engine.id==='codex'?await runOnAgentEngine(agentRunFrom(turns,answers),options):await runOnModelEngine(answerFrom(turns,'whole'),options)
 return {...ran,rawEvents:allEvents,answers,written}
}
for(const engine of [vercel,deepagents,codex])describe(`${engine.id==='codex'?'agent ':''}engine ${engine.id} · scenarios`,()=>{
 it('17 · output boundary: undeclared names and gate notices are guarded before delivery and save',async()=>{
  for(const mode of ['redact','refuse'] as const){
   const run=await play(engine,[{call:{tool:key,arguments:{}}},{say:'Finished.'}],mode==='refuse'?{outputGuard:mode}:{})
   expect(run.events.filter(e=>e.type==='error')).toEqual([])
   expect(run.seen.some(c=>c.name===key)).toBe(false)
   const notices=run.events.filter(e=>e.type==='guardrail')
   expect(notices.some(e=>JSON.stringify(e).includes(key))).toBe(false)
   if(mode==='redact'){
    expect(notices).toContainEqual(expect.objectContaining({tool:hole,detail:expect.stringContaining(hole),action:'refused'}))
    expect(saved(run)?.outputGuard?.['aws-access-key']).toBe(3)
   }else{
    expect(notices).toContainEqual(expect.objectContaining({reason:'output refused',action:'refused'}))
    expect(saved(run)?.outputGuard?.['aws-access-key']).toBe(2)
   }
   expect(run.events).toContainEqual({ version: 1,type:'output_guarded',where:'message',pattern:'aws-access-key',action:mode})
   const noticeAt=run.events.length-1-[...run.events].reverse().findIndex(e=>e.type==='guardrail')
   expect(noticeAt).toBeGreaterThan(run.events.findIndex(e=>e.type==='tool_call'))
   expect(noticeAt).toBeLessThan(run.events.findIndex(e=>e.type==='tool_result'))
   // Result payloads remain exempt, including the gate's model-readable refusal.
   for(const event of run.events){
    if(event.type==='tool_result'){expect(event.name).not.toContain(key);continue}
    if(event.type==='execution_checkpoint'){
     for(const operation of event.checkpoint.operations)expect(JSON.stringify({...operation,result:undefined})).not.toContain(key)
     expect(outputJSON(event.checkpoint)).not.toContain(key)
    }else expect(JSON.stringify(event)).not.toContain(key)
   }
   expect(saved(run)?.operations[0]).toMatchObject({status:'failed'})
   expect(run.written.length).toBeGreaterThan(0)
   for(const checkpoint of run.written)expect(outputJSON(checkpoint)).not.toContain(key)
   expect(run.events.find(e=>e.type==='tool_call')).toMatchObject({name:mode==='redact'?hole:'Output refused: aws-access-key.'})
   const declaredRun=await play(engine,[{call:{tool:key,arguments:{}}},{say:'Finished.'}],mode==='refuse'?{outputGuard:mode}:{},undefined,false,true)
   expect(declaredRun.events.find(e=>e.type==='tool_call')).toMatchObject({name:key})
   expect(declaredRun.events.filter(e=>e.type==='output_guarded')).toEqual([])
   expect(saved(declaredRun)?.operations[0]).toMatchObject({name:key,status:'complete'})
   // The gate's permission refusal remains model-readable, with its original tool name.
   expect(JSON.stringify(run.requests.map(r=>r.body))+JSON.stringify(run.answers)).toContain(key)
   for(const event of run.events)if(event.type==='guardrail'||event.type==='output_guarded')expect(argumentProblem(eventSchema.$defs[event.type],event)).toBeUndefined()
  }
 })
 it('17 · forged labels in model answers are scanned',async()=>{
  const run=await play(engine,[{say:'[redacted: sk-abcdefghijklmnopqrst]'}])
  expect(run.events.find(e=>e.type==='message')).toMatchObject({text:'[redacted: [redacted: provider-key]]'})
  expect(saved(run)?.outputGuard?.['provider-key']).toBe(1)
 })
 it('17 · output boundary: answer, proposal, arguments, memory, draft, refusal, allow and resume',async()=>{
  const noticeRun=await play(engine,[{call:{tool:'propose_edit',arguments:{document:key,old_text:'before',new_text:'after'}}},{say:'Finished.'}])
  expect(noticeRun.events.filter(e=>e.type==='error')).toEqual([])
  expect(noticeRun.events.find(e=>e.type==='edit_refused')).toMatchObject({document:hole})
  expect(noticeRun.events.filter(e=>e.type==='edit_refused'||e.type==='guardrail'||e.type==='edit_applied').some(e=>JSON.stringify(e).includes(key))).toBe(false)
  expect(noticeRun.events).toContainEqual({ version: 1,type:'output_guarded',where:'draft edit',pattern:'aws-access-key',action:'redact'})
  expect(saved(noticeRun)?.outputGuard?.['aws-access-key']).toBe(1)
  const run=await play(engine,[{call:{tool:'get_schema',arguments:{}}},{call:{tool:'validate',arguments:{content:JSON.stringify({title:key})}}},{call:{tool:'remember',arguments:{kind:'fact',text:key}}},{call:{tool:'propose_pack',arguments:{document:{title:key}}}},{say:'Held '+key}])
  expect(run.events.filter(e=>e.type==='error')).toEqual([])
  expect(run.events.filter(e=>e.type==='message_progress'||e.type==='reasoning')).toEqual([])
  expect(run.events.find(e=>e.type==='message')).toMatchObject({text:'Held '+hole})
  expect(run.events.find(e=>e.type==='proposal')).toMatchObject({document:{title:hole}})
  expect(run.events.find(e=>e.type==='memory_proposed')).toMatchObject({memory:{text:hole}})
  expect(run.seen.filter(c=>c.name==='validate').every(c=>!JSON.stringify(c.args).includes(key))).toBe(true)
  expect(saved(run)?.outputGuard?.['aws-access-key']).toBe(4)
  expect(JSON.stringify(run.requests.map(r=>r.body))+JSON.stringify(run.answers)).toContain(key)
  const interrupted=await play(engine,[{call:{tool:'remember',arguments:{kind:'fact',text:key}}},{say:'Not reached.'}],{},undefined,true)
  expect(saved(interrupted)?.outputGuard?.['aws-access-key']).toBe(1)
  const restarted=await play(engine,[{say:key}],{},saved(interrupted))
  expect(saved(restarted)?.outputGuard?.['aws-access-key']).toBe(2)
  const continued=await play(engine,[{say:key}],{},saved(run))
  expect(saved(continued)?.outputGuard?.['aws-access-key']).toBe(5)
  const draft=await play(engine,[{call:{tool:'propose_pack',arguments:{document:{title:'before'}}}},{call:{tool:'propose_edit',arguments:{document:'document',old_text:'before',new_text:key}}},{say:'Edited.'}])
  expect(draft.events.find(e=>e.type==='proposal')).toMatchObject({document:{title:hole}})
  expect(saved(draft)?.outputGuard?.['aws-access-key']).toBe(1)
  const refused=await play(engine,[{say:key}],{outputGuard:'refuse'})
  expect(refused.events.some(e=>e.type==='message')).toBe(false)
  expect(refused.events).toContainEqual(expect.objectContaining({type:'guardrail',reason:'output refused'}))
  expect(saved(refused)?.outputGuard?.['aws-access-key']).toBe(1)
  expect(refused.rawEvents.filter(e=>e.type==='usage')).toHaveLength(1)
  expect(refused.rawEvents.at(-2)).toMatchObject({type:'usage',usage:saved(refused)?.usage})
  expect(refused.rawEvents.at(-1)).toEqual({version:1,type:'end'})
  const refusedCall=await play(engine,[{call:{tool:'validate',arguments:{content:key}}},{say:'Not sent.'}],{outputGuard:'refuse'})
  expect(refusedCall.seen).toHaveLength(0)
  expect(saved(refusedCall)?.outputGuard?.['aws-access-key']).toBe(1)
  const asked=await play(engine,[{call:{tool:'ask_user',arguments:{title:'Question',question:key,selection:'single',options:[],recommendedOptionId:null,recommendation:null}}}])
  expect(asked.events.find(e=>e.type==='user_question')).toMatchObject({question:{question:hole}})
  const allowed=await play(engine,[{say:key}],{outputAllow:['aws-access-key']})
  expect(allowed.events.find(e=>e.type==='message')).toMatchObject({text:key})
  expect(saved(allowed)?.outputGuard??{}).toEqual({})
 })
})
