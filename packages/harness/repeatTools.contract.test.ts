import {it,expect,vi} from 'vitest'
import {resolvePolicy,PolicyRefusal} from './policy'
import {readExecution} from './execution/state'
import {checkRecord,readPolicyLayer,writeRunEvent} from './recordReader'
import {writeRecord,CHECKPOINT_SCHEMA,RECORD_DEFAULTS} from './records'
import {vercel} from './engines/vercel'
import {MemoryRunStore} from './storage/run'
import type {AssistantSession} from './engine'
import vectors from '../../contract/testdata/repeat-tools/records.json'
it('#204 shared current records refuse historical versions',()=>{
 for(const value of vectors.policies){expect(readPolicyLayer(value)).toEqual(value);expect(()=>readPolicyLayer({...value,version:1})).toThrow()}
 for(const value of vectors.events){expect(()=>checkRecord('event',value)).not.toThrow();expect(()=>checkRecord('event',{...value,version:1})).toThrow();expect(writeRunEvent(value as any).version).toBe(2)}
 expect(readExecution(vectors.checkpoint)).toEqual(vectors.checkpoint);expect(()=>readExecution({...vectors.checkpoint,version:2})).toThrow()
 const written=writeRecord(CHECKPOINT_SCHEMA,{...vectors.checkpoint,repeatTools:undefined},RECORD_DEFAULTS,undefined,new Date('2026-10-26T00:00:00Z'));expect(written.version).toBe(3)
 const bad=structuredClone(vectors.checkpoint);bad.repeatTools.executions[0]!.count=0;expect(()=>readExecution(bad)).toThrow()
})
it('#204 admits host thresholds and validates malformed-policy refusal events',()=>{
 const table={models:{author:{model:'fixture',reasoning:'off'}}},host={connections:['fixture'],agent:false,requests:16}
 const admitted=resolvePolicy({version:2,repeatTools:{mode:'on'}},{},table,host).members
 expect(admitted['repeatTools.remindAt']).toMatchObject({value:1,layer:'table'});expect(admitted['repeatTools.stopAt']!.value).toBe(5)
 for(const repeatTools of ['',null,{mode:'invalid'},{remindAt:0},{stopAt:401},{remindAt:3,stopAt:2},{unknown:1}]){let refusal:unknown;try{resolvePolicy({version:2,repeatTools} as any,{},table,host)}catch(error){refusal=error};expect(refusal).toBeInstanceOf(Error);expect(()=>checkRecord('event',writeRunEvent((refusal as PolicyRefusal).event()))).not.toThrow()}
 const off=resolvePolicy({version:2,repeatTools:{mode:'off'}},{},table,host).members;expect(Object.keys(off).filter(key=>key.startsWith('repeatTools.'))).toEqual([])
})
it('#204 explicit off and absent policy produce identical request/result/checkpoint/event bytes',async()=>{
 vi.useFakeTimers({toFake:['Date']});vi.setSystemTime(new Date('2026-10-26T00:00:00Z'));let sequence=0;const random=vi.spyOn(crypto,'getRandomValues').mockImplementation(((array:Uint8Array)=>{array.fill(7);return array}) as typeof crypto.getRandomValues);const uuid=vi.spyOn(crypto,'randomUUID').mockImplementation(()=>`00000000-0000-4000-8000-${String(++sequence).padStart(12,'0')}`)
 try{
  const capture=async(off:boolean)=>{sequence=0;const store=new MemoryRunStore(),bodies:string[]=[];let sends=0
   const input:AssistantSession={prompt:'Read.',testPrompt:'',allowConversation:true,critic:'off',tools:[],hostTools:[{name:'lookup',description:'Read one value.',effect:'read',taxonomy:{operation:'read',effect:'read'},inputSchema:{type:'object',properties:{}},execute:async()=>({content:[{type:'text',text:'Value.'}]})}],callTool:vi.fn(),signal:new AbortController().signal,model:{family:'openai-compatible',model:'fixture',call:async(_suffix,request)=>{bodies.push(request.body);sends++;return new Response(JSON.stringify({id:'answer',object:'chat.completion',model:'fixture',choices:[{index:0,message:{role:'assistant',content:sends<=2?null:'Done.',...(sends<=2?{tool_calls:[{id:`wire-${sends}`,type:'function',function:{name:'lookup',arguments:'{}'}}]}:{})},finish_reason:sends<=2?'tool_calls':'stop'}],usage:{prompt_tokens:2,completion_tokens:1}}),{headers:{'content-type':'application/json'}})}},context:{currentRequest:{message:'Read',input:'Fixed'},runStore:{store,id:'off-204'}},...(off?{policy:{version:2,repeatTools:{mode:'off' as const}}}:{})}
   const events=[];for await(const event of vercel.start(input))events.push(event)
   expect(events.filter(event=>event.type==='error')).toEqual([]);expect(bodies).toHaveLength(3)
   const checkpoint=new TextDecoder().decode((await store.load('off-204'))!.checkpoint!),stored=await store.readEvents('off-204',null,100)
   expect(checkpoint).not.toContain('repeatTools');expect(JSON.parse(checkpoint).version).toBe(2)
   return {bodies,checkpoint,events,stored:stored.items.map(row=>new TextDecoder().decode(row.bytes))}
  }
  const explicit=await capture(true),absent=await capture(false);for(const key of ['bodies','checkpoint','events','stored'] as const){const a=JSON.stringify(explicit[key]),b=JSON.stringify(absent[key]);const at=[...a].findIndex((c,i)=>c!==b[i]);expect(at,`${key} first byte difference at ${at}: ${a.slice(Math.max(0,at-40),at+80)} / ${b.slice(Math.max(0,at-40),at+80)}`).toBe(-1);expect(a.length).toBe(b.length)}
 }finally{uuid.mockRestore();random.mockRestore();vi.useRealTimers()}
})
it('#204 shared exact custody record rejects missing, changed, orphan and nonterminal evidence',()=>{
 const cp=vectors.custodyCheckpoint;expect(readExecution(cp)).toEqual(cp)
 const historical={...structuredClone(cp),version:2};delete (historical as any).repeatTools;expect(()=>readExecution(historical)).toThrow()
 for(const change of ['mismatch','orphan','unknown','model'] as const){
  const bad=structuredClone(cp),row=bad.durable.dispatches[0]!
  if(change==='mismatch')row.repeatResultJson='{"n":2}'
  if(change==='orphan')delete (bad as any).repeatTools
  if(change==='unknown')row.state='unknown'
  if(change==='model')row.kind='model'
  expect(()=>readExecution(bad),change).toThrow()
 }
 for(const field of ['execution','remindAt','stopAt','count','reminded','reminderDelivered','stopped','seen'] as const){const bad=structuredClone(cp);delete (bad.repeatTools.executions[0] as any)[field];expect(()=>readExecution(bad),field).toThrow()}
 const {version:_,...event}=vectors.events[0]!;expect(writeRunEvent(event as any).version).toBe(2)
})
