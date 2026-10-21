import {afterEach,describe,it,expect,vi} from 'vitest'
import {vercel} from '../engines/vercel'
import {deepagents} from '../engines/deepagents'
import {codex} from '../engines/codex'
import {agentRunFrom,answerFrom,runOnAgentEngine,runOnModelEngine,type Turn,type Ran} from '../testing/scenarioDriver'
import {runtimeAnswer} from '../testing/scripted'
import {PROPOSE_PACK_SCHEMA} from '../engines/contract'
import type {Engine} from '../engine'
import type {PolicyLayer} from '../policy'
import type {ExecutionCheckpoint} from '../execution/state'
afterEach(()=>vi.unstubAllGlobals())
const key='AKIAABCDEFGHIJKLMNOP',hole='[redacted: aws-access-key]'
const saved=(r:Ran)=>r.events.filter(e=>e.type==='execution_checkpoint').at(-1)?.checkpoint
async function play(engine:Engine,turns:Turn[],policy:PolicyLayer={},execution?:ExecutionCheckpoint,interrupt=false){
 const options={engine,...(interrupt?{stopAt:(event:import('../engine').AssistantEvent)=>event.type==='execution_checkpoint'&&Number(event.checkpoint.outputGuard?.['aws-access-key'])>0}:{}),session:{prompt:'Inspect and report.',interactive:true,critic:'off' as const,policy,context:{currentRequest:{message:'Inspect and report.',input:'Inspect and report.'},...(execution?{execution}:{})}},hostTools:[{name:'ask_user',description:'Ask.',interaction:'question' as const,inputSchema:{type:'object'},execute:async()=>runtimeAnswer({})},{name:'propose_pack',description:'Propose.',inputSchema:PROPOSE_PACK_SCHEMA,effect:'proposal' as const,execute:async()=>runtimeAnswer({})}],handle:(call:{name:string;args:Record<string,unknown>})=>runtimeAnswer(call.name==='validate'?{status:'valid'}:{key})}
 const answers:{text:string;isError:boolean}[]=[]
 const ran=engine.id==='codex'?await runOnAgentEngine(agentRunFrom(turns,answers),options):await runOnModelEngine(answerFrom(turns,'whole'),options)
 return {...ran,answers}
}
for(const engine of [vercel,deepagents,codex])describe(`${engine.id==='codex'?'agent ':''}engine ${engine.id} · scenarios`,()=>{
 it('17 · output boundary: answer, proposal, arguments, memory, draft, refusal, allow and resume',async()=>{
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
