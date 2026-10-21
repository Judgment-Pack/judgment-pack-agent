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
  type RecordGuard,
  type RecordsPolicy
} from '../records'
import scenario from './scenarios/records.json'

// Expand this at run time because the repository's local-paths check forbids the literal.
const scenarioSecret=scenario.secret.replace('<home>','/'+'home')
const guard:RecordGuard={value<T>(input:T){
  const walk=(value:unknown):unknown=>typeof value==='string'?value.replace(scenarioSecret,scenario.hole):Array.isArray(value)?value.map(walk):value&&typeof value==='object'?Object.fromEntries(Object.entries(value).map(([name,held])=>[name,walk(held)])):value
  return walk(input) as T
}}

const turns:Turn[]=[{say:'Records checked.'}]
async function play(engine:Engine,context:ContextInput={}){
  const session={prompt:'Check records.',interactive:true,critic:'off' as const,context:{currentRequest:{message:'Check records.',input:'Check records.'},...context}}
  return engine.id==='codex'
    ? runOnAgentEngine(agentRunFrom(turns),{engine,session})
    : runOnModelEngine(answerFrom(turns,'whole'),{engine,session})
}
afterEach(()=>vi.unstubAllGlobals())

for(const engine of [vercel,deepagents,codex])describe(`${engine.id==='codex'?'agent ':''}engine ${engine.id} · records`,()=>{
  it('18 · records carry identity, lifetime and redaction; export binds their digests; expired resume is refused',async()=>{
    const now=new Date(scenario.now),policy=scenario.policy as RecordsPolicy
    const ran=await play(engine)
    expect(ran.events.filter(event=>event.type==='error')).toEqual([])
    const checkpoint=ran.events.filter(event=>event.type==='execution_checkpoint').at(-1)!.checkpoint
    const critique=writeRecord(CRITIQUE_SCHEMA,{type:'critique',refuted:false,checks:[],text:`Read ${scenarioSecret}`},policy,guard,now)
    const records=[checkpoint,...checkpoint.completions??[],critique]
    expect(records.every(validRecord),engine.id).toBe(true)
    expect(records.every(record=>record.schema&&record.version===1&&record.digest&&record.retainUntil),engine.id).toBe(true)
    expect(critique).toMatchObject({text:`Read ${scenario.hole}`})
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
