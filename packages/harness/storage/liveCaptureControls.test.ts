import {beforeAll,it,expect,vi} from 'vitest'
import {codex} from '../engines/codex'
import {vercel} from '../engines/vercel'
import {MemoryRunStore} from './run'
import {exportRunArchive,readRunArchive} from '../history/archive'
import {readArchiveCapturedCorpusView,ReplayUnavailable} from '../history/capture'
import {readRetainedRunHistory} from '../history/view'
import {readCapturedCorpusView,type CaptureReadView} from '../history/capture'
import {historyObject,historyJSONBytes,historyJSONDigest,chunkHistoryValue,readHistoryChunkManifest} from '../history/codec'
import {canonicalJSON} from '../context/chat'
import {answerWith} from '../testing/scenarioDriver'
import type {AgentAssistantSession,AssistantSession,AssistantEvent} from '../engine'
const manifest={id:'capture-controls',packageVersion:'0.11.0',policy:{},files:{requests:'requests.json',toolResults:'tool-results.json',events:'events.json',checkpoint:'checkpoint.json'},expected:{file:'expected.json'},tolerance:[]} as const
const consume=async(stream:AsyncIterable<AssistantEvent>)=>{const events:AssistantEvent[]=[];for await(const event of stream)events.push(event);return events}
const expectedBindings={desk_host_0:'lookup',desk_host_1:'unused'}
let retained:Awaited<ReturnType<typeof readRetainedRunHistory>>
beforeAll(async()=>{
 const store=new MemoryRunStore(),sink={write:vi.fn()},input:AgentAssistantSession={prompt:'Read.',testPrompt:'',allowConversation:true,critic:'off',policy:{version:2,routing:{mode:'rules',tiers:['low']}},route:{route:'agent',tier:'low',skills:[],tools:['lookup','unused'],operations:'all',planning:'withheld'},tools:[],hostTools:[{name:'lookup',description:'Read.',effect:'read',taxonomy:{operation:'read',effect:'read'},inputSchema:{type:'object',properties:{}},execute:async()=>({content:[{type:'text',text:'Value.'}]})},{name:'unused',description:'Unused source.',effect:'read',taxonomy:{operation:'read',effect:'read'},inputSchema:{type:'object',properties:{}},execute:async()=>{throw Error('unused source called')}}],callTool:vi.fn(),signal:new AbortController().signal,record:true,corpus:{manifest:{...manifest,tolerance:[]},guard:{value:<T>(v:T)=>v},sink},context:{runStore:{store,id:'capture-controls'}},agent:{model:'fixture',run:async(request,callbacks)=>{await callbacks.tool({id:'lookup',name:request.tools[0]!.name,arguments:{}},callbacks.signal);await callbacks.event({type:'message',id:'final',text:'Done.',phase:'final'})}}}
 expect((await consume(codex.start(input))).filter(e=>e.type==='error')).toEqual([]);expect(sink.write).toHaveBeenCalledTimes(1);retained=await readRetainedRunHistory(store,'capture-controls');expect(readCapturedCorpusView(retained).entry.toolResults).toHaveLength(1)
 const entry=readCapturedCorpusView(retained).entry;expect(entry.requests[0]!.response.headers['x-jp-native-tool-bindings']).toBe(canonicalJSON(expectedBindings))
 const observations=retained.commits.flatMap(commit=>commit.observations.map(ref=>JSON.parse(new TextDecoder().decode(retained.readObject(ref))))),admission=observations.find(row=>row.kind==='capture-request');expect(admission.nativeBindings).toBeDefined();expect(new TextDecoder().decode(retained.readObject(admission.nativeBindings))).toBe(canonicalJSON(expectedBindings))
 const archive=await exportRunArchive(store,'capture-controls'),imported=readRunArchive(archive.bytes,archive.digest);expect(imported.readDisplay(admission.nativeBindings,'evidence').document).toEqual(expectedBindings);expect(readArchiveCapturedCorpusView(imported).entry.requests).toEqual(entry.requests)
 const restarted=await readRetainedRunHistory(store,'capture-controls');expect(readCapturedCorpusView(restarted).entry.requests).toEqual(entry.requests)
})
async function altered(file:string|undefined,change:(value:any)=>void,captureChange?:(capture:any)=>void):Promise<CaptureReadView>{
 const objects=new Map<string,Uint8Array>(),head=structuredClone(retained.head),metadata=structuredClone(retained.metadata),row=metadata.find(row=>row.record.kind==='capture')!
 if(row.record.kind!=='capture')throw Error('capture required')
 const read=(ref:{digest:string})=>objects.get(ref.digest)??retained.readObject(ref as never)
 const put=async(value:unknown)=>{const object=await historyObject(historyJSONBytes(value));objects.set(object.digest,object.value);return {digest:object.digest,bytes:object.bytes}}
 const capture=JSON.parse(new TextDecoder().decode(read(row.record.capture)))
 if(file){const entry=capture.files.find((row:any)=>row.name===file),m=readHistoryChunkManifest(read(entry.manifest)),bytes=new Uint8Array(m.parts.flatMap(part=>Array.from(read(part)))),value=JSON.parse(new TextDecoder().decode(bytes));change(value);const chunks=await chunkHistoryValue(historyJSONBytes(value));for(const object of [...chunks.objects,chunks.manifest])objects.set(object.digest,object.value);entry.manifest={digest:chunks.manifest.digest,bytes:chunks.manifest.bytes}}
 captureChange?.(capture);row.record.capture=await put(capture);const {digest:_,...body}=row;row.digest=historyJSONDigest(body);head.metadata!.root=row.digest
 return {head,commits:retained.commits,metadata,readObject:ref=>read(ref)}
}
for(const fault of ['extra-event','missing-event','reordered-events','projection-absent','projection-invalid','alias-absent','alias-tampered','alias-called-tampered','alias-unused-tampered','alias-empty','alias-duplicate','alias-partial','request-digest'] as const)it(`refuses altered original projected capture ${fault}`,async()=>{
 const file=fault.startsWith('projection')?undefined:fault.endsWith('event')||fault==='reordered-events'?'events.json':'requests.json'
 const view=await altered(file,value=>{
  if(fault==='extra-event')value.push({version:1,type:'message',text:'Extra.'})
  if(fault==='missing-event')value.shift()
  if(fault==='reordered-events')[value[0],value[1]]=[value[1],value[0]]
  if(fault.startsWith('alias')){const headers=value[0].response.headers;if(fault==='alias-absent')delete headers['x-jp-native-tool-bindings'];else{const mapping=JSON.parse(headers['x-jp-native-tool-bindings']),keys=Object.keys(mapping).sort(),key=keys[0]!;if(fault==='alias-tampered'){for(const alias of keys)mapping[alias]='different-source-'+alias}else if(fault==='alias-called-tampered'||fault==='alias-unused-tampered'){const selected=keys.find(alias=>(mapping[alias]==='lookup')===(fault==='alias-called-tampered'));expect(selected).toBeDefined();mapping[selected!]='different-source'}else if(fault==='alias-empty')mapping[key]='';else if(fault==='alias-duplicate'){expect(keys.length).toBeGreaterThan(1);mapping[key]=mapping[keys[1]!]}else delete mapping[key];headers['x-jp-native-tool-bindings']=canonicalJSON(mapping)}}
  if(fault==='request-digest')value[0].request.prompt+=' altered'
 },capture=>{if(fault==='projection-absent')delete capture.eventProjection;if(fault==='projection-invalid')capture.eventProjection='transcript-v2'})
 expect(()=>readCapturedCorpusView(view)).toThrow()
})
for(const fault of ['missing','partial','empty','duplicate','ref-mismatch','missing-object'] as const)it(`refuses altered acknowledged native source ${fault}`,async()=>{
 const objects=new Map<string,Uint8Array>(),head=structuredClone(retained.head),commits=structuredClone(retained.commits)
 const read=(ref:{digest:string})=>objects.get(ref.digest)??retained.readObject(ref as never)
 const put=async(value:unknown)=>{const item=await historyObject(historyJSONBytes(value));objects.set(item.digest,item.value);return {digest:item.digest,bytes:item.bytes}}
 let missing:string|undefined
 for(const commit of commits)for(const [index,ref] of commit.observations.entries()){
  const row=JSON.parse(new TextDecoder().decode(read(ref)));if(row.kind!=='capture-request')continue
  if(fault==='missing')delete row.nativeBindings
  else if(fault==='ref-mismatch')row.nativeBindings.bytes++
  else if(fault==='missing-object')missing=row.nativeBindings.digest
  else{const source={...expectedBindings} as Record<string,string>;if(fault==='partial')delete source.desk_host_1;else if(fault==='empty')for(const key of Object.keys(source))delete source[key];else source.desk_host_1='lookup';row.nativeBindings=await put(source)}
  commit.observations[index]=await put(row)
 }
 let previous:string|null=null;for(const commit of commits){commit.previousDigest=previous;const {digest:_,...body}=commit;commit.digest=historyJSONDigest(body);previous=commit.digest}head.root=previous!
 expect(()=>readCapturedCorpusView({head,commits,metadata:retained.metadata,readObject:ref=>{if(ref.digest===missing)throw Error('missing native source object');return read(ref)}})).toThrow(ReplayUnavailable)
})
for(const capture of [false,true])it(`correlates noninteractive HTTP tool frames only for original capture ${capture}`,async()=>{
 const store=new MemoryRunStore(),sink={write:vi.fn()};let calls=0
 const input:AssistantSession={prompt:'Read.',testPrompt:'',allowConversation:true,critic:'off',tools:[],hostTools:[{name:'lookup',description:'Read.',effect:'read',taxonomy:{operation:'read',effect:'read'},inputSchema:{type:'object',properties:{}},execute:async()=>({content:[{type:'text',text:'Value.'}]})}],callTool:vi.fn(),signal:new AbortController().signal,...(capture?{record:true,corpus:{manifest:{...manifest,tolerance:[]},guard:{value:<T>(v:T)=>v},sink}}:{}),context:{runStore:{store,id:'capture-controls'}},model:{family:'openai-compatible',model:'fixture',call:async()=>answerWith(++calls===1?{call:{tool:'lookup',arguments:{}}}:{say:'Done.'},'whole',calls)}}
 const events=await consume(vercel.start(input)),call=events.find(event=>event.type==='tool_call'),result=events.find(event=>event.type==='tool_result');expect(calls).toBe(2);expect(call).toBeDefined();expect(result).toBeDefined()
 if(capture){expect(call!.callId).toEqual(expect.any(String));expect(result!.callId).toBe(call!.callId);expect(sink.write).toHaveBeenCalledTimes(1);const view=readCapturedCorpusView(await readRetainedRunHistory(store,'capture-controls'));expect(view.entry.requests).toHaveLength(2);expect(view.entry.toolResults[0]!.name).toBe('lookup')}
 else{expect(call!.callId).toBeUndefined();expect(result!.callId).toBeUndefined();expect(sink.write).not.toHaveBeenCalled()}
})
it('refuses synthetic native binding metadata supplied by an actual HTTP model response',async()=>{
 const store=new MemoryRunStore(),sink={write:vi.fn()},input:AssistantSession={prompt:'Answer.',testPrompt:'',allowConversation:true,critic:'off',tools:[],hostTools:[],callTool:vi.fn(),signal:new AbortController().signal,record:true,corpus:{manifest:{...manifest,tolerance:[]},guard:{value:<T>(v:T)=>v},sink},context:{runStore:{store,id:'capture-controls'}},model:{family:'openai-compatible',model:'fixture',call:async()=>{const response=answerWith({say:'Done.'},'stream',0);response.headers.set('x-jp-native-tool-bindings','{}');return response}}}
 const events=await consume(vercel.start(input));expect(events.some(e=>e.type==='recorded')).toBe(false);expect(events.some(e=>e.type==='guardrail'&&e.tool==='record')).toBe(true);expect(sink.write).not.toHaveBeenCalled();expect((await store.readHistoryHead('capture-controls'))!.metadata).toBeUndefined()
})
