import {it,expect,vi} from 'vitest'
import {MemoryRunStore} from '../storage/run'
import {managedContext} from '../context/managed'
import type {AssistantSession} from '../engine'
import {canonicalJSON} from '../context/chat'
import {historyJSONBytes,historyJSONDigest,historyBytesDigest,HistoryInputInvalid,HistoryChanged} from './codec'
import {exportRunArchive,readRunArchive,HistoryIncomplete,ImportedRunHistory} from './archive'
import {queryHistoryEvents,findHistoryRelations} from './query'
const session=(store:MemoryRunStore):AssistantSession=>({prompt:'Work',testPrompt:'',tools:[],hostTools:[],callTool:vi.fn(async()=>({content:[]})),model:{family:'openai-compatible',model:'fixture',call:vi.fn(async()=>{throw Error('No provider in archive fixture')})},signal:new AbortController().signal,context:{currentRequest:{message:'Work',input:'Work'},runStore:{store,id:'r'}}})
async function completed(){const store=new MemoryRunStore({now:()=>new Date('2026-10-25T00:00:00.000Z')});for await(const _ of managedContext(session(store),async function*(){yield {type:'message',text:'Account code SECRET-123'};yield {type:'message',text:'second'};yield {type:'end'}})){}return store}
const members={outputPatterns:{value:[{name:'account-code',pattern:'SECRET-[0-9]+'}]}} as never
function resign(value:any){const {digest:_,...body}=value.manifest;value.manifest.digest=historyJSONDigest(body);return historyJSONBytes(value)}
it('exports the real Memory writer, redacts display data and imports a detached canonical query',async()=>{
 const store=await completed(),before=await store.load('r'),result=await exportRunArchive(store,'r',{members}),text=new TextDecoder().decode(result.bytes)
 expect(text).not.toContain('SECRET-123');expect(result.manifest.coverage).toBe('recording-incomplete');expect(result.digest).toBe(historyBytesDigest(result.bytes))
 const imported=readRunArchive(result.bytes,result.digest),events=queryHistoryEvents(imported,{types:['message']});expect(events.items.map(row=>(row.event as any).text)).toEqual(['Account code [redacted: account-code]','second']);expect(events.items[0]!.representation?.kind).toBe('redacted-view');expect(new TextDecoder().decode(imported.canonicalBytes())).toBe(text)
 expect(findHistoryRelations(imported,'job','fake')).toEqual({kind:'unavailable',rows:[]});expect(await store.load('r')).toEqual(before)
 expect('readObject'in imported).toBe(false);expect('commit'in imported).toBe(false);expect('restore'in imported).toBe(false);expect('resume'in imported).toBe(false)
 const cp=imported.readDisplay(imported.head.checkpoint!);(cp.document as any).id='changed';expect((imported.readDisplay(imported.head.checkpoint!).document as any).id).toBe('r')
 await expect(exportRunArchive(store,'r',{members,requireComplete:true})).rejects.toThrow(HistoryIncomplete)
 expect(()=>new ImportedRunHistory(Symbol(),{} as never,new Map(),new Map(),new Uint8Array())).toThrow(HistoryInputInvalid)
})
it('rejects replacement pins, corrupt or undeclared members, order/truncation/version/path/duplicate JSON attacks',async()=>{
 const {bytes}=await exportRunArchive(await completed(),'r',{members});const parse=()=>JSON.parse(new TextDecoder().decode(bytes))
 expect(()=>readRunArchive(bytes,'sha256:'+'0'.repeat(64))).toThrow(HistoryInputInvalid)
 for(const change of [(a:any)=>{a.version=2},(a:any)=>{a.path='../escape'},(a:any)=>{a.objects[0].bytes='AAAA'},(a:any)=>{a.objects.reverse()},(a:any)=>{a.objects.pop()},(a:any)=>{a.objects.push(a.objects[0])},(a:any)=>{a.manifest.commits.reverse()},(a:any)=>{a.manifest.commits.pop()},(a:any)=>{a.manifest.members[0].kind='future'},(a:any)=>{a.manifest.source.eventCount++},(a:any)=>{a.manifest.coverage='complete'},(a:any)=>{a.manifest.members.find((row:any)=>row.representation.kind==='redacted-view').representation={kind:'exact'}}]){const a=parse();change(a);expect(()=>readRunArchive(resign(a))).toThrow(HistoryInputInvalid)}
 const text=canonicalJSON(parse());expect(()=>readRunArchive(new TextEncoder().encode(text.replace('"version":1','"version":1,"version":1')))).toThrow(HistoryInputInvalid)
 expect(()=>readRunArchive(new TextEncoder().encode(text+' '))).toThrow(HistoryInputInvalid)
})
it('rechecks the head after export redaction and refuses protected identity changes',async()=>{
 const store=await completed();const read=store.readHistoryHead.bind(store);let reads=0
 store.readHistoryHead=async(...args)=>{const head=await read(...args);if(++reads===3&&head)head.revision='changed';return head}
 await expect(exportRunArchive(store,'r',{members})).rejects.toThrow(HistoryChanged)
 await expect(exportRunArchive(await completed(),'r',{members:{outputPatterns:{value:[{name:'identity',pattern:'^r$'}]}} as never})).rejects.toThrow(HistoryInputInvalid)
})

import vectors from '../../../contract/fixtures/history/archive.json'
it('reads the same handwritten exact/redacted archives and canonical bytes as Go',()=>{
 for(const row of vectors.cases){const bytes=historyJSONBytes(row.archive),text=canonicalJSON(row.archive),view=readRunArchive(bytes,row.expectedDigest);expect(new TextDecoder().decode(view.canonicalBytes())).toBe(text);expect(queryHistoryEvents(view,{types:['message']}).items[0]!.event).toMatchObject({text:row.expectedMessage});expect(queryHistoryEvents(view).items[0]!.representation!.kind).toBe(row.expectedRepresentation)}
})

it('refuses a self-consistent derived event that declares a protected scope-role redaction',()=>{
 const a:any=structuredClone(vectors.cases[1]!.archive),mapping=a.manifest.mappings.find((row:any)=>row.kind==='event'),old=mapping.exported.digest,blob=a.objects.find((row:any)=>row.digest===old),view=JSON.parse(Buffer.from(blob.bytes,'base64').toString('utf8'))
 view.document.scope.role='[redacted: forged-role]';view.redactions=['/event/text','/scope/role'];const bytes=historyJSONBytes(view),ref={digest:historyBytesDigest(bytes),bytes:bytes.length};mapping.exported=ref;mapping.representation.redactions=view.redactions
 const member=a.manifest.members.find((row:any)=>row.digest===old);Object.assign(member,ref,{representation:structuredClone(mapping.representation)});blob.digest=ref.digest;blob.bytes=Buffer.from(bytes).toString('base64');a.manifest.members.sort((x:any,y:any)=>(x.source.digest+'|'+x.kind).localeCompare(y.source.digest+'|'+y.kind));a.objects.sort((x:any,y:any)=>x.digest.localeCompare(y.digest))
 expect(()=>readRunArchive(resign(a))).toThrow(HistoryInputInvalid)
})

import {prepareHistoryOriginal,prepareHistoryCompaction,prepareHistoryRequest,stageHistoryObservation} from './recording'
it('redacts whole retained original/replacement/request payloads and preserves their source DAG',async()=>{
 const store=await completed(),scope={execution:'r',role:'author'},original=await prepareHistoryOriginal(scope,'boundary','[{"text":"SECRET-123"}]','messages',0)
 const commit=async(prepared:Awaited<ReturnType<typeof prepareHistoryOriginal>>,id:string)=>{const ref=await stageHistoryObservation(store,'r',prepared),head=(await store.load('r'))!;await store.commitHistory({run:{runId:'r',expectedRevision:head.revision,commitId:id,checkpoint:head.checkpoint,events:[]},observations:[ref]});return ref}
 const originalRef=await commit(original,'original'),compaction=await prepareHistoryCompaction(scope,'boundary',[originalRef],'{"content":"Summary SECRET-123"}','summary',[],'legacy-digest'),compactionRef=await commit(compaction,'compaction'),request=await prepareHistoryRequest(scope,'dispatch','model','model','{"messages":[{"role":"user","content":"SECRET-123"}]}',[compactionRef],[originalRef]);await commit(request,'request')
 const result=await exportRunArchive(store,'r',{members}),view=readRunArchive(result.bytes),derived=view.manifest.mappings.filter(row=>row.representation.kind==='redacted-view'&&row.kind.endsWith('-chunk'))
 expect(derived).toHaveLength(3);expect(derived.map(row=>view.readDisplay(row.source).document).join(' ')).not.toContain('SECRET-123');expect(derived.every(row=>row.exported.digest!==row.source.digest)).toBe(true)
 const held=view.readDisplay(compactionRef).document as typeof compaction.observation;expect(held).toMatchObject({kind:'compaction',originals:[originalRef],replacement:compaction.observation.kind==='compaction'?compaction.observation.replacement:undefined})
 expect(view.commits.at(-1)!.observations).toEqual([request.ref]);expect(new TextDecoder().decode(view.canonicalBytes())).toBe(new TextDecoder().decode(result.bytes))
})

it('refuses conflicting source-to-derived mappings even with a recomputed manifest digest',()=>{
 const a:any=structuredClone(vectors.cases[1]!.archive),row=a.manifest.mappings.find((r:any)=>r.kind==='event'),blob=a.objects.find((b:any)=>b.digest===row.exported.digest),wrapper=JSON.parse(Buffer.from(blob.bytes,'base64').toString('utf8'))
 wrapper.document.event.text='Alternative [redacted: fixture]';const value=historyJSONBytes(wrapper),ref={digest:historyBytesDigest(value),bytes:value.length}
 a.manifest.mappings.push({...row,exported:ref});a.manifest.members.push({...ref,source:row.source,kind:row.kind,representation:row.representation});a.objects.push({digest:ref.digest,bytes:Buffer.from(value).toString('base64')})
 a.manifest.mappings.sort((x:any,y:any)=>(x.source.digest+'|'+x.kind).localeCompare(y.source.digest+'|'+y.kind));a.manifest.members.sort((x:any,y:any)=>(x.source.digest+'|'+x.kind).localeCompare(y.source.digest+'|'+y.kind));a.objects.sort((x:any,y:any)=>x.digest.localeCompare(y.digest));const {digest:_,...body}=a.manifest;a.manifest.digest=historyJSONDigest(body)
 expect(()=>readRunArchive(historyJSONBytes(a))).toThrow(HistoryInputInvalid)
})

import typedRoles from '../../../contract/fixtures/history/typed-role.json'
import {readCapturedCorpus,readArchiveCapturedCorpus,ReplayUnavailable,type CaptureReadView} from './capture'
import nativeBindingsCapture from '../../../contract/fixtures/history/capture-native-bindings.json'
for(const row of typedRoles.cases)it(`checks handwritten typed roles ${row.name}`,()=>{
 const bytes=historyJSONBytes(row.archive)
 if(!row.valid){expect(()=>readRunArchive(bytes,row.expectedDigest)).toThrow(HistoryInputInvalid);return}
 const view=readRunArchive(bytes,row.expectedDigest);expect(Array.from(view.canonicalBytes())).toEqual(Array.from(bytes));expect(view.manifest.members.length).toBeGreaterThan(view.manifest.mappings.length-1);expect(view.manifest.members.length).toBeGreaterThan(row.archive.objects.length)
 const ambiguous='ambiguous'in row?row.ambiguous:undefined
 if(ambiguous){expect(()=>view.readDisplay(ambiguous)).toThrow(HistoryInputInvalid);const roles=view.manifest.mappings.filter(m=>m.source.digest===ambiguous.digest);expect(roles.length).toBeGreaterThan(1);for(const m of roles)expect(view.readDisplay(m.source,m.kind).exported).toEqual(m.exported)}
 if(row.captureReady)expect(readArchiveCapturedCorpus(view).requests).toHaveLength(2);else expect(()=>readArchiveCapturedCorpus(view)).toThrow(ReplayUnavailable)
})

it('exports real Memory store payloads sharing typed roles with whole JSON redaction',async()=>{
 const store=await completed(),scope={execution:'r',role:'author'},text='{"text":"SECRET-123"}',original=await prepareHistoryOriginal(scope,'boundary',text,'context',0)
 const commit=async(prepared:Awaited<ReturnType<typeof prepareHistoryOriginal>>,id:string)=>{await stageHistoryObservation(store,'r',prepared);const head=(await store.load('r'))!;await store.commitHistory({run:{runId:'r',expectedRevision:head.revision,commitId:id,checkpoint:head.checkpoint,events:[]},observations:[prepared.ref]})}
 await commit(original,'role-original');const request=await prepareHistoryRequest(scope,'dispatch','model','model',text,[],[original.ref]);await commit(request,'role-request')
 const result=await exportRunArchive(store,'r',{members}),view=readRunArchive(result.bytes);for(const blob of JSON.parse(new TextDecoder().decode(result.bytes)).objects)expect(Buffer.from(blob.bytes,'base64').toString('utf8')).not.toContain('SECRET-123');expect(new TextDecoder().decode(result.bytes)).not.toContain('SECRET-123');expect(Array.from(view.canonicalBytes())).toEqual(Array.from(result.bytes));const shared=historyBytesDigest(new TextEncoder().encode(text)),aliases=view.manifest.mappings.filter(m=>m.source.digest===shared&&(m.kind==='source-chunk'||m.kind==='request-chunk'));expect(aliases).toHaveLength(2);expect(aliases[0]!.source).toEqual(aliases[1]!.source);expect(()=>view.readDisplay(aliases[0]!.source)).toThrow(HistoryInputInvalid);for(const row of aliases)expect(view.readDisplay(row.source,row.kind).document).toContain('[redacted: account-code]')
})

it('reads the shared exact native-binding fixture without granting execution or store authority',()=>{
 const source=structuredClone(nativeBindingsCapture.source),objects=new Map(Object.entries(source.objects).map(([digest,text])=>[digest,Uint8Array.from(Buffer.from(text,'base64'))]))
 const view={head:source.head,commits:source.commits,metadata:source.metadata,readObject:(ref:{digest:string})=>objects.get(ref.digest)!} as CaptureReadView
 const corpus=readCapturedCorpus(view)
 expect(corpus.requests).toHaveLength(2)
 expect(corpus.requests[1]!.response.headers['x-jp-native-tool-bindings']).toBe('{}')
 expect(corpus.requests[1]!.request).toMatchObject({tools:[]})
 const native=source.commits.flatMap(commit=>commit.observations).map(ref=>JSON.parse(new TextDecoder().decode(objects.get(ref.digest)!))).find(row=>row.kind==='capture-request'&&row.captureKind==='agent')
 expect(JSON.parse(new TextDecoder().decode(objects.get(native.nativeBindings.digest)!))).toEqual({})
 expect('commit'in corpus).toBe(false);expect('resume'in corpus).toBe(false)
})
