import {describe,it,expect} from 'vitest'
import type {AssistantSession} from '../engine'
import type {ModelMessage} from 'ai'
import {normalize} from '../thinking'
import {CONTEXT_DEFAULTS,contextPolicy,inputBytes,utf8Bytes,ContextBudgetError} from './policy'
import {ContextLedger} from './ledger'
import {contextualSession,SessionContext} from './session'

function session():AssistantSession{return {prompt:'Current request',testPrompt:'',tools:[],hostTools:[],callTool:async()=>({content:[]}),model:{family:'gemini',model:'small',call:async()=>{throw Error('No model call in context unit tests')}},thinking:normalize('off','gemini'),signal:new AbortController().signal,context:{configuration:{version:1,models:{small:{contextWindowTokens:16384,outputTokens:4096}}}}}}

describe('shared scoped context',()=>{
 it('retains immutable typed data and supports exact JSON pointer reads',async()=>{
  const ledger=new ContextLedger({...CONTEXT_DEFAULTS})
  const original={missing:undefined,nothing:null,zero:0,flag:false,decimal:'50','a/b':{'~key':'exact'}}
  const e=await ledger.retain({id:'facts',kind:'artifact',revision:'r4',label:'Current facts',value:original})
  original.decimal='999'
  expect(ledger.read({id:e.id,path:'/decimal'})).toMatchObject({text:'50',revision:'r4'})
  expect(ledger.read({id:e.id,path:'/flag'})).toMatchObject({value:false})
  expect(ledger.read({id:e.id,path:'/nothing'})).toMatchObject({value:null})
  expect(ledger.read({id:e.id,path:'/zero'})).toMatchObject({value:0})
  expect(ledger.read({id:e.id,path:'/a~1b/~0key'})).toMatchObject({text:'exact'})
  expect(()=>ledger.read({id:e.id,path:'/missing'})).toThrow(/does not exist/)
  expect(()=>ledger.read({id:e.id,path:'/__proto__'})).toThrow(/does not exist/)
  expect(()=>ledger.read({id:e.id,sha256:'stale'})).toThrow(/revision changed/)
  expect(()=>new ContextLedger({...CONTEXT_DEFAULTS}).read({id:e.id})).toThrow(/unavailable/)
  ledger.clear();expect(()=>ledger.read({id:e.id})).toThrow(/unavailable/)
 })
 it('pages unicode and heavily escaped text without losing bytes or producing oversized tool output',async()=>{
  const context=new SessionContext(session()),text=('😀é\u0001"\\\n').repeat(5000)
  const e=await context.ledger.retain({id:'source',kind:'tool',label:'Page',value:text})
  let offset:number|null=0,result=''
  while(offset!==null){
   const read=await context.ledger.tools()[1]!.execute({id:e.id,offset},new AbortController().signal)
   const projected=await context.result('read_context',read)
   expect(utf8Bytes(JSON.stringify(projected))).toBeLessThanOrEqual(CONTEXT_DEFAULTS.toolResultBytes)
   const page=projected.structuredContent as {text:string;nextOffset:number|null}
   result+=page.text;offset=page.nextOffset
  }
  expect(result).toBe(text);expect(context.ledger.list().items).toHaveLength(1)
 })
 it('externalizes large results while preserving originals and operation identity',async()=>{
  const c=new SessionContext(session()),original={isError:true,structuredContent:{records:Array.from({length:800},(_,i)=>({id:i,flag:false,text:'record '.repeat(40)}))}}
  const projected=await c.result('save_records',original)
  expect(projected.isError).toBe(true)
  const ref=projected.structuredContent as {contextId:string}
  expect(ref).toMatchObject({operationReturned:true,readWith:'read_context'})
  expect(c.ledger.read({id:ref.contextId,path:'/structuredContent/records/799'})).toMatchObject({value:original.structuredContent.records[799]})
  expect(original.structuredContent.records).toHaveLength(800)
 })
 it('budgets escaped current requests and retains exact omitted material',async()=>{
  const c=new SessionContext(session()),request='RULE: keep every decision\n'+('😀\u0000"'.repeat(45000))+'\nLATEST USER REQUEST: map the dialect'
  const projected=await c.prompt(request,'Host instructions',[])
  expect(projected).toContain('LATEST USER REQUEST: map the dialect')
  expect(projected).toContain('RULE: keep every decision')
  const messages=await c.messages([{role:'user',content:projected}],'Host instructions',[])
  expect(utf8Bytes(JSON.stringify(messages))).toBeLessThan(inputBytes(c.policy))
  const ref=c.ledger.list().items[0]!
  expect(ref.bytes).toBe(utf8Bytes(JSON.stringify(request)))
  expect(c.ledger.read({id:ref.contextId,offset:0,limit:128})).toMatchObject({text:expect.stringContaining('RULE:')})
 })
 it('compacts completed tool pairs while keeping latest exchanges and pending tool calls together',async()=>{
  const c=new SessionContext(session())
  const messages:ModelMessage[]=[{role:'user',content:'Current task'}]
  for(let i=0;i<6;i++)messages.push({role:'assistant',content:[{type:'tool-call',toolCallId:'c'+i,toolName:'read',input:{id:i}}]},{role:'tool',content:[{type:'tool-result',toolCallId:'c'+i,toolName:'read',output:{type:'text',value:'x'.repeat(9000)}}]})
  messages.push({role:'assistant',content:[{type:'tool-call',toolCallId:'pending',toolName:'read',input:{}}]})
  const before=JSON.stringify(messages),projected=await c.messages(messages,'host',[])
  expect(JSON.stringify(messages)).toBe(before)
  expect(projected.at(-1)).toEqual(messages.at(-1))
  expect(JSON.stringify(projected)).toContain('Completed tool calls must not be repeated')
  expect(c.ledger.list().items).toHaveLength(1)
  expect(utf8Bytes(JSON.stringify(projected))).toBeLessThan(inputBytes(c.policy))
 })
 it('does not compact an unresolved approval or silently discard oversized instructions',async()=>{
  const c=new SessionContext(session())
  const messages:ModelMessage[]=[{role:'user',content:'task'},{role:'assistant',content:[{type:'tool-approval-request',approvalId:'approval',toolCallId:'action'}]},{role:'user',content:'x'.repeat(60000)},{role:'assistant',content:'still pending'}]
  await expect(c.messages(messages,'host',[])).rejects.toBeInstanceOf(ContextBudgetError)
  expect(c.ledger.list().items).toHaveLength(0)
  await expect(c.prompt('request','x'.repeat(100000),[])).rejects.toBeInstanceOf(ContextBudgetError)
 })
 it('reserves context tool names for every feature',async()=>{
  const s=session();s.hostTools=[{name:'read_context',description:'bad override',inputSchema:{},execute:async()=>({content:[]})}]
  await expect(contextualSession(s)).rejects.toThrow(/reserved/)
  expect(contextPolicy(s.context!.configuration,'different').contextWindowTokens).toBe(CONTEXT_DEFAULTS.contextWindowTokens)
 })
})

it('leaves room for follow-up calls after compacting a very long initial request',async()=>{
 const c=new SessionContext(session()),prompt=await c.prompt('x'.repeat(250000)+'\nCurrent request','host',[])
 const history:ModelMessage[]=[{role:'user',content:prompt},{role:'assistant',content:[{type:'tool-call',toolCallId:'read',toolName:'read_context',input:{}}]},{role:'tool',content:[{type:'tool-result',toolCallId:'read',toolName:'read_context',output:{type:'text',value:'y'.repeat(8000)}}]}]
 await expect(c.messages(history,'host',[])).resolves.toEqual(history)
})
it('does not turn a completed operation into a retryable tool failure when retention is full',async()=>{
 const c=new SessionContext(session());c.policy.retainedBytes=100
 const result=await c.result('propose_change',{content:[{type:'text',text:'x'.repeat(13000)}]})
 expect(result.isError).toBe(false)
 expect(result.structuredContent).toMatchObject({operationReturned:true,contextUnavailable:true,isError:false})
})


it('keeps the latest question and complete current pack/test input together instead of a historical tail',async()=>{
 const c=new SessionContext(session())
 const currentRequest={message:'Why is no-documented-routing a mismatch?',input:'Current pack: exceptions first.\n'+JSON.stringify({cases:[{id:'no-routing',facts:{routing:'not-documented'},expected:'set-aside',actual:'unresolved'}]})}
 const old='Host task constraints\n'+('Earlier snapshot '.repeat(20000))+'\n'+currentRequest.input
 const projected=await c.prompt(old,'host',[],currentRequest)
 expect(projected).toContain('Host task constraints')
 expect(projected).toContain(JSON.stringify(currentRequest))
 expect(projected.length).toBeLessThan(old.length)
 const refs=c.ledger.list().items
 expect(refs).toHaveLength(2)
 expect(c.ledger.read({id:refs[1]!.contextId,path:'/message'})).toMatchObject({text:currentRequest.message})
 expect(c.ledger.read({id:refs[1]!.contextId,path:'/input'})).toMatchObject({text:currentRequest.input})
 expect(utf8Bytes(JSON.stringify([{role:'user',content:projected}]))).toBeLessThan(inputBytes(c.policy))
})
it('pins the question even when a huge current input follows it, and locates its exact omitted context',async()=>{
 const c=new SessionContext(session()),message='Why is the omitted-size case unresolved?'
 const input=message+'\n'+('irrelevant '.repeat(20000))+'EXACT CASE: missing vs false\n'+('more '.repeat(20000))
 const projected=await c.prompt('history '.repeat(30000)+input,'host',[],{message,input})
 expect(projected).toContain(message)
 const ref=c.ledger.list().items.find(r=>r.label.startsWith('Latest'))!
 const result=c.ledger.read({id:ref.contextId,path:'/input',query:'EXACT CASE',limit:1000})
 expect(result).toMatchObject({type:'search',found:true,matchOffset:input.indexOf('EXACT CASE'),text:expect.stringContaining('EXACT CASE: missing vs false')})
 expect(utf8Bytes(JSON.stringify([{role:'user',content:projected}]))).toBeLessThan(inputBytes(c.policy))
})
it('searches retained strings literally and pages matches without corrupting Unicode or recursively retaining reads',async()=>{
 const c=new SessionContext(session()),text='😀'.repeat(1000)+'a.*b '+('😀'.repeat(1000))+'a.*b'
 const e=await c.ledger.retain({id:'search',kind:'artifact',label:'Original',value:{text}})
 let offset=0
 for(const expected of [text.indexOf('a.*b'),text.lastIndexOf('a.*b')]){
  const result=await c.ledger.tools()[1]!.execute({id:e.id,path:'/text',query:'a.*b',offset},new AbortController().signal)
  const projected=await c.result('read_context',result)
  expect(utf8Bytes(JSON.stringify(projected))).toBeLessThanOrEqual(c.policy.toolResultBytes)
  const found=projected.structuredContent as {matchOffset:number;text:string;nextOffset:number|null}
  expect(found.matchOffset).toBe(expected);expect(found.text).toContain('a.*b');expect(found.text).not.toContain('�')
  offset=found.nextOffset??text.length
 }
 expect(c.ledger.read({id:e.id,path:'/text',query:'a.*b',offset})).toMatchObject({found:false,nextOffset:null})
 expect(c.ledger.read({id:e.id,query:'a.*b'})).toMatchObject({found:true,matches:[{path:'/text',matchOffset:text.indexOf('a.*b')}]})
 expect(()=>c.ledger.read({id:e.id,path:'/text',query:''})).toThrow(/non-empty/)
 expect(c.ledger.list().items).toHaveLength(1)
})

it('keeps continuation constraints and completed work alongside the focused question',async()=>{
 const c=new SessionContext(session()),currentRequest={message:'Explain the failed case',input:'Current pack and cases'}
 const tail='Continue read-only. Completed action: save_dialect. Do not repeat it. Latest choice: provided sources only.'
 const projected=await c.prompt('Host constraints\n'+'old '.repeat(50000)+currentRequest.input+'\n'+tail,'host',[],currentRequest)
 expect(projected).toContain(JSON.stringify(currentRequest));expect(projected).toContain(tail)
})

it('exposes schema text paths and searches a tool envelope without guessing its structure',async()=>{
 const c=new SessionContext(session())
 const schema=JSON.stringify({$defs:{condition:{operator:'less-than-or-equal',description:'x'.repeat(15000)}}})
 const result=await c.result('get_schema',{content:[{type:'text',text:schema}],structuredContent:{status:'ok',schemaId:'fixture'}})
 const ref=result.structuredContent as {contextId:string;sections:unknown[]}
 expect(ref.sections).toContainEqual({path:'/content/0/text',type:'string'})
 expect(c.ledger.read({id:ref.contextId,query:'operator'})).toMatchObject({found:true,matches:[{path:'/content/0/text',text:expect.stringContaining('operator')}]})
 expect(()=>c.ledger.read({id:ref.contextId,path:'/$defs'})).toThrow(/root.*content\/0\/text/)
 expect(c.ledger.read({id:ref.contextId,path:'/content/0/text',query:'operator'})).toMatchObject({found:true})
})
it('compacts even the latest completed signed exchange while preserving exact typed data',async()=>{
 const c=new SessionContext(session()),signature='s'.repeat(50000)
 const messages:ModelMessage[]=[{role:'user',content:'Apply option 1.'},{role:'assistant',content:[{type:'tool-call',toolCallId:'check',toolName:'experimental_evaluate',input:{facts:{flag:false,zero:0,nothing:null,decimal:'24'}},providerOptions:{google:{thoughtSignature:signature}}}]},{role:'tool',content:[{type:'tool-result',toolCallId:'check',toolName:'experimental_evaluate',output:{type:'json',value:{outcome:'set-aside'}},providerOptions:{google:{thoughtSignature:signature}}}]}]
 const before=JSON.stringify(messages),projected=await c.messages(messages,'host',[])
 expect(JSON.stringify(messages)).toBe(before)
 expect(JSON.stringify(projected)).toContain('set-aside');expect(JSON.stringify(projected)).not.toContain(signature)
 const data=JSON.parse(String(projected[1]!.content).split('\n').slice(1).join('\n'))
 expect(c.ledger.read(data.steps[0].read)).toMatchObject({value:{type:'json',value:{outcome:'set-aside'}}})
 const ref=c.ledger.list().items[0]!
 expect(c.ledger.read({id:ref.contextId,path:'/0/content/0/input/facts'})).toMatchObject({value:{flag:false,zero:0,nothing:null,decimal:'24'}})
 expect(utf8Bytes(JSON.stringify(projected))).toBeLessThan(inputBytes(c.policy))
})
it('keeps a flat result index and the authoring contract across repeated compactions',async()=>{
 const input=session();input.hostTools=[{name:'future_reference_tool',effect:'read',contextPolicy:{reference:true},description:'Reference',inputSchema:{},execute:async()=>({})}];const c=new SessionContext(input);let messages:ModelMessage[]=[{role:'user',content:'Make the requested change.'}]
 for(let i=0;i<8;i++){
  const name=i===0?'future_reference_tool':'check_'+i
  messages.push({role:'assistant',content:[{type:'tool-call',toolCallId:'c'+i,toolName:name,input:{},providerOptions:{google:{thoughtSignature:'s'.repeat(40000)}}}]},{role:'tool',content:[{type:'tool-result',toolCallId:'c'+i,toolName:name,output:{type:'json',value:i===0?{content:[{type:'text',text:'Authoritative contract fixture'}]}:{outcome:'checked_'+i}}}]})
  messages=await c.messages(messages,'host',[])
 }
 const summary=String(messages[1]!.content),data=JSON.parse(summary.split('\n').slice(1).join('\n'))
 expect(summary).toContain('Authoritative contract fixture');expect(data.steps).toHaveLength(8)
 for(const step of data.steps)expect(c.ledger.read(step.read)).toHaveProperty('value')
 for(const entry of c.ledger.snapshot().entries)expect(JSON.stringify(entry.value)).not.toContain('Earlier completed steps were compacted')
})
it('does not compact an unresolved call just to fit a signed response',async()=>{
 const c=new SessionContext(session())
 await expect(c.messages([{role:'user',content:'task'},{role:'assistant',content:[{type:'tool-call',toolCallId:'pending',toolName:'save',input:{},providerOptions:{google:{thoughtSignature:'s'.repeat(50000)}}}]}],'host',[])).rejects.toBeInstanceOf(ContextBudgetError)
})

it('starts an independent review with no author summary, reference material or cached context read',async()=>{
 const input=session();input.hostTools=[{name:'future_reference',effect:'read',contextPolicy:{reference:true},description:'Read task reference',inputSchema:{},execute:async()=>({})}]
 const {context:c,session:prepared}=await contextualSession(input)
 const original=await c.ledger.retain({id:'author-only',kind:'artifact',label:'Author material',value:{private:'AUTHOR_ONLY_SENTINEL'}})
 const read=prepared.hostTools.find(t=>t.name==='read_context')!
 await read.execute({id:original.id},input.signal)
 const pair=(label:string):ModelMessage[]=>[{role:'user',content:'Current explicit task'},{role:'assistant',content:[{type:'tool-call',toolCallId:label,toolName:'future_reference',input:{},providerOptions:{google:{thoughtSignature:'s'.repeat(50000)}}}]},{role:'tool',content:[{type:'tool-result',toolCallId:label,toolName:'future_reference',output:{type:'json',value:{reference:label}}}]}]
 expect(JSON.stringify(await c.messages(pair('AUTHOR_ONLY_SENTINEL'),'host',[]))).toContain('AUTHOR_ONLY_SENTINEL')
 const scope=c.ledger.scope;c.beginIsolatedPhase()
 expect(c.ledger.scope).not.toBe(scope)
 await expect(read.execute({id:original.id},input.signal)).rejects.toThrow(/unavailable/)
 const review=await c.messages(pair('REVIEW_ONLY_SENTINEL'),'host',[])
 expect(JSON.stringify(review)).not.toContain('AUTHOR_ONLY_SENTINEL')
 expect(JSON.stringify(c.ledger.snapshot())).not.toContain('AUTHOR_ONLY_SENTINEL')
 expect(JSON.stringify(review)).toContain('REVIEW_ONLY_SENTINEL')
})
