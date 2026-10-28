import {readFileSync} from 'node:fs'
import {resolve} from 'node:path'
import {expect,it} from 'vitest'
import taskIdentity from '../../../contract/fixtures/history/task-identity.json'
import {MemoryRunStore} from '../storage/run'
import {CaptureCollector,type CaptureAdmission} from '../history/captureCollector'
import {prepareCapturePublication,commitCapturePublication,readArchiveCapturedCorpusView} from '../history/capture'
import {historyJSONBytes,historyJSONDigest,historyObject,chunkHistoryValue} from '../history/codec'
import {exportRunArchive,readRunArchive} from '../history/archive'
import {canonicalJSON} from '../context/chat'
import {loadEngine} from '../engines'
import {replayCorpus,type CorpusEntry} from '../corpus'
import {runOnModelEngine} from '../testing/scenarioDriver'
import {normalize} from '../thinking'
import {TEST_CASES_TOOL} from '../testing/scripted'
/** Constructed native journal over handwritten seed transport frames; live writer hooks are separate. */
async function importedReplay(){
 const root=resolve(import.meta.dirname,'../../../corpus/model-recorded-tool-result'),read=(name:string)=>JSON.parse(readFileSync(resolve(root,name),'utf8')),manifest=read('manifest.json'),frames=read('requests.json') as CorpusEntry['requests'],events=(read('events.json') as CorpusEntry['events']).filter(e=>e.type!=='end')
 const cp:any=structuredClone(taskIdentity.checkpoint),scope={execution:cp.id,role:'author'},store=new MemoryRunStore(),owner={},collector=new CaptureCollector(owner,cp.id,scope),admissions:CaptureAdmission[]=[],observations:{digest:string;bytes:number}[]=[],objects:Awaited<ReturnType<typeof historyObject>>[]=[]
 collector.bindPublisher(owner,store);cp.durable={dispatches:[],children:[],observations:[]}
 for(const [i,frame]of frames.entries()){
  const supplied=frame.request as {method?:string;body:string},request=canonicalJSON({suffix:frame.suffix,method:supplied.method,body:supplied.body}),requestDigest=historyJSONDigest(JSON.parse(request)),dispatchId=`replay-${i}`,source='provider',tool='fixture',identity={scope,dispatchId,kind:'model' as const,source,tool,requestDigest,surface:'session-model' as const}
  cp.durable.dispatches.push({id:dispatchId,scope,kind:'model',source,tool,request,requestDigest,fingerprint:historyJSONDigest({kind:'model',scope,source,tool,request:JSON.parse(request)}),state:'result',read:true})
  const payload=await chunkHistoryValue(new TextEncoder().encode(request));objects.push(payload.manifest,...payload.objects)
  const observation=await historyObject(historyJSONBytes({version:1,kind:'request',scope,dispatchId,source,tool,request:{digest:payload.manifest.digest,bytes:payload.manifest.bytes},originals:[],compactions:[]})),exp=await historyObject(historyJSONBytes({version:1,kind:'capture-request',scope,dispatchId,source,tool,requestDigest,surface:'session-model',captureKind:'model'}))
  objects.push(observation,exp);observations.push({digest:observation.digest,bytes:observation.bytes},{digest:exp.digest,bytes:exp.bytes});admissions.push({identity,sequence:1,ordinal:observations.length-1,commitId:'constructed',expectation:{digest:exp.digest,bytes:exp.bytes},request})
 }
 await store.stageHistoryObjects({kind:'run',id:cp.id},objects)
 await store.commitHistory({run:{runId:cp.id,expectedRevision:null,commitId:'constructed',checkpoint:historyJSONBytes(cp),events:events.map(event=>historyJSONBytes({version:1,scope,event}))},observations})
 for(const [i,a]of admissions.entries()){const token=collector.admit(owner,a);collector.record(owner,token,frames[i]);collector.acknowledge(owner,token)}
 const head=(await store.readHistoryHead(cp.id))!,seal=collector.seal(owner,collector.rootToken(owner),{runId:cp.id,executionRevision:head.executionRevision,checkpoint:head.checkpoint!,status:'complete'}),members={outputPatterns:{value:[{name:'prose',pattern:'First secret prose'}]}} as never
 const prepared=await prepareCapturePublication(store,seal,{commitId:'capture',manifest,members});await commitCapturePublication(store,prepared)
 const archive=await exportRunArchive(store,cp.id,{members}),imported=readRunArchive(archive.bytes,archive.digest);expect(Array.from(imported.canonicalBytes())).toEqual(Array.from(archive.bytes))
 return readArchiveCapturedCorpusView(imported)
}
it('replays an imported derived capture through a fresh actual model engine with exhausted recordings and new outcomes',async()=>{
 const view=await importedReplay();expect(view.checkpoint.representation).toBe('derived');expect(view.entry.checkpoint).toEqual(view.checkpoint)
 const engine=await loadEngine('vercel');let invoked=0,newTools=0
 const prior=TEST_CASES_TOOL._meta;TEST_CASES_TOOL._meta={operation:'check',effect:'pure',checkKind:'test',experimental:true}
 try{
 const run=async(entry:CorpusEntry)=>replayCorpus(entry,async server=>{invoked++;const result=await runOnModelEngine(request=>server.model('chat/completions',{body:JSON.stringify(request.body)}),{engine,tools:[{name:'get_schema',description:'Read the recorded schema.',inputSchema:{type:'object',properties:{},additionalProperties:false},_meta:{operation:'read',effect:'read'}}],handle:call=>{newTools++;return server.tool(call.name,call.args)},session:{critic:'off',thinking:normalize('off','openai-compatible')}});expect(result.events.some(e=>e.type==='tool_result')).toBe(true);return result.events})
 await run(view.entry);expect(invoked).toBe(1);expect(newTools).toBe(1)
 const wrong=structuredClone(view.entry);wrong.expected.outcomes=[];await expect(run(wrong)).rejects.toThrow('/outcomes/length differs')
 const extra=structuredClone(view.entry);extra.requests.push(extra.requests[0]!);await expect(run(extra)).rejects.toThrow(/request/)
 }finally{if(prior===undefined)delete TEST_CASES_TOOL._meta;else TEST_CASES_TOOL._meta=prior}
})
