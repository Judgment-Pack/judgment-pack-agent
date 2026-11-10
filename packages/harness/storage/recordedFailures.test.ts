/**
 * #352: a durable call's failure is recorded with the form the record guard (ADR-0018) leaves of its detail alone, read whole
 * and then cut to the thousand UTF-16 code units a detail may hold, and the call fails with that same form: the error it throws is given it
 * in place, so a check of the error's identity still holds, and the record and what the model is given agree. A model's
 * failure is stored in that form. A guard refusal is an unknown outcome, and nothing of the failure is stored. The Go twins
 * are go/context/recorded_failures_test.go.
 */
import {describe,expect,it} from 'vitest'
import {RunWriter,StorageOutcomeUnknown,type DurableBinding} from './writer'
import {MemoryRunStore} from './run'
import {RECORD_DEFAULTS,type RecordGuard} from '../records'
import {OutputGuard} from '../outputGuard'
import {inspect} from 'node:util'
import {AIFailureError,authoredFailure,normalizeFailure,restateFailure,takeWholeDetail,toolFailureResult} from '../failures'
import {ofType,sseTurn,streamed,vercelTurn,type EndpointRequest} from '../testing/scripted'
import {vercel} from '../engines/vercel'
import type {ExecutionCheckpoint} from '../execution/state'
import type {HostTool} from '../engine'

const RAW='https://packs.example/tmp/evaluation-pack',REDACTED='https://packs.example[redacted: local-temp-path]',RAW_PATH='packs.example/tmp/'
const DETAIL=`Could not read ${RAW} today`,RECORDED=`Could not read ${REDACTED} today`
const decode=(bytes:Uint8Array|null|undefined)=>bytes?new TextDecoder().decode(bytes):''
const checkpoint=(id:string):ExecutionCheckpoint=>({version:2,id,requestKey:'a'.repeat(64),phase:'work',status:'running',unit:'model-requests',used:0,limit:20,grants:[],plan:[],explanation:'',operations:[],context:{scope:'fixture',entries:[]}})
const setup=(id:string,mode:'redact'|'refuse'='redact',store=new MemoryRunStore())=>{
 const writer=new RunWriter(store,id),binding:DurableBinding={writer,root:true,scope:{execution:id,role:'author'}}
 const guard=new OutputGuard({outputGuard:{value:mode,layer:'host'}} as never,()=>({}),()=>{}) as unknown as RecordGuard
 writer.bind(()=>writer.checkpoint??checkpoint(id),RECORD_DEFAULTS,()=>guard)
 return {writer,binding,store}
}
const stored=async(store:MemoryRunStore,id:string)=>{
 const text=decode((await store.load(id))?.checkpoint)
 return {text,row:(JSON.parse(text) as {durable:{dispatches:{kind:string;state:string;failure?:unknown}[]}}).durable.dispatches[0]!}
}
const failing=(error:Error)=>async():Promise<never>=>{throw error}

describe('a durable call’s failure the record guard redacts (#352)',()=>{
 it('is recorded, and fails the call, in the recorded form: the same error, given it in place',async()=>{
  const {writer,binding,store}=setup('failed'),thrown=authoredFailure('tool-failed',DETAIL,'tool')
  const error=await writer.invoke(binding,'host','source','evaluate','{}',false,failing(thrown)).then(()=>undefined,(e:unknown)=>e)
  expect(error).toBe(thrown)
  expect(thrown.failure).toEqual({version:1,code:'tool-failed',source:'tool',outcome:'unknown',detail:RECORDED})
  expect(thrown.message).toBe(RECORDED)
  // What the model reads of it.
  expect(toolFailureResult(error)).toEqual({isError:true,content:[{type:'text',text:RECORDED}],structuredContent:{failure:{version:1,code:'tool-failed',source:'tool',outcome:'unknown',detail:RECORDED}}})
  const {text,row}=await stored(store,'failed')
  expect(row.state).toBe('unknown')
  expect(row.failure).toEqual({version:1,code:'tool-failed',source:'tool',outcome:'unknown',detail:RECORDED})
  expect(text).not.toContain(RAW_PATH)
 })
 it('a failure it reads from an error the call’s error wraps: that error is given the form',async()=>{
  const {writer,binding,store}=setup('wrapped'),inner=authoredFailure('tool-failed',DETAIL,'tool'),thrown=new Error('wrapper',{cause:inner})
  const error=await writer.invoke(binding,'host','source','evaluate','{}',false,failing(thrown)).then(()=>undefined,(e:unknown)=>e)
  expect(error).toBe(thrown)
  expect(inner.failure.detail).toBe(RECORDED)
  expect(JSON.stringify(toolFailureResult(error))).not.toContain(RAW_PATH)
  expect((await stored(store,'wrapped')).row.failure).toEqual({version:1,code:'tool-failed',source:'tool',outcome:'unknown',detail:RECORDED})
 })
 it('a detail the guard leaves as it is: the error and the row keep it',async()=>{
  const kept='Could not read https://packs.example/var/evaluation-pack.',{writer,binding,store}=setup('kept'),thrown=authoredFailure('tool-failed',kept,'tool')
  const before=thrown.failure
  await expect(writer.invoke(binding,'host','source','evaluate','{}',false,failing(thrown))).rejects.toBe(thrown)
  expect(thrown.failure).toBe(before)
  expect((await stored(store,'kept')).row.failure).toEqual({version:1,code:'tool-failed',source:'tool',outcome:'unknown',detail:kept})
 })
 it('redaction past a thousand UTF-16 code units: the detail is cut there, and a pair the cut would split is left out whole',async()=>{
  const filler='a'.repeat(950),detail=`${RAW} ${filler}\u{1F600}`,want=`${REDACTED} ${filler}`
  expect(detail.length).toBe(994)
  const {writer,binding,store}=setup('cut'),thrown=authoredFailure('tool-failed',detail,'tool')
  await expect(writer.invoke(binding,'host','source','evaluate','{}',false,failing(thrown))).rejects.toBe(thrown)
  expect(thrown.failure.detail).toBe(want)
  expect(want.length).toBe(999)
  expect((await stored(store,'cut')).row.failure).toEqual({version:1,code:'tool-failed',source:'tool',outcome:'unknown',detail:want})
 })
 it('a refusing guard: the outcome is unknown, and nothing of the failure is stored',async()=>{
  const {writer,binding,store}=setup('refused','refuse'),thrown=authoredFailure('tool-failed',DETAIL,'tool')
  await expect(writer.invoke(binding,'host','source','evaluate','{}',false,failing(thrown))).rejects.toBeInstanceOf(StorageOutcomeUnknown)
  const {text,row}=await stored(store,'refused')
  expect(row.state).toBe('claimed')
  expect(row.failure).toBeUndefined()
  expect(text).not.toContain(RAW_PATH)
 })
 it('a model’s failure is stored in the recorded form',async()=>{
  const {writer,binding,store}=setup('model-failed'),handle=await writer.startModel(binding,'source','fixture','{}')
  await writer.finishModel(binding,handle,{ok:false,error:new AIFailureError({version:1,code:'unavailable',source:'model',outcome:'unknown',detail:DETAIL})})
  const {text,row}=await stored(store,'model-failed')
  expect(row.state).toBe('unknown')
  expect(row.failure).toEqual({version:1,code:'unavailable',source:'model',outcome:'unknown',detail:RECORDED})
  expect(text).not.toContain(RAW_PATH)
 })
 it('a model’s failure a refusing guard withholds: the outcome is unknown, and nothing of it is stored',async()=>{
  const {writer,binding,store}=setup('model-refused','refuse'),handle=await writer.startModel(binding,'source','fixture','{}')
  await expect(writer.finishModel(binding,handle,{ok:false,error:new AIFailureError({version:1,code:'unavailable',source:'model',outcome:'unknown',detail:DETAIL})})).rejects.toBeInstanceOf(StorageOutcomeUnknown)
  const {text,row}=await stored(store,'model-refused')
  expect(row.state).toBe('claimed')
  expect(row.failure).toBeUndefined()
  expect(text).not.toContain(RAW_PATH)
 })
 it('through the API engine: the model is given the recorded form, and nothing stored or shown holds the raw text',async()=>{
  const store=new MemoryRunStore(),requests:EndpointRequest[]=[]
  const tool={name:'evaluate',description:'Evaluate a pack.',inputSchema:{type:'object'},taxonomy:{operation:'read',effect:'read'},effect:'read',
   execute:async()=>{throw authoredFailure('tool-failed',DETAIL,'tool')}} as unknown as HostTool
  const out=await vercelTurn({engine:vercel,hostTools:[tool],
   answer:(request:EndpointRequest)=>{requests.push(request);return streamed(request.number===1?sseTurn({tool:{name:'evaluate',args:{pack:'evaluation-pack'},id:'call_1'}}):sseTurn({text:'Done.'}))},
   session:{context:{currentRequest:{message:'Evaluate it.',input:'Evaluate it.'},runStore:{store,id:'engine'}}} as never})
  const given=requests.map(request=>JSON.stringify(request.body))
  expect(given.length).toBeGreaterThan(1)
  expect(given.at(-1)).toContain(JSON.stringify(RECORDED).slice(1,-1))
  for(const text of given)expect(text).not.toContain(RAW_PATH)
  expect(JSON.stringify(out.events)).not.toContain(RAW_PATH)
  expect(ofType(out.events,'tool_result').some(e=>e.text.includes('[redacted: local-temp-path]'))).toBe(true)
  const row=(JSON.parse(decode((await store.load('engine'))?.checkpoint)) as {durable:{dispatches:{kind:string;failure?:unknown}[]}}).durable.dispatches.find(d=>d.kind==='host')
  expect(row?.failure).toEqual({version:1,code:'tool-failed',source:'tool',outcome:'unknown',detail:RECORDED})
  expect(decode((await store.load('engine'))?.checkpoint)).not.toContain(RAW_PATH)
 })
})

// A detail the guard reads whole: authoredFailure cuts a detail to the thousand units it may hold, and a match the cut would
// split (a private key whose END marker lies past unit 1000) is found only in the whole detail. The fixtures' key is synthetic;
// the Go twins are in recorded_failures_test.go.
const KEY_BODY='TUlJRWtleUJvZHlGb3JUZXN0aW5nT25seQ'.repeat(8),RSA_KEY=`-----BEGIN RSA PRIVATE KEY-----\n${KEY_BODY}\n-----END RSA PRIVATE KEY-----`,KEY_REDACTED='[redacted: private-key]'
const EC_BODY='Q'.repeat(227),EC_BEGIN='-----BEGIN EC PRIVATE KEY-----\n',EC_FILLER='x'.repeat(1000-EC_BEGIN.length-EC_BODY.length-1-10)
const WHOLE=[
 // The key's body crosses unit 1000, its END marker wholly past it.
 {name:'a body across the cut',raw:`Rejected key after ${'x'.repeat(800)}: ${RSA_KEY} (bad padding)`,want:`Rejected key after ${'x'.repeat(800)}: ${KEY_REDACTED} (bad padding)`},
 // Its END marker crosses unit 1000.
 {name:'an END marker across the cut',raw:`Rejected key after ${'x'.repeat(660)}: ${RSA_KEY} (bad padding)`,want:`Rejected key after ${'x'.repeat(660)}: ${KEY_REDACTED} (bad padding)`},
 // An EC-sized key whose body lies within the cut and whose END marker alone crosses it.
 {name:'an EC key with only its END marker cut',raw:`${EC_FILLER}${EC_BEGIN}${EC_BODY}\n-----END EC PRIVATE KEY----- (bad curve)`,want:`${EC_FILLER}${KEY_REDACTED} (bad curve)`},
]
const toolFailed=(detail:string)=>({version:1,code:'tool-failed',source:'tool',outcome:'unknown',detail})

describe('a failure’s detail the record guard reads whole, before the cut (#352)',()=>{
 for(const c of WHOLE)it(c.name,async()=>{
  const thrown=authoredFailure('tool-failed',c.raw,'tool'),cut=c.raw.slice(0,1000)
  expect(c.raw.length).toBeGreaterThan(1000)
  expect(cut).toContain('-----BEGIN')
  // Where no durable writer reads it, the failure is as it was: cut to a thousand units.
  expect(thrown.failure).toEqual(toolFailed(cut))
  expect(thrown.message).toBe(cut)
  expect(normalizeFailure(thrown,'tool')).toEqual(toolFailed(cut))
  // The whole detail is no member of the error: nothing encodes, prints or clones it.
  const shown=[JSON.stringify(thrown),String(thrown),inspect(thrown,{showHidden:true,depth:8}),JSON.stringify(Object.getOwnPropertyNames(thrown).map(key=>(thrown as unknown as Record<string,unknown>)[key])),inspect(structuredClone(thrown),{showHidden:true,depth:8})].join('|')
  expect(shown).not.toContain('PRIVATE KEY----- (')
  const {writer,binding,store}=setup('whole')
  const error=await writer.invoke(binding,'host','source','evaluate','{}',false,failing(new Error('evaluate',{cause:thrown}))).then(()=>undefined,(e:unknown)=>e)
  expect(thrown.failure).toEqual(toolFailed(c.want))
  expect(thrown.message).toBe(c.want)
  // Exactly what the model is given, and exactly what the row holds.
  expect(JSON.stringify(toolFailureResult(error))).toBe(JSON.stringify({isError:true,content:[{type:'text',text:c.want}],structuredContent:{failure:toolFailed(c.want)}}))
  const {text,row}=await stored(store,'whole')
  // The record's canonical bytes: its members in key order.
  expect(JSON.stringify(row.failure)).toBe(JSON.stringify({code:'tool-failed',detail:c.want,outcome:'unknown',source:'tool',version:1}))
  for(const part of [KEY_BODY.slice(0,34),'QQQQQQQQ','PRIVATE KEY-----\\n'])expect(text).not.toContain(part)
 })
 it('a long detail the guard leaves as it is: the error keeps its own, the row holds that, and the whole detail is dropped',async()=>{
  const raw='y'.repeat(1200),thrown=authoredFailure('tool-failed',raw,'tool'),before=thrown.failure
  const {writer,binding,store}=setup('long')
  await expect(writer.invoke(binding,'host','source','evaluate','{}',false,failing(thrown))).rejects.toBe(thrown)
  expect(thrown.failure).toBe(before)
  expect((await stored(store,'long')).row.failure).toEqual(toolFailed('y'.repeat(1000)))
  expect(takeWholeDetail(thrown)).toBe('y'.repeat(1000))
 })
 it('the whole detail is read only for the detail it was cut to',async()=>{
  const thrown=authoredFailure('tool-failed',WHOLE[0]!.raw,'tool')
  restateFailure(thrown,toolFailed('Rejected a key.') as never)
  const {writer,binding,store}=setup('restated')
  await expect(writer.invoke(binding,'host','source','evaluate','{}',false,failing(thrown))).rejects.toBe(thrown)
  expect((await stored(store,'restated')).row.failure).toEqual(toolFailed('Rejected a key.'))
 })
 it('a model’s failure',async()=>{
  const c=WHOLE[1]!,{writer,binding,store}=setup('model-whole'),handle=await writer.startModel(binding,'source','fixture','{}')
  await writer.finishModel(binding,handle,{ok:false,error:authoredFailure('unavailable',c.raw,'model')})
  const {text,row}=await stored(store,'model-whole')
  expect(row.failure).toEqual({version:1,code:'unavailable',source:'model',outcome:'unknown',detail:c.want})
  expect(text).not.toContain(KEY_BODY.slice(0,34))
 })
 it('authoredFailure cuts as Go’s Authored does: a pair the cut would split is left out whole',()=>{
  expect(authoredFailure('tool-failed','a'.repeat(999)+'\u{1F600}tail','tool').failure.detail).toBe('a'.repeat(999))
  expect(authoredFailure('tool-failed','a'.repeat(998)+'\u{1F600}tail','tool').failure.detail).toBe('a'.repeat(998)+'\u{1F600}')
 })
})
