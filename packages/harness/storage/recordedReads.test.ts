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
