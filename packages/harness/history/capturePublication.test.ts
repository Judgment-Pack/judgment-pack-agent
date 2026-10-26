import {expect,it,vi} from 'vitest'
import {vercel} from '../engines/vercel'
import {answerWith} from '../testing/scenarioDriver'
import type {AssistantSession} from '../engine'
import taskIdentity from '../../../contract/fixtures/history/task-identity.json'
import repeatRecords from '../../../contract/testdata/repeat-tools/records.json'
import {MemoryRunStore} from '../storage/run'
import {StorageConflict} from '../storage/common'
import type {HistoryRunStore} from '../storage/history'
import {CaptureCollector,sealedCapture,type CaptureAdmission,type CaptureSeal} from './captureCollector'
import {prepareCapturePublication,commitCapturePublication,readCapturedCorpus,readArchiveCapturedCorpus,readCapturedCorpusView,readArchiveCapturedCorpusView,ReplayUnavailable} from './capture'
import {historyBytesDigest,historyJSONBytes,historyObject,readCanonicalHistoryJSON,HISTORY_OBJECT_BYTES} from './codec'
import {readRetainedRunHistory} from './view'
import {readRunArchive,exportRunArchive} from './archive'
import type {CorpusManifest} from '../corpus'
import {readExecution} from '../execution/state'
import {readCorpusCheckpointView} from './corpusCheckpointView'
function manifest():CorpusManifest{return {id:'publication-controls',packageVersion:'0.11.0',policy:{},files:{requests:'requests.json',toolResults:'tool-results.json',events:'events.json',checkpoint:'checkpoint.json'},expected:{file:'expected.json'},tolerance:[]}}
const originalMaterial=new WeakMap<HistoryRunStore,ReturnType<typeof sealedCapture>>()
async function fixture(){
 // Exercise the original writer and keep its actual acknowledged admissions. These HTTP
 // publication controls require no native source evidence and never retrofit historical bytes.
 const source=new MemoryRunStore(),originalSeal=CaptureCollector.prototype.seal;let material:ReturnType<typeof sealedCapture>|undefined,calls=0
 const spy=vi.spyOn(CaptureCollector.prototype,'seal').mockImplementation(function(this:CaptureCollector,...args:Parameters<CaptureCollector['seal']>){const result=originalSeal.apply(this,args);material=sealedCapture(result);return result})
 const input:AssistantSession={prompt:'Read.',testPrompt:'',allowConversation:true,critic:'off',tools:[],hostTools:[],callTool:vi.fn(),signal:new AbortController().signal,record:true,corpus:{manifest:manifest(),guard:{value:<T>(v:T)=>v},sink:{write:vi.fn()}},context:{runStore:{store:source,id:'r'}},model:{family:'openai-compatible',model:'fixture',call:async()=>{calls++;return answerWith({say:'Done.'},'stream',0)}}}
 const observed=[];try{for await(const event of vercel.start(input)){observed.push(event);expect(event.type).not.toBe('error')}}finally{spy.mockRestore()}
 expect(observed.some(event=>event.type==='recorded'),JSON.stringify(observed.filter(event=>event.type==='guardrail'))).toBe(true)
 expect(calls).toBe(1);expect(material?.rows).toHaveLength(1)
 const retained=await readRetainedRunHistory(source,'r'),store=new MemoryRunStore(),objects=new Map<string,Uint8Array>()
 // Copy only original transaction objects, leaving metadata unpublished for the controls.
 const read=(ref:{digest:string;bytes:number})=>{const bytes=retained.readObject(ref);objects.set(ref.digest,bytes);return bytes}
 for(const commit of retained.commits){if(commit.checkpoint)read(commit.checkpoint);for(const ref of [...commit.events,...commit.observations]){const raw=read(ref);if(commit.observations.includes(ref)){const row=readCanonicalHistoryJSON(raw) as any;if(row.kind==='request'){const chunks=readCanonicalHistoryJSON(read(row.request)) as any;for(const part of chunks.parts)read(part)}else if(row.kind==='original'||row.kind==='compaction'){const chunks=readCanonicalHistoryJSON(read(row.value??row.replacement)) as any;for(const part of chunks.parts)read(part)}}}}
 await store.stageHistoryObjects({kind:'run',id:'r'},await Promise.all([...objects.values()].map(historyObject)))
 for(const commit of retained.commits){const prior=await store.load('r');await store.commitHistory({run:{runId:'r',expectedRevision:prior?.revision??null,commitId:commit.commitId,checkpoint:commit.checkpoint?read(commit.checkpoint):null,events:commit.events.map(read)},observations:commit.observations})}
 const copied=await readRetainedRunHistory(store,'r');for(const {admission} of material!.rows){const commit=copied.commits[admission.sequence-1]!;expect(commit.commitId).toBe(admission.commitId);expect(commit.observations[admission.ordinal]).toEqual(admission.expectation);const checkpoint=readExecution(readCanonicalHistoryJSON(copied.readObject(commit.checkpoint!),HISTORY_OBJECT_BYTES,'finite'))!;expect(checkpoint.durable!.dispatches.find(row=>row.id===admission.identity.dispatchId)?.request).toBe(admission.request)}
 originalMaterial.set(store,material!);return store
}
function seal(store:HistoryRunStore,head:Awaited<ReturnType<HistoryRunStore['readHistoryHead']>>,change?:(a:CaptureAdmission[],frames:unknown[])=>void,bind=true):CaptureSeal{
 const owner={},collector=new CaptureCollector(owner,'r',{execution:'r',role:'author'});if(bind)collector.bindPublisher(owner,store)
 const material=originalMaterial.get(store)!;const admissions=material.rows.map(row=>structuredClone(row.admission)),frames=material.rows.map(row=>structuredClone(row.frame));change?.(admissions,frames);const tokens=admissions.map(a=>collector.admit(owner,a))
 for(const i of [...tokens.keys()].reverse()){collector.record(owner,tokens[i]!,frames[i]);collector.acknowledge(owner,tokens[i]!)}
 return collector.seal(owner,collector.rootToken(owner),{runId:'r',executionRevision:head!.executionRevision,checkpoint:head!.checkpoint!,status:'complete'})
}
const options=()=>({commitId:'capture-publication',manifest:manifest()})
it('publishes only metadata from an opaque store-bound seal and round-trips actual store corpus',async()=>{
 const store=await fixture(),before=await store.load('r'),head=(await store.readHistoryHead('r'))!,prepared=await prepareCapturePublication(store,seal(store,head),options())
 expect((await store.readHistoryHead('r'))!.metadata).toBeUndefined();expect(await store.load('r')).toEqual(before)
 const result=await commitCapturePublication(store,prepared);expect(result.capture).toBe('committed');expect(result.delivery).toBe('not-requested');expect(await store.load('r')).toEqual(before);expect((await store.readHistoryHead('r'))!.metadata!.count).toBe(1)
 const entry=readCapturedCorpus(await readRetainedRunHistory(store,'r'));expect(entry.requests).toEqual(originalMaterial.get(store)!.rows.map(row=>row.frame))
 const again=await commitCapturePublication(store,prepared);expect(again).toEqual(result);expect((await store.readHistoryHead('r'))!.metadata!.count).toBe(1)
 const exported=await exportRunArchive(store,'r'),archive=readRunArchive(exported.bytes,exported.digest);expect(Array.from(archive.canonicalBytes())).toEqual(Array.from(exported.bytes));expect(readArchiveCapturedCorpus(archive).requests).toEqual(originalMaterial.get(store)!.rows.map(row=>row.frame))
 await expect(commitCapturePublication(store,{...prepared})).rejects.toThrow(ReplayUnavailable)
})
it('refuses wrong original store identity even with identical delegated read capabilities',async()=>{
 const store=await fixture(),s=seal(store,(await store.readHistoryHead('r'))!),other=new Proxy(store,{get(target,key){const v=Reflect.get(target,key);return typeof v==='function'?v.bind(target):v}})
 await expect(prepareCapturePublication(other,s,options())).rejects.toThrow(ReplayUnavailable)
 expect(await prepareCapturePublication(store,s,options())).toBeDefined()
})
it('consumes publication seal once and refuses unbound or snapshot authority',async()=>{
 const store=await fixture(),head=await store.readHistoryHead('r'),s=seal(store,head)
 await expect(prepareCapturePublication(store,{...s},options())).rejects.toThrow(ReplayUnavailable)
 await expect(prepareCapturePublication(store,sealedCapture(s) as never,options())).rejects.toThrow(ReplayUnavailable)
 await prepareCapturePublication(store,s,options());await expect(prepareCapturePublication(store,s,options())).rejects.toThrow(ReplayUnavailable)
 await expect(prepareCapturePublication(store,seal(store,head,undefined,false),options())).rejects.toThrow(ReplayUnavailable)
})
for(const fault of ['ordinal','commit-id','expectation','frame-hidden-by-redaction'])it(`refuses publication source association ${fault}`,async()=>{
 const store=await fixture(),head=await store.readHistoryHead('r'),staging=vi.spyOn(store,'stageHistoryObjects'),s=seal(store,head,(rows,frames)=>{if(fault==='ordinal')rows[0]!.ordinal++;else if(fault==='commit-id')rows[0]!.commitId='different';else if(fault==='expectation')rows[0]!.expectation={digest:'sha256:'+'0'.repeat(64),bytes:2};else (frames[0] as any).request.body='WRONG-PRIVATE'})
 await expect(prepareCapturePublication(store,s,{...options(),members:{outputPatterns:{value:[{name:'mismatch',pattern:'WRONG-PRIVATE'}]}} as never})).rejects.toThrow(ReplayUnavailable)
 expect(staging).not.toHaveBeenCalled();expect((await store.readHistoryHead('r'))!.metadata).toBeUndefined()
})
it('refuses overbudget whole logical material before staging metadata or delivery',async()=>{
 const store=await fixture(),staging=vi.spyOn(store,'stageHistoryObjects'),metadata=vi.spyOn(store,'commitHistoryMetadata'),before=await store.readHistoryHead('r'),sink={write:vi.fn()},s=seal(store,before,(_,frames)=>{const frame=frames[0] as any;frame.response.body='BODY-MARKER';const start=new TextDecoder().decode(historyJSONBytes(frames)).indexOf('BODY-MARKER'),length=HISTORY_OBJECT_BYTES-5-start;frame.response.body='Public transcript. '.repeat(Math.ceil(length/19)).slice(0,length)+'SECRET-123';expect(new TextDecoder().decode(historyJSONBytes(frames)).indexOf('SECRET-123')).toBe(HISTORY_OBJECT_BYTES-5)})
 const publish=async()=>{const prepared=await prepareCapturePublication(store,s,{...options(),members:{outputPatterns:{value:[{name:'private',pattern:'SECRET-123'}]}} as never});return commitCapturePublication(store,prepared,{sink})}
 await expect(publish()).rejects.toThrow(ReplayUnavailable)
 expect(staging).not.toHaveBeenCalled();expect(metadata).not.toHaveBeenCalled();expect(sink.write).not.toHaveBeenCalled();expect(await store.readHistoryHead('r')).toEqual(before)
})
it('sanitizes accepted whole logical corpus material before staging and delivery',async()=>{
 const store=await fixture(),staging=vi.spyOn(store,'stageHistoryObjects'),s=seal(store,await store.readHistoryHead('r'),(_,frames)=>{(frames[0] as any).response.body='Public transcript. '.repeat(128)+'SECRET-123 public ending.'}),sink={write:vi.fn(async(_id:string,files:Record<string,string>)=>{expect(Object.values(files).join('')).not.toContain('SECRET-123');expect(files['requests.json']).toContain('[redacted: private]')})}
 const prepared=await prepareCapturePublication(store,s,{...options(),members:{outputPatterns:{value:[{name:'private',pattern:'SECRET-123'}]}} as never})
 expect(staging).toHaveBeenCalled();for(const [,objects]of staging.mock.calls)for(const object of objects)expect(new TextDecoder().decode(object.value)).not.toContain('SECRET-123')
 const result=await commitCapturePublication(store,prepared,{sink});expect(result.capture).toBe('committed');expect(result.delivery).toBe('delivered');expect(sink.write).toHaveBeenCalledTimes(1)
 const requests=readCapturedCorpusView(await readRetainedRunHistory(store,'r')).entry.requests
 expect((requests[0] as any).response.body).toBe('Public transcript. '.repeat(128)+'[redacted: private] public ending.')
})
it('publishes derived Plan display without repairing native revision or changing source execution',async()=>{
 const store=new MemoryRunStore(),cp=taskIdentity.checkpoint;await store.commit({runId:cp.id,expectedRevision:null,commitId:'terminal',checkpoint:historyJSONBytes(cp),events:[]});const before=await store.load(cp.id),head=(await store.readHistoryHead(cp.id))!,owner={},collector=new CaptureCollector(owner,cp.id,{execution:cp.id,role:'author'});collector.bindPublisher(owner,store);const s=collector.seal(owner,collector.rootToken(owner),{runId:cp.id,executionRevision:head.executionRevision,checkpoint:head.checkpoint!,status:'complete'})
 const members={outputPatterns:{value:[{name:'prose',pattern:'First secret prose'}]}} as never
 const prepared=await prepareCapturePublication(store,s,{...options(),members});expect((await commitCapturePublication(store,prepared)).capture).toBe('committed')
 const retained=await readRetainedRunHistory(store,cp.id),view=readCapturedCorpusView(retained),display=view.checkpoint.document as typeof cp
 expect(view.checkpoint.representation).toBe('derived');expect(view.checkpoint.source).toEqual(head.checkpoint);expect(display.planState.revision).toBe(cp.planState.revision);expect(display.plan[0]!.step).toBe('[redacted: prose]')
 expect(view.checkpoint.derived).toEqual({digest:historyBytesDigest(historyJSONBytes(display)),bytes:historyJSONBytes(display).length});expect(view.entry.checkpoint).toEqual(view.checkpoint)
 expect(()=>readCapturedCorpus(retained)).toThrow(ReplayUnavailable);expect(()=>readExecution(display)).toThrow();expect(()=>readExecution(view.checkpoint)).toThrow();expect(readCorpusCheckpointView(view.checkpoint)).toEqual(view.checkpoint);expect(await store.load(cp.id)).toEqual(before)
 const exported=await exportRunArchive(store,cp.id,{members}),imported=readRunArchive(exported.bytes,exported.digest);expect(Array.from(imported.canonicalBytes())).toEqual(Array.from(exported.bytes))
 expect(readArchiveCapturedCorpusView(imported).checkpoint).toEqual(view.checkpoint);expect(()=>readArchiveCapturedCorpus(imported)).toThrow(ReplayUnavailable)
 for(const blob of JSON.parse(new TextDecoder().decode(exported.bytes)).objects)expect(Buffer.from(blob.bytes,'base64').toString()).not.toContain('First secret prose')
})
it('refuses stale metadata CAS without sink delivery',async()=>{
 const store=await fixture(),head=(await store.readHistoryHead('r'))!,prepared=await prepareCapturePublication(store,seal(store,head),options()),sink={write:vi.fn()}
 await store.commitHistoryMetadata({runId:'r',expectedHistoryRevision:head.revision,expectedMetadataRevision:null,commitId:'rating-race',record:{version:1,kind:'feedback',feedback:{version:1,id:'rating',runId:'r',sequence:1,previous:null,target:{kind:'run'},rating:'good',actor:'reviewer',recordedAt:'2026-10-26T00:00:00.000Z'}}})
 await expect(commitCapturePublication(store,prepared,{sink})).rejects.toThrow(StorageConflict);expect(sink.write).not.toHaveBeenCalled();expect((await store.readHistoryHead('r'))!.metadata!.count).toBe(1)
})
for(const fault of ['receipt','metadata-page','object','lost-ack'])it(`keeps publication unknown without delivery ${fault}`,async()=>{
 const store=await fixture(),before=await store.load('r'),prepared=await prepareCapturePublication(store,seal(store,await store.readHistoryHead('r')),options()),sink={write:vi.fn()},commit=store.commitHistoryMetadata.bind(store),page=store.readHistoryMetadataPage.bind(store),read=store.readHistoryObject.bind(store);let calls=0
 store.commitHistoryMetadata=async(...args)=>{calls++;const head=await commit(...args);if(fault==='receipt')head.commitCount++;if(fault==='lost-ack'&&calls===1)throw Error('lost acknowledgement');return head}
 if(fault==='metadata-page')store.readHistoryMetadataPage=async(...args)=>({...await page(...args),hasMore:true})
 if(fault==='object')store.readHistoryObject=async(...args)=>{const object=await read(...args);object.value[0]=0;return object}
 const result=await commitCapturePublication(store,prepared,{sink});expect(result).toEqual({capture:'unknown',delivery:'not-attempted'});expect(sink.write).not.toHaveBeenCalled();expect(calls).toBe(1);expect(await store.load('r')).toEqual(before);expect((await store.readHistoryHead('r'))!.metadata!.count).toBe(1)
 if(fault==='lost-ack'){expect((await commitCapturePublication(store,prepared)).capture).toBe('committed');expect(calls).toBe(2);expect((await store.readHistoryHead('r'))!.metadata!.count).toBe(1)}
})
it('reports committed sink error separately and never automatically retries external delivery',async()=>{
 const store=await fixture(),prepared=await prepareCapturePublication(store,seal(store,await store.readHistoryHead('r')),options()),sink={write:vi.fn(async()=>{throw Error('offline')})}
 expect(await commitCapturePublication(store,prepared,{sink})).toMatchObject({capture:'committed',delivery:'unknown'});expect(await commitCapturePublication(store,prepared,{sink})).toMatchObject({capture:'committed',delivery:'unknown'});expect(sink.write).toHaveBeenCalledTimes(1);expect((await store.readHistoryHead('r'))!.metadata!.count).toBe(1)
})

it('redacts nested exact repeat result custody in imported display and omits it from report summary',async()=>{
 const cp=repeatRecords.custodyCheckpoint,store=new MemoryRunStore();expect(readExecution(cp)).toBeDefined();await store.commit({runId:cp.id,expectedRevision:null,commitId:'custody-source',checkpoint:historyJSONBytes(cp),events:[]});const before=await store.load(cp.id),head=(await store.readHistoryHead(cp.id))!
 const exported=await exportRunArchive(store,cp.id,{members:{outputPatterns:{value:[{name:'precise',pattern:'9007199254740993'}]}} as never}),view=readRunArchive(exported.bytes,exported.digest),display=view.readDisplay(head.checkpoint!,'checkpoint')
 expect(display.document).toBeDefined();expect((display.document as typeof cp).durable.dispatches[0]!.repeatResultJson).toBe('{"n":[redacted: precise]}');expect(()=>readExecution(display.document)).toThrow();expect(await store.load(cp.id)).toEqual(before)
 for(const blob of JSON.parse(new TextDecoder().decode(exported.bytes)).objects)expect(Buffer.from(blob.bytes,'base64').toString()).not.toContain('9007199254740993')
 const report=view.readDisplay(view.manifest.report,'report').document;expect(JSON.stringify(report)).not.toContain('repeatResultJson');expect(JSON.stringify(report)).not.toContain('9007199254740993')
})
