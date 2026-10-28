import {it,expect,vi} from 'vitest'
import {MemoryRunStore} from './run'
import {durableSession} from './writer'
import {StorageConflict} from './common'
import type {CorpusManifest} from '../corpus'
import {vercel} from '../engines/vercel'
import {codex} from '../engines/codex'
import type {AgentAssistantSession} from '../engine'
import {answerWith} from '../testing/scenarioDriver'
import type {AssistantSession,AssistantEvent} from '../engine'
const manifest:CorpusManifest={id:'live-lifecycle',packageVersion:'0.11.0',policy:{},files:{requests:'requests.json',toolResults:'tool-results.json',events:'events.json',checkpoint:'checkpoint.json'},expected:{file:'expected.json'},tolerance:[]}
const consume=async(stream:AsyncIterable<AssistantEvent>)=>{const events:AssistantEvent[]=[];for await(const event of stream)events.push(event);return events}
function input(store:MemoryRunStore,sink:{write:()=>void|Promise<void>},send:()=>Promise<Response>):AssistantSession{return {prompt:'Read.',testPrompt:'',allowConversation:true,critic:'off',tools:[],hostTools:[],callTool:vi.fn(),signal:new AbortController().signal,model:{family:'openai-compatible',model:'fixture',call:send},record:true,policy:{version:2,routing:{mode:'rules',tiers:['off']}},route:{route:'agent',tier:'off',tools:[],skills:[],operations:'all',planning:'withheld'},corpus:{manifest,guard:{value:<T>(v:T)=>v},sink},context:{runStore:{store,id:'live-lifecycle'}}}}
for(const point of ['claim-before','claim-after','result-before','result-after','end-before','end-after','metadata-CAS','metadata-lost','sink-error'] as const)it(`original engine never delivers unacknowledged capture at ${point}`,async()=>{
 const store=new MemoryRunStore(),send=vi.fn(async()=>answerWith({say:'Done.'},'stream',0)),sink={write:vi.fn(async()=>{if(point==='sink-error')throw Error('fixture sink unavailable')})},commit=store.commitHistory.bind(store),metadata=store.commitHistoryMetadata.bind(store);let fired=false,metadataCalls=0
 store.commitHistory=async(transaction,signal)=>{const checkpoint=transaction.run.checkpoint?JSON.parse(new TextDecoder().decode(transaction.run.checkpoint)):undefined,dispatch=checkpoint?.durable?.dispatches?.find((row:any)=>row.kind==='model'),isEnd=transaction.run.events.some(bytes=>JSON.parse(new TextDecoder().decode(bytes)).event.type==='end'),hit=!fired&&(point.startsWith('claim')&&dispatch?.state==='claimed'||point.startsWith('result')&&dispatch?.state==='result'||point.startsWith('end')&&isEnd);if(hit){fired=true;if(point.endsWith('before'))throw Error('fixture before ACK')};const receipt=await commit(transaction,signal);if(hit)throw Error('fixture lost ACK');return receipt}
 store.commitHistoryMetadata=async(transaction,signal)=>{metadataCalls++;if(point==='metadata-CAS'){fired=true;const head=(await store.readHistoryHead(transaction.runId))!;await metadata({runId:transaction.runId,expectedHistoryRevision:head.revision,expectedMetadataRevision:null,commitId:'fixture-rating-race',record:{version:1,kind:'feedback',feedback:{version:1,id:'rating',runId:transaction.runId,sequence:1,previous:null,target:{kind:'run'},rating:'good',actor:'fixture',recordedAt:'2026-10-26T00:00:00.000Z'}}},signal);try{return await metadata(transaction,signal)}catch(error){expect(error).toBeInstanceOf(StorageConflict);throw error}};const receipt=await metadata(transaction,signal);if(point==='metadata-lost'){fired=true;throw Error('fixture metadata lost ACK')};return receipt}
 const events=await consume(vercel.start(input(store,sink,send)));expect(events.some(e=>e.type==='recorded')).toBe(false);expect(send).toHaveBeenCalledTimes(point.startsWith('claim')?0:1);expect(sink.write).toHaveBeenCalledTimes(point==='sink-error'?1:0);expect(metadataCalls).toBe(point.startsWith('metadata')||point==='sink-error'?1:0);expect((await store.readHistoryHead('live-lifecycle'))?.metadata?.count??0).toBe(point==='metadata-CAS'||point==='metadata-lost'||point==='sink-error'?1:0);if(point!=='sink-error')expect(fired,'target transaction reached').toBe(true);if(point==='metadata-CAS'){const head=(await store.readHistoryHead('live-lifecycle'))!;expect((await store.readHistoryMetadataPage('live-lifecycle',head.revision,0,10)).items.map(row=>row.record.kind)).toEqual(['feedback'])}
})
it('resumed history without original response frames stays unavailable',async()=>{
 const store=new MemoryRunStore(),sink={write:vi.fn()},send=vi.fn(async()=>answerWith({say:'Done.'},'stream',0)),first=input(store,sink,send);first.record=undefined;first.corpus=undefined
 await consume(vercel.start(first));expect(send).toHaveBeenCalledTimes(1)
 const resumed=input(store,sink,send);resumed.context!.runStore!.resume=true;await consume(vercel.start(resumed));expect(sink.write).not.toHaveBeenCalled();expect((await store.readHistoryHead('live-lifecycle'))!.metadata).toBeUndefined()
})

function nativeInput(store:MemoryRunStore,sink:{write:()=>void|Promise<void>},run:AgentAssistantSession['agent']['run']):AgentAssistantSession{const {model:_model,...base}=input(store,sink,async()=>answerWith({say:'Done.'},'stream',0));return {...base,policy:{version:2,routing:{mode:'rules',tiers:['low']}},route:{route:'agent',tier:'low',tools:[],skills:[],operations:'all',planning:'withheld'},agent:{model:'fixture',run}}}
it('native provider release acknowledgement cannot publish before callback/result/root receipts',async()=>{
 const store=new MemoryRunStore(),sink={write:vi.fn()};let entered!:()=>void,release!:()=>void
 const ready=new Promise<void>(r=>entered=r),gate=new Promise<void>(r=>release=r),running=consume(codex.start(nativeInput(store,sink,async(_request,callbacks)=>{expect(callbacks.providerRelease).toBeDefined();expect(callbacks.providerRelease!.acknowledge({})).toBe(false);expect(callbacks.providerRelease!.acknowledge(callbacks.providerRelease!.invocation)).toBe(true);await callbacks.event({type:'message',id:'final',text:'Done.',phase:'final'});entered();await gate})))
 await ready;expect(sink.write).not.toHaveBeenCalled();expect((await store.readHistoryHead('live-lifecycle'))!.metadata).toBeUndefined();release();const events=await running;expect(events.filter(e=>e.type==='error')).toEqual([]);expect(sink.write).toHaveBeenCalledTimes(1)
})
it('native retry after lost original claim ACK keeps exact request bytes without inventing old capture frames',async()=>{
 const store=new MemoryRunStore(),sink={write:vi.fn()},requests:string[]=[],commit=store.commitHistory.bind(store);let original='',armed=true
 store.commitHistory=async(transaction,signal)=>{const receipt=await commit(transaction,signal),checkpoint=transaction.run.checkpoint?JSON.parse(new TextDecoder().decode(transaction.run.checkpoint)):undefined,claim=checkpoint?.durable?.dispatches?.find((row:any)=>row.kind==='agent'&&row.state==='claimed');if(armed&&claim){armed=false;original=claim.request;throw Error('fixture lost original claim ACK')};return receipt}
 const run:AgentAssistantSession['agent']['run']=async(request,callbacks)=>{requests.push(JSON.stringify(request));await callbacks.event({type:'message',id:'final',text:'Done.',phase:'final'});callbacks.providerRelease?.acknowledge(callbacks.providerRelease.invocation)}
 await consume(codex.start(nativeInput(store,sink,run)));expect(requests).toEqual([]);expect(original).not.toBe('');const resumed=nativeInput(store,sink,run);resumed.context!.runStore!.resume=true;await consume(codex.start(resumed));expect(requests).toEqual([original]);expect(sink.write).not.toHaveBeenCalled();expect((await store.readHistoryHead('live-lifecycle'))!.metadata).toBeUndefined()
})
it('waits for an already admitted native tool callback after the provider returns',async()=>{
 const store=new MemoryRunStore(),sink={write:vi.fn()};let entered!:()=>void,release!:()=>void,providerReturned!:()=>void
 const returned=new Promise<void>(r=>providerReturned=r),ready=new Promise<void>(r=>entered=r),gate=new Promise<void>(r=>release=r),session=nativeInput(store,sink,async(request,callbacks)=>{const work=callbacks.tool({id:'lookup-call',name:request.tools[0]!.name,arguments:{}},callbacks.signal);void work.catch(()=>{});await ready;await callbacks.event({type:'message',id:'final',text:'Done.',phase:'final'});providerReturned()})
 session.route!.tools=['lookup'];session.hostTools=[{name:'lookup',description:'Read.',effect:'read',taxonomy:{operation:'read',effect:'read'},inputSchema:{type:'object',properties:{}},execute:async()=>{entered();await gate;return {content:[{type:'text',text:'Value.'}]}}}]
 const running=consume(codex.start(session));await returned;expect(sink.write).not.toHaveBeenCalled();expect((await store.readHistoryHead('live-lifecycle'))!.metadata).toBeUndefined();release();const events=await running;expect(events.filter(e=>e.type==='error')).toEqual([]);expect(sink.write).toHaveBeenCalledTimes(1)
})

for(const fault of ['getter','nonfinite'] as const)it(`validates original capture configuration before dispatch without executing ${fault}`,async()=>{
 const store=new MemoryRunStore(),sink={write:vi.fn()},send=vi.fn(async()=>answerWith({say:'Done.'},'stream',0)),session=input(store,sink,send),getter=vi.fn(()=> 'live-lifecycle');session.corpus!.manifest=structuredClone(session.corpus!.manifest)
 if(fault==='getter')Object.defineProperty(session.corpus!.manifest,'id',{enumerable:true,get:getter});else session.corpus!.manifest.policy={value:Infinity}
 await consume(vercel.start(session));expect(getter).not.toHaveBeenCalled();expect(send).not.toHaveBeenCalled();expect(sink.write).not.toHaveBeenCalled();expect((await store.readHistoryHead('live-lifecycle'))?.metadata).toBeUndefined()
})
it('keeps the original enrolled manifest detached from later host mutation',async()=>{
 const store=new MemoryRunStore(),sink={write:vi.fn()},session=input(store,sink,async()=>answerWith({say:'Done.'},'stream',0));session.corpus!.manifest=structuredClone(session.corpus!.manifest);const enrolled=await durableSession(session);session.corpus!.manifest.id='mutated-after-enrollment';await consume(vercel.start(enrolled));expect(sink.write).toHaveBeenCalledTimes(1);expect(sink.write.mock.calls[0]?.[0]).toBe('live-lifecycle')
})

for(const point of ['before-response','after-response'] as const)it(`does not publish an interrupted original model at ${point}`,async()=>{
 const controller=new AbortController(),store=new MemoryRunStore(),sink={write:vi.fn()},send=vi.fn(async()=>{if(point==='before-response'){controller.abort();throw controller.signal.reason}const response=answerWith({say:'Done.'},'stream',0);controller.abort();return response}),session=input(store,sink,send);session.signal=controller.signal
 await consume(vercel.start(session));expect(send).toHaveBeenCalledTimes(1);expect(sink.write).not.toHaveBeenCalled();expect((await store.readHistoryHead('live-lifecycle'))!.metadata).toBeUndefined()
})

it('keeps original callbacks running when capture cannot safely snapshot provider material',async()=>{
 const store=new MemoryRunStore(),sink={write:vi.fn()},events=await consume(codex.start(nativeInput(store,sink,async(_request,callbacks)=>{await callbacks.event(Object.assign({type:'message' as const,id:'final',text:'Done.',phase:'final' as const},{unserializable:()=>{}}))})))
 expect(events.filter(e=>e.type==='error')).toEqual([]);expect(events.some(e=>e.type==='message'&&e.text==='Done.')).toBe(true);expect(sink.write).not.toHaveBeenCalled();expect((await store.readHistoryHead('live-lifecycle'))!.metadata).toBeUndefined()
})
