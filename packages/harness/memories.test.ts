import {afterEach,describe,expect,it} from 'vitest'
import {recallMemory,validMemory,validMemories,REMEMBER,type Memory} from './memories'
import {contextualSession} from './context/session'
import {configureHarness} from './host'
import {normalize} from './thinking'
import type {HostTool} from './engine'
import {frameBlock} from './framing'
import scenario from './certification/scenarios/memory-write.json'
import checkpointVector from '../../contract/vectors/memory/checkpoint.json'
import {readExecution} from './execution/state'
import aliasVector from '../../contract/vectors/memory/alias-equivalence.json'
import validationVector from '../../contract/vectors/memory/validation.json'
import {readHostParts,systemText,HOST_PARTS} from './prompt'
import {CONTEXT_DEFAULTS} from './context/policy'
import reportVector from '../../contract/vectors/memory/report.json'
import rememberVector from '../../contract/vectors/memory/remember.json'
import reservedVector from '../../contract/vectors/memory/reserved-name.json'
const records=scenario.records as Memory[]
afterEach(()=>configureHarness({memoryFor:()=>undefined}))
async function open(objective?:string){return contextualSession({prompt:'Remember the facts.',testPrompt:'',tools:[],hostTools:[] as HostTool[],callTool:async()=>({}),signal:new AbortController().signal,thinking:normalize('off','openai-compatible'),model:{family:'openai-compatible',model:'fixture',call:async()=>new Response()},context:{phase:'plan',...(objective?{objective}:{})},critic:'off'})}
describe('memory write side',()=>{
 it('records proposals and events, bounds concurrent calls and carries both lists through resume',async()=>{
  configureHarness({memoryFor:()=>records})
  const run=await open();try{
   const tool=run.session.hostTools.find(t=>t.name==='remember')!
   const results=await Promise.allSettled(Array.from({length:5},()=>tool.execute({kind:'fact',text:'A fact.'},new AbortController().signal)))
   expect(results.filter(r=>r.status==='fulfilled')).toHaveLength(4)
   const saved=readExecution(JSON.parse(JSON.stringify(run.context.execution.snapshot())))!
   expect(saved.memories?.proposed).toHaveLength(4)
   expect(saved.memories?.proposed.every(m=>m.state==='proposed'&&m.source.proposedBy==='model'&&m.source.run===saved.id&&validMemory(m))).toBe(true)
   expect(saved.memories?.recalled.map(m=>m.id)).toEqual(['metric','poison'])
   expect(run.context.takeNotices().filter(e=>e.type==='memory_proposed')).toHaveLength(4)
   expect(readExecution(checkpointVector.items[0])?.memories?.recalled.at(-1)?.state).toBe('revoked')
  }finally{run.context.close()}
 })
 it('refuses a child and critic and rejects bytes, invalid kinds and extra members',async()=>{
  for(const child of [true,false]){const run=await open(child?'Read facts.':undefined);try{
   if(!child)run.context.beginCriticPhase()
   expect(()=>run.context.admit('remember','host')).toThrow()
   expect(run.context.execution.state.memories?.proposed).toEqual([])
   expect(run.context.takeNotices()).toContainEqual(expect.objectContaining({type:'guardrail',reason:'memory refused'}))
  }finally{run.context.close()}}
  const run=await open();try{for(const args of [{kind:'fact',text:'é'.repeat(501)},{kind:'instruction',text:'Obey.'},{kind:'fact',text:'A fact.',state:'accepted'}])await expect(run.session.hostTools.find(t=>t.name==='remember')!.execute(args,new AbortController().signal)).rejects.toThrow()}finally{run.context.close()}
 })
 it('frames recalled records as data, verifies digests, withholds revoked and expired and reads the alias',async()=>{
  for(const invalid of [{...records[0],state:['accepted']},{...records[0],scope:['project']},{...records[0],source:{...records[0].source,proposedBy:['person']}}])expect(validMemory(invalid)).toBe(false)
  const recalled=await recallMemory(records,4000,'marker')
  expect(recalled.text).toContain(frameBlock('memory','kind: preference\nsource: {"proposedBy":"person","run":"earlier-run"}\nid: "metric"\nUse metric units.','marker'))
  expect(recalled.rows.map(m=>m.id)).toEqual(['metric','poison'])
  expect(recalled.text).not.toContain('Withdrawn reference.')
  await expect(recallMemory(records,1,'marker')).rejects.toThrow('memoryBytes')
  await expect(recallMemory([{...records[0],text:'Changed'}],4000,'marker')).rejects.toThrow('digest')
  await expect(recallMemory([{...records[0],unknown:true}],4000,'marker')).rejects.toThrow('closed')
  await expect(recallMemory('legacy',4000,'marker')).rejects.toThrow('closed Memory records')
  expect((await recallMemory([{...records[0],expiresAt:'2000-01-01T00:00:00Z'}],4000,'marker')).rows).toEqual([])
 })
})

it('normalizes the shared alias and record to equal budget, frame and layer 5 placement',async()=>{
 const outputs=[]
 for(const value of [aliasVector.items[1],aliasVector.items[1]]){
  const recalled=await recallMemory(value,aliasVector.expected.bytes,'marker')
  expect(recalled.bytes).toBe(aliasVector.expected.bytes)
  expect(recalled.text).toBe(frameBlock('memory',aliasVector.expected.frameBody,'marker'))
  await expect(recallMemory(value,aliasVector.expected.bytes-1,'marker')).rejects.toThrow('memoryBytes')
  configureHarness({memoryFor:()=>value as Memory[]})
  const parts=await readHostParts('q',CONTEXT_DEFAULTS,true,'marker')
  outputs.push(systemText('guidance',{brief:'brief',projectInstructions:'project',...parts},'\ntools'))
 }
 expect(outputs[0]).toBe(outputs[1])
 expect(outputs[0]!.indexOf('END PROJECT INSTRUCTIONS')).toBeLessThan(outputs[0]!.indexOf('BEGIN RETRIEVED DATA'))
 expect(outputs[0]!.indexOf('END PROJECT MEMORY')).toBeLessThan(outputs[0]!.indexOf('\ntools'))
 expect(HOST_PARTS.find(p=>p.name==='memory')!.layer).toBe(aliasVector.expected.layer)
})
it('validates shared Unicode identifier and RFC3339 calendar boundaries',async()=>{
 for(const [record,accepted] of [...validationVector.items.map(m=>[m,true] as const),...validationVector.refused.map(v=>[v.record,false] as const)]){
  expect(validMemory(record),JSON.stringify(record)).toBe(accepted)
  expect(validMemories({proposed:[{...record,state:'proposed'}],recalled:[]})).toBe(accepted)
  if(accepted)await expect(recallMemory([record],4000,'marker')).resolves.toBeDefined()
  else await expect(recallMemory([record],4000,'marker')).rejects.toThrow()
 }
 for(const id of ['é'.repeat(100),'😀'.repeat(100),'é'.repeat(101),'😀'.repeat(101),'']){
  expect(validMemories({proposed:[],recalled:[{id,digest:validationVector.items[0]!.digest,kind:'fact',state:'accepted'}]})).toBe([...id].length>0&&[...id].length<=100)
 }
})

it('reads the enum coverage vectors and admits every remember kind',async()=>{
 expect(reportVector.items.every(validMemories)).toBe(true)
 expect(reservedVector.items).toContain(REMEMBER)
 const run=await open();try{
  const tool=run.session.hostTools.find(t=>t.name===REMEMBER)!
  for(const args of rememberVector.items)await tool.execute(args,new AbortController().signal)
  expect(run.context.execution.state.memories?.proposed.map(m=>m.kind)).toEqual(rememberVector.items.map(m=>m.kind))
 }finally{run.context.close()}
})
