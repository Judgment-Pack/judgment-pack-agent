import {beforeAll,it,expect,vi} from 'vitest'
import {codex} from '../engines/codex'
import {vercel} from '../engines/vercel'
import {MemoryRunStore} from './run'
import {readRetainedRunHistory} from '../history/view'
import {readCapturedCorpusView,type CaptureReadView} from '../history/capture'
import {historyObject,historyJSONBytes,historyJSONDigest,chunkHistoryValue,readHistoryChunkManifest} from '../history/codec'
import {canonicalJSON} from '../context/chat'
import {answerWith} from '../testing/scenarioDriver'
import type {AgentAssistantSession,AssistantSession,AssistantEvent} from '../engine'
const manifest={id:'capture-controls',packageVersion:'0.11.0',policy:{},files:{requests:'requests.json',toolResults:'tool-results.json',events:'events.json',checkpoint:'checkpoint.json'},expected:{file:'expected.json'},tolerance:[]} as const
const consume=async(stream:AsyncIterable<AssistantEvent>)=>{const events:AssistantEvent[]=[];for await(const event of stream)events.push(event);return events}
let retained:Awaited<ReturnType<typeof readRetainedRunHistory>>
beforeAll(async()=>{
 const store=new MemoryRunStore(),sink={write:vi.fn()},input:AgentAssistantSession={prompt:'Read.',testPrompt:'',allowConversation:true,critic:'off',tools:[],hostTools:[{name:'lookup',description:'Read.',effect:'read',taxonomy:{operation:'read',effect:'read'},inputSchema:{type:'object',properties:{}},execute:async()=>({content:[{type:'text',text:'Value.'}]})}],callTool:vi.fn(),signal:new AbortController().signal,record:true,corpus:{manifest:{...manifest,tolerance:[]},guard:{value:<T>(v:T)=>v},sink},context:{runStore:{store,id:'capture-controls'}},agent:{model:'fixture',run:async(request,callbacks)=>{await callbacks.tool({id:'lookup',name:request.tools[0]!.name,arguments:{}},callbacks.signal);await callbacks.event({type:'message',id:'final',text:'Done.',phase:'final'})}}}
 expect((await consume(codex.start(input))).filter(e=>e.type==='error')).toEqual([]);expect(sink.write).toHaveBeenCalledTimes(1);retained=await readRetainedRunHistory(store,'capture-controls');expect(readCapturedCorpusView(retained).entry.toolResults).toHaveLength(1)
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
for(const fault of ['extra-event','missing-event','reordered-events','projection-absent','projection-invalid','alias-absent','alias-tampered','alias-partial','request-digest'] as const)it(`refuses altered original projected capture ${fault}`,async()=>{
 const file=fault.startsWith('projection')?undefined:fault.endsWith('event')||fault==='reordered-events'?'events.json':'requests.json'
 const view=await altered(file,value=>{
  if(fault==='extra-event')value.push({version:1,type:'message',text:'Extra.'})
  if(fault==='missing-event')value.shift()
  if(fault==='reordered-events')[value[0],value[1]]=[value[1],value[0]]
  if(fault.startsWith('alias')){const headers=value[0].response.headers;if(fault==='alias-absent')delete headers['x-jp-native-tool-bindings'];else{const mapping=JSON.parse(headers['x-jp-native-tool-bindings']),key=Object.keys(mapping)[0]!;if(fault==='alias-tampered')mapping[key]='different-source';else delete mapping[key];headers['x-jp-native-tool-bindings']=canonicalJSON(mapping)}}
  if(fault==='request-digest')value[0].request.prompt+=' altered'
 },capture=>{if(fault==='projection-absent')delete capture.eventProjection;if(fault==='projection-invalid')capture.eventProjection='transcript-v2'})
 expect(()=>readCapturedCorpusView(view)).toThrow()
})
it('refuses synthetic native binding metadata supplied by an actual HTTP model response',async()=>{
 const store=new MemoryRunStore(),sink={write:vi.fn()},input:AssistantSession={prompt:'Answer.',testPrompt:'',allowConversation:true,critic:'off',tools:[],hostTools:[],callTool:vi.fn(),signal:new AbortController().signal,record:true,corpus:{manifest:{...manifest,tolerance:[]},guard:{value:<T>(v:T)=>v},sink},context:{runStore:{store,id:'capture-controls'}},model:{family:'openai-compatible',model:'fixture',call:async()=>{const response=answerWith({say:'Done.'},'stream',0);response.headers.set('x-jp-native-tool-bindings','{}');return response}}}
 const events=await consume(vercel.start(input));expect(events.some(e=>e.type==='recorded')).toBe(false);expect(events.some(e=>e.type==='guardrail'&&e.tool==='record')).toBe(true);expect(sink.write).not.toHaveBeenCalled();expect((await store.readHistoryHead('capture-controls'))!.metadata).toBeUndefined()
})
