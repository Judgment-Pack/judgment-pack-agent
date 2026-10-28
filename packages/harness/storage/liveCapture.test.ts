import {CaptureCollector,sealedCapture} from '../history/captureCollector'
import {durableSession,durableOwner,childDurability} from './writer'
import '../testing/host'
import {expect,it,vi} from 'vitest'
import {MemoryRunStore} from './run'
import {vercel} from '../engines/vercel'
import {codex} from '../engines/codex'
import type {AssistantSession,AgentAssistantSession,AssistantEvent} from '../engine'
import {answerWith} from '../testing/scenarioDriver'
import {readRetainedRunHistory} from '../history/view'
import {readCapturedCorpusView,readArchiveCapturedCorpusView} from '../history/capture'
import {readRunArchive,exportRunArchive} from '../history/archive'
import {replayCorpus,type CorpusManifest,type CorpusFiles} from '../corpus'
const manifest:CorpusManifest={id:'original-live',packageVersion:'0.11.0',policy:{},files:{requests:'requests.json',toolResults:'tool-results.json',events:'events.json',checkpoint:'checkpoint.json'},expected:{file:'expected.json'},tolerance:[]}
const config=(store:MemoryRunStore,sink:{write:(id:string,files:CorpusFiles)=>void|Promise<void>},resume=false,native=false)=>({critic:'off' as const,record:true as const,policy:{version:2 as const,routing:{mode:'rules' as const,tiers:native?['low']:['off']}},route:{route:'agent' as const,tier:native?'low':'off',skills:[],tools:native?['lookup']:[],operations:'all' as const,planning:'withheld' as const},corpus:{manifest,guard:{value:<T>(value:T)=>value},sink},context:{connection:{connectionId:'provider',connectionRevision:'one'},connectionRevisions:{provider:'one'},currentRequest:{message:'Read',input:'Fixed'},runStore:{id:'original-live',store,...(resume?{resume:true}: {})}}})
const consume=async(events:AsyncIterable<AssistantEvent>)=>{const out:AssistantEvent[]=[];for await(const event of events)out.push(event);return {events:out}}
const base=()=>({prompt:'Read one source.',testPrompt:'',allowConversation:true,critic:'off' as const,tools:[],hostTools:[],callTool:vi.fn(),signal:new AbortController().signal})
it('captures the actual original Vercel engine through claim/result/root ACKs and replays a fresh adapter',async()=>{
 const store=new MemoryRunStore(),sink={write:vi.fn()},send=vi.fn(async()=>answerWith({say:'Captured original answer.'},'stream',0)),input:AssistantSession={...base(),...config(store,sink),model:{family:'openai-compatible',model:'fixture',call:send}}
 const original=await consume(vercel.start(input))
 expect(original.events.filter(e=>e.type==='error')).toEqual([])
 expect(original.events.some(e=>e.type==='recorded'),JSON.stringify(original.events.filter(e=>e.type==='guardrail'))).toBe(true);expect(sink.write).toHaveBeenCalledTimes(1);expect(send).toHaveBeenCalledTimes(1)
 const view=readCapturedCorpusView(await readRetainedRunHistory(store,'original-live'));expect(view.entry.requests).toHaveLength(1);expect(view.entry.requests[0]!.response.body).toContain('Captured original answer.')
 const archived=await exportRunArchive(store,'original-live'),imported=readArchiveCapturedCorpusView(readRunArchive(archived.bytes,archived.digest))
 await replayCorpus(imported.entry,async server=>(await consume(vercel.start({...base(),model:{family:'openai-compatible',model:'fixture',call:(suffix,request)=>server.model(suffix,request)}}))).events)
})
it('captures original native callback/tool frames and replays the actual fresh Codex engine',async()=>{
 const store=new MemoryRunStore(),sink={write:vi.fn()},tool={name:'lookup',description:'Read one source.',effect:'read' as const,taxonomy:{operation:'read' as const,effect:'read' as const},inputSchema:{type:'object',properties:{}},execute:vi.fn(async()=>({content:[{type:'text' as const,text:'Value.'}]}))}
 const input:AgentAssistantSession={...base(),...config(store,sink,false,true),hostTools:[tool],agent:{model:'fixture',run:async(request,callbacks)=>{const declared=request.tools.find(t=>t.description.startsWith('lookup:'))!;await callbacks.tool({id:'lookup-call',name:declared.name,arguments:{}},callbacks.signal);await callbacks.event({type:'message',id:'final',text:'Native captured answer.',phase:'final'})}}}
 const original=await consume(codex.start(input))
 expect(original.events.filter(e=>e.type==='error')).toEqual([]);expect(original.events.some(e=>e.type==='recorded'),JSON.stringify(original.events.filter(e=>e.type==='guardrail'))).toBe(true);expect(sink.write).toHaveBeenCalledTimes(1)
 const view=readCapturedCorpusView(await readRetainedRunHistory(store,'original-live'));expect(view.entry.toolResults).toHaveLength(1);expect(JSON.parse(view.entry.requests[0]!.response.body).steps).toHaveLength(2)
 await replayCorpus(view.entry,async server=>(await consume(codex.start({...base(),hostTools:[{...tool,execute:args=>server.tool('lookup',args)}],agent:{model:'fixture',run:(request,callbacks)=>server.agent(request,callbacks)}}))).events)
})

it('repeat stop refuses the next actual model request before logical accounting changes',async()=>{
 const store=new MemoryRunStore(),sink={write:vi.fn()};let calls=0
 const input:AssistantSession={...base(),...config(store,sink),record:undefined,corpus:undefined,policy:{version:2,repeatTools:{mode:'on',stopAt:1}},hostTools:[{name:'lookup',description:'Read.',effect:'read',taxonomy:{operation:'read',effect:'read'},inputSchema:{type:'object',properties:{}},execute:async()=>({content:[{type:'text',text:'Value.'}]})}],model:{family:'openai-compatible',model:'fixture',call:async()=>{calls++;return answerWith({call:{tool:'lookup',arguments:{}}},'whole',calls)}}}
 await consume(vercel.start(input));const cp=JSON.parse(new TextDecoder().decode((await store.load('original-live'))!.checkpoint!))
 expect(calls).toBe(2);expect(cp.status).toBe('stopped');expect(cp.used,'blocked physical call must not consume a logical request').toBe(calls);expect(cp.usage.rows.filter((row:any)=>row.source==='model').reduce((n:number,row:any)=>n+row.requests,0),'blocked physical call must not increase model request usage').toBe(calls)
})

it('captures the actual original native engine with an empty routed tool offer',async()=>{
 const store=new MemoryRunStore(),sink={write:vi.fn()},configuration=config(store,sink,false,true);configuration.route.tools=[]
 const input:AgentAssistantSession={...base(),...configuration,agent:{model:'fixture',run:async(_request,callbacks)=>{await callbacks.event({type:'message',id:'final',text:'Native captured answer.',phase:'final'})}}}
 const original=await consume(codex.start(input));expect(original.events.filter(e=>e.type==='error')).toEqual([]);expect(original.events.some(e=>e.type==='recorded')).toBe(true);expect(sink.write).toHaveBeenCalledTimes(1)
 const view=readCapturedCorpusView(await readRetainedRunHistory(store,'original-live'));expect(view.entry.requests).toHaveLength(1);expect(view.entry.requests[0]!.response.headers['x-jp-native-tool-bindings']).toBe('{}')
 await replayCorpus(view.entry,async server=>(await consume(codex.start({...base(),agent:{model:'fixture',run:(request,callbacks)=>server.agent(request,callbacks)}}))).events)
})

it('captures default model configuration with explicit checkpoint projection and preserves retained snapshots',async()=>{
 const store=new MemoryRunStore(),sink={write:vi.fn()},configuration=config(store,sink)
 const input:AssistantSession={...base(),...configuration,policy:undefined,route:undefined,model:{family:'openai-compatible',model:'fixture',call:async()=>answerWith({say:'Default captured answer.'},'stream',0)}}
 const original=await consume(vercel.start(input));expect(original.events.filter(e=>e.type==='error')).toEqual([]);expect(sink.write).toHaveBeenCalledTimes(1)
 const retained=await readRetainedRunHistory(store,'original-live'),view=readCapturedCorpusView(retained);expect(view.entry.events.some(e=>e.type==='execution_checkpoint')).toBe(false)
 const stored=retained.commits.flatMap(row=>row.events.map(ref=>JSON.parse(new TextDecoder().decode(retained.readObject(ref)))));expect(stored.some(row=>row.event.type==='execution_checkpoint')).toBe(true)
 const archive=await exportRunArchive(store,'original-live'),imported=readRunArchive(archive.bytes,archive.digest);for(const commit of retained.commits)for(const ref of commit.events)expect(imported.readDisplay(ref,'event').document).toBeDefined()
})

it('orders actual concurrent child frames by admission and lets only the original root finalize',async()=>{
 const store=new MemoryRunStore(),sink={write:vi.fn()};let rootEntered!:()=>void,firstEntered!:()=>void,releaseRoot!:()=>void,releaseFirst!:()=>void
 const rootWaiting=new Promise<void>(r=>rootEntered=r),firstWaiting=new Promise<void>(r=>firstEntered=r),rootGate=new Promise<void>(r=>releaseRoot=r),firstGate=new Promise<void>(r=>releaseFirst=r)
 const input:AssistantSession={...base(),...config(store,sink),model:{family:'openai-compatible',model:'fixture',call:async()=>{rootEntered();await rootGate;return answerWith({say:'Root answer.'},'stream',0)}}},root=await durableSession(input),rootBinding=root.context![durableOwner]!
 let sealOrder:string[]=[];const originalSeal=CaptureCollector.prototype.seal,seal=vi.spyOn(CaptureCollector.prototype,'seal').mockImplementation(function(this:CaptureCollector,...args:Parameters<CaptureCollector['seal']>){const result=originalSeal.apply(this,args);sealOrder=sealedCapture(result).rows.map(row=>row.admission.identity.scope.execution);return result})
 const running=consume(vercel.start(root));await rootWaiting
 const child=(id:string,wait:boolean)=>childDurability({...base(),...config(store,sink),model:{family:'openai-compatible',model:'fixture',call:async()=>{if(wait){firstEntered();await firstGate}return answerWith({say:id+' answer.'},'stream',0)}}} as AssistantSession,root,id)
 const first=child('first-child',true),firstRun=consume(vercel.start(first));await firstWaiting
 const second=child('second-child',false);await consume(vercel.start(second))
 expect(sink.write).not.toHaveBeenCalled();expect((await store.readHistoryHead('original-live'))!.metadata).toBeUndefined()
 const childBinding=second.context![durableOwner]!;expect(await childBinding.writer.finishCapture(childBinding)).toBeUndefined();expect(await childBinding.writer.finishCapture({writer:rootBinding.writer,root:true,scope:{execution:'original-live',role:'author'}})).toBeUndefined()
 releaseFirst();await firstRun;expect(sink.write).not.toHaveBeenCalled();releaseRoot();const done=await running;expect(done.events.filter(e=>e.type==='error')).toEqual([]);expect(sealOrder).toEqual(['original-live','first-child','second-child']);seal.mockRestore();expect(sink.write).toHaveBeenCalledTimes(1)
 const view=readCapturedCorpusView(await readRetainedRunHistory(store,'original-live'));expect(view.entry.requests.map(row=>row.response.body.match(/(Root|first-child|second-child) answer/)?.[1])).toEqual(['Root','first-child','second-child'])
})
