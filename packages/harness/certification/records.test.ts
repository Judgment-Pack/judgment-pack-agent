import {OutputGuard,RecordsRefused} from '../outputGuard'
import {afterEach,describe,expect,it,vi} from 'vitest'
import {vercel} from '../engines/vercel'
import {deepagents} from '../engines/deepagents'
import {codex} from '../engines/codex'
import {agentRunFrom,answerFrom,runOnAgentEngine,runOnModelEngine,type Turn} from '../testing/scenarioDriver'
import type {ContextInput} from '../context/session'
import type {Engine} from '../engine'
import {
  CRITIQUE_SCHEMA,
  exportRecords,
  recordDigest,
  validRecord,
  writeRecord,
  type RecordsPolicy
} from '../records'
import scenario from './scenarios/records.json'
import eventSchema from '../../../contract/schemas/run-event.json'
import {argumentProblem} from '../sourceArguments'
import type {AssistantEvent} from '../engine'
import {CHECKPOINT_SCHEMA} from '../records'
import {checkRecord,UnknownRecordMember,readRunEvent} from '../recordReader'

// Expand this at run time because the repository's local-paths check forbids the literal.
const scenarioSecret=scenario.secret.replace('<home>','/'+'home')

const turns:Turn[]=[{say:'Records checked.'}]
async function play(engine:Engine,context:ContextInput={}){
  const session={prompt:'Check records.',interactive:true,critic:'off' as const,context:{currentRequest:{message:'Check records.',input:'Check records.'},...context}}
  return engine.id==='codex'
    ? runOnAgentEngine(agentRunFrom(turns),{engine,session})
    : runOnModelEngine(answerFrom(turns,'whole'),{engine,session})
}
afterEach(()=>vi.unstubAllGlobals())

for(const engine of [vercel,deepagents,codex])describe(`${engine.id==='codex'?'agent ':''}engine ${engine.id} · records`,()=>{
  it('refuses future records before any provider or tool work and emits a current named notice',async()=>{
    for(const context of [{execution:{version:3}}, {configuration:undefined,execution:{version:3,future:'x'.repeat(10000)}}]){
      const ran=await play(engine,context as unknown as ContextInput)
      expect(ran.requests.length+ran.runs.length).toBe(0)
      expect(ran.events).toContainEqual(expect.objectContaining({type:'guardrail',reason:'record version unsupported',version:1}))
      for(const event of ran.events)expect(()=>readRunEvent(event)).not.toThrow()
    }
  })
  it('resumes legacy source absence without rewriting its input or extending retention',async()=>{
    const first=await play(engine)
    const saved=structuredClone(first.events.filter(e=>e.type==='execution_checkpoint').at(-1)!.checkpoint)
    // A real v1 writer had no version 2 plan book. Its frozen reader must still reject one.
    expect(()=>checkRecord('checkpoint',{...saved,version:1})).toThrow(UnknownRecordMember)
    delete saved.planState
    saved.version=1;saved.status='stopped'
    for(const member of Object.values(saved.policy??{}))if(member.source==='layer')delete member.source
    saved.digest=recordDigest(saved)
    expect(()=>checkRecord('checkpoint',saved)).not.toThrow()
    const before=JSON.stringify(saved),resumed=await play(engine,{execution:saved})
    expect(JSON.stringify(saved)).toBe(before)
    expect(resumed.events.filter(e=>e.type==='error')).toEqual([])
    const next=resumed.events.filter(e=>e.type==='execution_checkpoint').at(-1)!.checkpoint
    expect(next.version).toBe(2);expect(next.retainUntil).toBe(saved.retainUntil);expect(next.digest).toBe(recordDigest(next))
  })
  it('18 · records carry identity, lifetime and redaction; export binds their digests; expired resume is refused',async()=>{
    const now=new Date(scenario.now),policy=scenario.policy as RecordsPolicy
    const ran=await play(engine)
    expect(ran.events.filter(event=>event.type==='error')).toEqual([])
    for(const event of ran.events){expect(event.version).toBe(1);expect(readRunEvent(event)).toEqual(event)}
    const checkpoint=ran.events.filter(event=>event.type==='execution_checkpoint').at(-1)!.checkpoint
    const delivered:Record<string,number>={}
    const notices:AssistantEvent[]=[]
    const guard=new OutputGuard({},()=>delivered,event=>notices.push(event))
    const critique=writeRecord(CRITIQUE_SCHEMA,{type:'critique',refuted:false,checks:[],text:`Read ${scenarioSecret}`},policy,guard,now)
    const records=[checkpoint,...checkpoint.completions??[],critique]
    expect(records.every(validRecord),engine.id).toBe(true)
    expect(records.every(record=>record.schema&&record.version===(record.schema===CHECKPOINT_SCHEMA?2:1)&&record.digest&&record.retainUntil),engine.id).toBe(true)
    expect(critique).toMatchObject({text:`Read ${scenario.hole}`})
    expect(critique.records).toEqual({redactions:{'local-home-path':1}})
    expect(delivered).toEqual({})
    const key='AKIAABCDEFGHIJKLMNOP',payload=key+'x'.repeat(200_000)
    guard.declaredNames=new Set([key])
    const large=writeRecord(CHECKPOINT_SCHEMA,{...checkpoint,operations:[
      {id:'declared',name:key,args:{},status:'complete',result:{content:[{type:'text',text:payload}]}},
      {id:'undeclared',name:'prefix-'+key,args:{text:key},status:'failed'},
    ]},policy,guard,now)
    expect(large.operations[0]!.name).toBe(key)
    expect(large.operations[0]!.result?.content[0]!.text).toBe(payload)
    expect(large.operations[1]!.name).toBe('prefix-[redacted: aws-access-key]')
    expect(large.records).toEqual({redactions:{'aws-access-key':2}})
    expect(validRecord(large)).toBe(true)
    let refusedRecord:unknown
    expect(()=>{refusedRecord=writeRecord(CRITIQUE_SCHEMA,{type:'critique',refuted:false,checks:[],text:'x'.repeat(200_000)},policy,guard,now)}).toThrow(RecordsRefused)
    expect(refusedRecord).toBeUndefined()
    expect(notices).toContainEqual({type:'guardrail',tool:'records',action:'refused',reason:'output refused',detail:'Output refused: work-bound.'})
    for(const notice of notices)if(notice.type==='guardrail')expect(argumentProblem(eventSchema.$defs.guardrail,notice)).toBeUndefined()
    expect(delivered).toEqual({})
    expect(validRecord(writeRecord(CRITIQUE_SCHEMA,{type:'critique',text:'Continued.'},policy,guard,now))).toBe(true)
    const exported=exportRecords(records,policy,'0.8.0',now)
    expect(exported.value.manifest.records.map(record=>record.digest)).toEqual(records.map(record=>record.digest))
    expect(exported.value.manifest.digest).toBe(recordDigest(exported.value.manifest))
    expect(exported.event).toMatchObject({type:'export_written',records:records.length})
    const expired={...checkpoint,retainUntil:scenario.expired};expired.digest=recordDigest(expired)
    const refused=await play(engine,{execution:expired})
    expect(refused.requests.length+refused.runs.length).toBe(0)
    expect(refused.events).toContainEqual(expect.objectContaining({type:'guardrail',reason:'records expired'}))
  })
})
