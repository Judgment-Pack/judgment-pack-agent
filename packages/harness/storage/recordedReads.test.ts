/**
 * #352: a read a durable run serves without a dispatch of its own, a Runtime read through callTool or an entry of the
 * chat's read cache, is given the form its writer records a call's result in (#343). The entry may have been kept by a
 * run that records nothing, or brought back by a stored chat from before this change: it is given that form when it is
 * served, not when it was kept. The Go twins are go/context/recorded_reads_test.go and
 * go/adapter/durableapprovaltests/recorded_reads_test.go.
 */
import {describe,expect,it} from 'vitest'
import {MemoryRunStore} from './run'
import {ChatContext} from '../context/chat'
import {codexTurn,ofType,runtimeAnswer,sseTurn,streamed,vercelTurn} from '../testing/scripted'
import type {AgentRun} from '../agent'
import {ContextLedger} from '../context/ledger'
import {CONTEXT_DEFAULTS} from '../context/policy'
import {OutputGuard} from '../outputGuard'
import {recordedForm,sha256,type RecordGuard,type RecordsPolicy} from '../records'

const RAW='https://packs.example/tmp/evaluation-pack',REDACTED='https://packs.example[redacted: local-temp-path]',RAW_PATH='packs.example/tmp/'
const pack=(id:string)=>({packId:id,status:'evaluated'})
const packText=(id:string)=>JSON.stringify(pack(id))
const RUNTIME={identity:'jpack',version:'0.39.0'}
const storedCheckpoint=async(store:MemoryRunStore)=>new TextDecoder().decode((await store.load('read'))?.checkpoint??undefined)

// A chat's earlier runs are shown to a later one by each kept result's first line: a cached answer whose first line holds
// the match makes a durable run's first request unrecordable, and it stops there (ContinuationNotRecoverable). The cache
// is reached where the match is further in, as here.
const lines=(id:string)=>({content:[{type:'text' as const,text:'Evaluated.\n'+packText(id)}],structuredContent:pack(id)})
/** One API turn that reads get_schema once, on chat where given, durable under store where given; calls counts the Runtime's answers. */
async function read(calls:string[],options:{chat?:ChatContext;store?:MemoryRunStore;policy?:Record<string,unknown>;answer?:typeof lines}={}){
 return vercelTurn({
  handle:call=>{calls.push(call.name);return options.answer?options.answer(RAW):runtimeAnswer(pack(RAW))},
  answer:request=>streamed(request.number===1?sseTurn({tool:{name:'get_schema',args:{}}}):sseTurn({text:'Done.'})),
  session:{...(options.policy?{policy:options.policy}:{}),context:{currentRequest:{message:'Read the schema.',input:'Read the schema.'},runtime:RUNTIME,...(options.chat?{chat:options.chat}:{}),...(options.store?{runStore:{store:options.store,id:'read'}}:{})}} as never})
}
const schemaResults=(events:{type:string}[])=>ofType(events as never,'tool_result').filter(e=>e.name==='get_schema').map(e=>e.text)

describe('a Runtime read in a durable run (#352)',()=>{
 it('api: is shown and given to the model in the recorded form, and the chat keeps that form',async()=>{
  const calls:string[]=[],chat=new ChatContext(),store=new MemoryRunStore(),out=await read(calls,{chat,store})
  expect(ofType(out.events,'error')).toEqual([])
  expect(calls).toEqual(['get_schema'])
  expect(schemaResults(out.events)).toEqual([packText(REDACTED)])
  const given=out.requests.map(request=>JSON.stringify(request.body))
  expect(given[1]).toContain('[redacted: local-temp-path]')
  for(const text of given)expect(text).not.toContain(RAW_PATH)
  expect(new TextDecoder().decode(chat.snapshot())).not.toContain(RAW_PATH)
  expect(await storedCheckpoint(store)).not.toContain(RAW_PATH)
 })
 it('native: the agent is given the recorded form',async()=>{
  const given:string[]=[],store=new MemoryRunStore()
  const run:AgentRun=async(request,callbacks)=>{
   try{
    const tool=request.tools.find(t=>t.description.startsWith('get_schema:'))!
    given.push((await callbacks.tool({id:'read-wire',name:tool.name,arguments:{}},callbacks.signal)).text)
    await callbacks.event({type:'message',id:'reply',text:'Done.',phase:'final'})
   }finally{callbacks.providerRelease?.acknowledge(callbacks.providerRelease.invocation)}
  }
  const out=await codexTurn({script:async()=>{},handle:()=>runtimeAnswer(pack(RAW)),session:{agent:{model:'fixture',run},context:{currentRequest:{message:'Read the schema.',input:'Read the schema.'},runStore:{store,id:'read'}}} as never})
  expect(ofType(out.events,'error')).toEqual([])
  expect(given).toHaveLength(1)
  expect(given[0]).not.toContain(RAW_PATH)
  expect(given[0]).toContain(JSON.stringify({structuredContent:pack(REDACTED)}))
  expect(schemaResults(out.events)).toEqual([packText(REDACTED)])
 })
 it('outside a durable run is answered as the Runtime answered, as before',async()=>{
  const calls:string[]=[],out=await read(calls)
  expect(schemaResults(out.events)).toEqual([packText(RAW)])
 })
})

describe('a chat’s cached read in a durable run (#352)',()=>{
 for(const from of ['the same chat','a stored chat'])it(`${from}: an entry the guard would change is read again, not served or stored, and the recorded answer is kept in its place`,async()=>{
  const calls:string[]=[],answer=lines
  let chat=new ChatContext()
  // A run that records nothing keeps the Runtime's answer in the chat's read cache as it was given.
  expect(schemaResults((await read(calls,{chat,answer})).events)).toEqual([answer(RAW).content[0]!.text])
  const saved=chat.snapshot()
  expect(new TextDecoder().decode(saved)).toContain(RAW_PATH)
  if(from==='a stored chat')chat=ChatContext.restore(saved)
  const store=new MemoryRunStore(),out=await read(calls,{chat,store,answer})
  expect(ofType(out.events,'error')).toEqual([])
  expect(calls).toEqual(['get_schema','get_schema'])
  expect(schemaResults(out.events)).toEqual([answer(REDACTED).content[0]!.text])
  for(const request of out.requests)expect(JSON.stringify(request.body)).not.toContain(RAW_PATH)
  // The kept entry was not made the durable run's: neither its stored checkpoint nor its events hold it.
  expect(await storedCheckpoint(store)).not.toContain(RAW_PATH)
  expect(JSON.stringify(out.events)).not.toContain(RAW_PATH)
  // A later run is served the recorded answer without a call.
  expect(schemaResults((await read(calls,{chat,answer})).events)).toEqual([answer(REDACTED).content[0]!.text])
  expect(calls).toHaveLength(2)
 })
 it('an entry the guard leaves as it is is served from the cache, as before',async()=>{
  const calls:string[]=[],chat=new ChatContext(),kept='https://packs.example/var/evaluation-pack',answer=()=>lines(kept)
  await read(calls,{chat,answer})
  const out=await read(calls,{chat,store:new MemoryRunStore(),answer})
  expect(calls).toEqual(['get_schema'])
  expect(schemaResults(out.events)).toEqual([lines(kept).content[0]!.text])
 })
 it('an entry a refusing guard withholds is not served, and nothing is run',async()=>{
  const calls:string[]=[],chat=new ChatContext(),answer=lines,store=new MemoryRunStore()
  await read(calls,{chat,answer})
  const out=await read(calls,{chat,store,policy:{outputGuard:'refuse'},answer})
  expect(calls).toEqual(['get_schema'])
  expect(out.requests.length).toBeGreaterThan(0)
  expect(JSON.stringify(out.events)).not.toContain(RAW_PATH)
  for(const request of out.requests)expect(JSON.stringify(request.body)).not.toContain(RAW_PATH)
  expect(await storedCheckpoint(store)).not.toContain(RAW_PATH)
  // The entry is left as it was kept.
  expect(schemaResults((await read(calls,{chat,answer})).events)).toEqual([answer(RAW).content[0]!.text])
  expect(calls).toEqual(['get_schema'])
 })
})

// #352: a chat entry a durable run reads by id, as the chat's index tells it to, is read and carried by its checkpoint in the
// form its writer records; the chat keeps its own. The entry was kept by a run that records nothing, its match past its
// first line. Before, the model was given the recorded form but the stored checkpoint carried the entry as it was kept.
describe('a chat entry a durable run reads by id (#352)',()=>{
 const keptID=(chat:ChatContext)=>new TextDecoder().decode(chat.snapshot()).match(/[^"\\]*result-get_schema-[0-9a-f]{64}[^"\\]*/)?.[0]
 const byID=(calls:string[],tool:string,id:string,options:{chat:ChatContext;store:MemoryRunStore;policy?:Record<string,unknown>})=>vercelTurn({
  handle:call=>{calls.push(call.name);return lines(RAW)},
  answer:request=>streamed(request.number===1?sseTurn({tool:{name:tool,args:{id}}}):sseTurn({text:'Done.'})),
  session:{...(options.policy?{policy:options.policy}:{}),context:{currentRequest:{message:'Read the schema.',input:'Read the schema.'},runtime:RUNTIME,chat:options.chat,runStore:{store:options.store,id:'read'}}} as never})
 // The recorded form of the entry, spelled as the run read it: the checkpoint keeps that spelling (valueJSON) beside the value.
 const RECORDED=JSON.stringify(lines(REDACTED))
 const carried=async(store:MemoryRunStore,id:string)=>(JSON.parse(await storedCheckpoint(store)).context.entries as {id:string;value:unknown;valueJSON?:string;bytes:number;digest:string}[]).find(entry=>entry.id===id)
 for(const tool of ['read_window','read_context'])it(`api, ${tool}: the stored checkpoint carries the recorded form, and the chat keeps its own`,async()=>{
  const calls:string[]=[],chat=new ChatContext()
  await read(calls,{chat,answer:lines})
  const id=keptID(chat)!
  expect(new TextDecoder().decode(chat.snapshot())).toContain(RAW_PATH)
  const store=new MemoryRunStore(),out=await byID(calls,tool,id,{chat,store})
  expect(ofType(out.events,'error')).toEqual([])
  for(const request of out.requests)expect(JSON.stringify(request.body)).not.toContain(RAW_PATH)
  expect(JSON.stringify(out.requests[1]!.body)).toContain('[redacted: local-temp-path]')
  expect(await storedCheckpoint(store)).not.toContain(RAW_PATH)
  const entry=await carried(store,id),want=RECORDED
  expect(entry?.valueJSON).toBe(want)
  expect(entry?.value).toEqual(lines(REDACTED))
  expect([entry?.bytes,entry?.digest]).toEqual([new TextEncoder().encode(want).length,sha256(want)])
  expect(new TextDecoder().decode(chat.snapshot())).toContain(RAW_PATH)
 })
 it('native, read_window: the agent and the stored checkpoint get the recorded form',async()=>{
  const calls:string[]=[],chat=new ChatContext()
  await read(calls,{chat,answer:lines})
  const id=keptID(chat)!,given:string[]=[],store=new MemoryRunStore()
  const run:AgentRun=async(request,callbacks)=>{
   try{
    const tool=request.tools.find(t=>t.description.startsWith('read_window:'))!
    given.push((await callbacks.tool({id:'read-by-id',name:tool.name,arguments:{id}},callbacks.signal)).text)
    await callbacks.event({type:'message',id:'reply',text:'Done.',phase:'final'})
   }finally{callbacks.providerRelease?.acknowledge(callbacks.providerRelease.invocation)}
  }
  const out=await codexTurn({script:async()=>{},handle:()=>lines(RAW),session:{agent:{model:'fixture',run},context:{currentRequest:{message:'Read the schema.',input:'Read the schema.'},runtime:RUNTIME,chat,runStore:{store,id:'read'}}} as never})
  expect(ofType(out.events,'error')).toEqual([])
  expect(given).toHaveLength(1)
  expect(given[0]).not.toContain(RAW_PATH)
  expect(given[0]).toContain('[redacted: local-temp-path]')
  expect(await storedCheckpoint(store)).not.toContain(RAW_PATH)
  expect((await carried(store,id))?.valueJSON).toBe(RECORDED)
 })
 it('a refusing guard: the read is refused, and nothing is referenced or stored',async()=>{
  const calls:string[]=[],chat=new ChatContext()
  await read(calls,{chat,answer:lines})
  const id=keptID(chat)!,store=new MemoryRunStore(),out=await byID(calls,'read_window',id,{chat,store,policy:{outputGuard:'refuse'}})
  expect(ofType(out.events,'tool_result').filter(e=>e.name==='read_window').map(e=>e.text).join('')).not.toContain(RAW_PATH)
  for(const request of out.requests)expect(JSON.stringify(request.body)).not.toContain(RAW_PATH)
  expect(await storedCheckpoint(store)).not.toContain(RAW_PATH)
  expect(await carried(store,id)).toBeUndefined()
 })
 it('a restored checkpoint: an entry the chat still holds as it was kept is carried in the recorded form',async()=>{
  const calls:string[]=[],chat=new ChatContext()
  await read(calls,{chat,answer:lines})
  const id=keptID(chat)!,want=lines(REDACTED),text=JSON.stringify(want),turn=chat.openTurn(),ledger=new ContextLedger({...CONTEXT_DEFAULTS},turn)
  await ledger.restore({scope:ledger.scope,entries:[{id,kind:'tool',label:'get_schema',value:want,bytes:new TextEncoder().encode(text).length,digest:sha256(text)}]})
  const guard=new OutputGuard({},()=>({}),()=>{}) as unknown as RecordGuard
  ledger.recordWith(value=>recordedForm(value,{redactOnStore:true} as RecordsPolicy,guard))
  const held=ledger.snapshot().entries
  expect(held.map(entry=>[entry.id,JSON.stringify(entry.value),entry.digest])).toEqual([[id,text,sha256(text)]])
  // And the run reads that form, as its checkpoint carries it, not the chat's.
  const shown=JSON.stringify(ledger.readItem({id}))
  expect(shown).not.toContain(RAW_PATH)
  expect(shown).toContain('[redacted: local-temp-path]')
  turn.release()
 })
 it('a restored checkpoint: an entry the record guard refuses is neither carried nor read',async()=>{
  const calls:string[]=[],chat=new ChatContext()
  await read(calls,{chat,answer:lines})
  const id=keptID(chat)!,want=lines(REDACTED),text=JSON.stringify(want),turn=chat.openTurn(),ledger=new ContextLedger({...CONTEXT_DEFAULTS},turn)
  await ledger.restore({scope:ledger.scope,entries:[{id,kind:'tool',label:'get_schema',value:want,bytes:new TextEncoder().encode(text).length,digest:sha256(text)}]})
  const guard=new OutputGuard({outputGuard:{value:'refuse',layer:'host'}} as never,()=>({}),()=>{}) as unknown as RecordGuard
  expect(()=>ledger.recordWith(value=>recordedForm(value,{redactOnStore:true} as RecordsPolicy,guard))).toThrow(/local-temp-path/)
  expect(ledger.snapshot().entries).toEqual([])
  expect(()=>ledger.readItem({id})).toThrow(/local-temp-path/)
  turn.release()
 })
})

// A durable run's search of every item matches each chat entry in the form the run reads it by id, so what it finds, and
// where, agrees with a search of that one item and with read_window, and the search makes no entry the run's. A run that
// records nothing searches the chat's own entries. A guard refusal of an entry is the search's error. The Go twin is
// TestADurableRunsSearchOfEveryItemMatchesTheRecordedForm.
describe('a durable run’s search of every item (#352)',()=>{
 const keptID=(chat:ChatContext)=>new TextDecoder().decode(chat.snapshot()).match(/[^"\\]*result-get_schema-[0-9a-f]{64}[^"\\]*/)?.[0]
 const ledgerOn=(chat:ChatContext,mode?:'redact'|'refuse')=>{
  const turn=chat.openTurn(),ledger=new ContextLedger({...CONTEXT_DEFAULTS},turn)
  if(mode){
   const guard=new OutputGuard((mode==='refuse'?{outputGuard:{value:'refuse',layer:'host'}}:{}) as never,()=>({}),()=>{}) as unknown as RecordGuard
   ledger.recordWith(value=>recordedForm(value,{redactOnStore:true} as RecordsPolicy,guard))
  }
  return {turn,ledger}
 }
 it('a durable run: what it finds is the recorded form’s, where a search of the one item finds it, and no entry is made the run’s',async()=>{
  const calls:string[]=[],chat=new ChatContext()
  await read(calls,{chat,answer:lines})
  const id=keptID(chat)!,{turn,ledger}=ledgerOn(chat,'redact')
  // The raw text's path is not there to find.
  expect(ledger.search({pattern:'tmp/evaluation'}).matches).toEqual([])
  const every=ledger.search({pattern:'local-temp-path'}).matches
  expect(ledger.snapshot().entries).toEqual([])
  expect(every.length).toBeGreaterThan(0)
  expect(JSON.stringify(every)).not.toContain(RAW_PATH)
  // The chat also keeps the call's operation, found too; the kept result's matches are the one item's.
  expect(every.filter(match=>match.id===id)).toEqual(ledger.search({pattern:'local-temp-path',id}).matches)
  turn.release()
 })
 it('a run that records nothing searches the chat’s own entries',async()=>{
  const calls:string[]=[],chat=new ChatContext()
  await read(calls,{chat,answer:lines})
  const {turn,ledger}=ledgerOn(chat)
  expect(ledger.search({pattern:'tmp/evaluation'}).matches.length).toBeGreaterThan(0)
  turn.release()
 })
 it('a refusing guard: the search is refused, and no entry is made the run’s',async()=>{
  const calls:string[]=[],chat=new ChatContext()
  await read(calls,{chat,answer:lines})
  const {turn,ledger}=ledgerOn(chat,'refuse')
  expect(()=>ledger.search({pattern:'evaluated'})).toThrow(/local-temp-path/)
  expect(ledger.snapshot().entries).toEqual([])
  turn.release()
 })
})
