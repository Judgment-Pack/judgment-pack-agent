import {it,expect,vi} from 'vitest'
import {MemoryRunStore} from '../storage/run'
import {managedContext} from '../context/managed'
import type {AssistantSession} from '../engine'
import {historyJSONDigest,HistoryInputInvalid,HistoryChanged} from './codec'
import {readRetainedRunHistory} from './view'
import {queryHistoryEvents} from './query'
import {exportRunArchive,readRunArchive} from './archive'
import {projectHistoryRunReport} from './report'
import {prepareRunFeedback,commitPreparedRunFeedback,writeRunFeedback,FeedbackOutcomeUnknown,type RunFeedbackInput} from './feedback'
import {canonicalJSON} from '../context/chat'
const members={outputPatterns:{value:[{name:'private-note',pattern:'SECRET-[0-9]+'}]}} as never
async function completed(){const store=new MemoryRunStore(),session:AssistantSession={prompt:'Work',testPrompt:'',tools:[],hostTools:[],callTool:vi.fn(async()=>({content:[]})),model:{family:'openai-compatible',model:'fixture',call:vi.fn(async()=>{throw Error('No provider')})},signal:new AbortController().signal,context:{currentRequest:{message:'Work',input:'Work'},runStore:{store,id:'r'}}};for await(const _ of managedContext(session,async function*(){yield {type:'message',text:'Answer'};yield {type:'end'}})){}return store}
async function input(store:MemoryRunStore,override:Partial<RunFeedbackInput>={}):Promise<RunFeedbackInput>{const head=(await store.readHistoryHead('r'))!;return {runId:'r',id:'rating',commitId:'feedback-1',expectedHistoryRevision:head.revision,expectedMetadataRevision:head.metadata?.revision??null,previous:null,target:{kind:'run'},rating:'good',actor:'user',recordedAt:'2026-10-25T01:02:03.000Z',...override}}
it('preserves rating edits, absent/empty remarks, report versions and every execution byte',async()=>{
 const store=await completed(),before=await store.load('r'),events=await store.readEvents('r',null,100),old=projectHistoryRunReport(await readRetainedRunHistory(store,'r'));expect(old.version).toBe(1)
 const first=await writeRunFeedback(store,await input(store));expect(first.delivery).toBe('not-requested');expect(first.candidate).toBeUndefined();let view=await readRetainedRunHistory(store,'r'),feedback=view.metadata[0]!.record;expect(feedback.kind).toBe('feedback');if(feedback.kind!=='feedback')throw Error('fixture')
 expect(Object.hasOwn(feedback.feedback,'remark')).toBe(false);const previous=historyJSONDigest(feedback.feedback),edit=await input(store,{commitId:'feedback-2',previous,rating:'bad',remark:'',actor:'second-user',recordedAt:'2026-10-25T02:02:03.000Z'})
 const prepared=await prepareRunFeedback(store,edit,{members}),result=await commitPreparedRunFeedback(store,prepared),replay=await commitPreparedRunFeedback(store,prepared);expect(replay.head).toEqual(result.head);expect(result.candidate?.status).toBe('pending-review');expect(result.candidate?.replay).toEqual({kind:'unavailable',reason:'not-recorded'})
 view=await readRetainedRunHistory(store,'r');expect(view.metadata).toHaveLength(2);expect(view.metadata[0]!.record).toEqual(feedback);expect(view.metadata[1]!.record).toMatchObject({feedback:{sequence:2,previous,remark:'',actor:'second-user'}})
 const report=projectHistoryRunReport(view);expect(report).toMatchObject({version:2,checkpointVersion:2});expect('usageTotals'in report).toBe(false);expect('route'in report).toBe(false);expect((report as any).feedback).toHaveLength(2)
 const exported=await exportRunArchive(store,'r',{members}),imported=readRunArchive(exported.bytes);expect(imported.readDisplay(imported.manifest.report).document).toEqual(report);expect(await store.load('r')).toEqual(before);expect(await store.readEvents('r',null,100)).toEqual(events)
})
it('persists a redacted pending candidate when delivery fails and preserves source hashes across redacted edits',async()=>{
 const store=await completed(),sink={writeCandidate:vi.fn(async()=>{throw Error('offline')})},first=await writeRunFeedback(store,await input(store,{rating:'bad',remark:'SECRET-123'}),{members,sink});expect(first.delivery).toBe('unknown');expect(sink.writeCandidate).toHaveBeenCalledTimes(1);expect(canonicalJSON(first.candidate)).not.toContain('SECRET-123');expect(first.candidate?.feedback.remark).toBe('[redacted: private-note]')
 const view=await readRetainedRunHistory(store,'r'),old=view.metadata[0]!.record;if(old.kind!=='feedback')throw Error('fixture');const original=historyJSONDigest(old.feedback)
 await writeRunFeedback(store,await input(store,{commitId:'feedback-2',previous:original,rating:'good',remark:'updated'}),{members});const exported=await exportRunArchive(store,'r',{members});expect(new TextDecoder().decode(exported.bytes)).not.toContain('SECRET-123');const imported=readRunArchive(exported.bytes),report:any=imported.readDisplay(imported.manifest.report).document
 expect(report.feedback[0].sourceDigest).toBe(original);expect(report.feedback[1].record.previous).toBe(original);expect(historyJSONDigest(report.feedback[0].record)).not.toBe(original);expect(report.feedback[0].record.remark).toBe('[redacted: private-note]')
})
it('refuses stale heads, changed targets and wrong message identity before metadata publication',async()=>{
 const store=await completed(),start=await input(store);await writeRunFeedback(store,start);await expect(prepareRunFeedback(store,start)).rejects.toThrow(HistoryChanged)
 const view=await readRetainedRunHistory(store,'r'),row=view.metadata[0]!.record;if(row.kind!=='feedback')throw Error('fixture');const previous=historyJSONDigest(row.feedback),message=queryHistoryEvents(view,{types:['message']}).items[0]!
 for(const override of [{previous:null},{previous,target:{kind:'assistant-message',eventSequence:message.sequence,eventDigest:message.source.digest}},{id:'message',previous:null,target:{kind:'assistant-message',eventSequence:message.sequence,eventDigest:'sha256:'+'0'.repeat(64)}}])await expect(prepareRunFeedback(store,await input(store,{commitId:'bad',...override} as Partial<RunFeedbackInput>))).rejects.toThrow(HistoryInputInvalid)
 expect((await store.readHistoryHead('r'))!.metadata!.count).toBe(1)
})
it('refuses false acknowledgements before candidate delivery and never retries execution',async()=>{
 const store=await completed(),before=await store.load('r'),prepared=await prepareRunFeedback(store,await input(store,{rating:'bad'}),{members}),commit=store.commitHistoryMetadata.bind(store);let calls=0
 store.commitHistoryMetadata=async(...args)=>{calls++;const head=await commit(...args);head.executionRevision='forged';return head}
 await expect(commitPreparedRunFeedback(store,prepared)).rejects.toThrow(FeedbackOutcomeUnknown);expect(calls).toBe(1);expect(await store.load('r')).toEqual(before);expect((await store.readHistoryHead('r'))!.metadata!.count).toBe(1)
})

import feedbackVectors from '../../../contract/fixtures/feedback/records.json'
import {readRunReport} from '../runReportReader'
import {readRunFeedback} from './metadata'
import {projectRunReport} from '../runReport'
import {readExecution} from '../execution/state'
import {historyJSONBytes,historyBytesDigest,historyObject} from './codec'
it('reads paired handwritten feedback and checkpoint1/2/3 report fixtures without relabeling source versions',async()=>{
 for(const row of feedbackVectors.records)expect(readRunFeedback(historyJSONBytes(row))).toEqual(row)
 for(const row of feedbackVectors.reports){expect(readRunReport(row.report)).toEqual(row.report);const store=new MemoryRunStore();await store.commit({runId:'r',expectedRevision:null,commitId:'source',checkpoint:historyJSONBytes(row.checkpoint),events:[]});const before=await readRetainedRunHistory(store,'r');expect(projectHistoryRunReport(before)).toEqual(projectRunReport(readExecution(row.checkpoint)!));await writeRunFeedback(store,await input(store,{actor:row.feedback.actor,recordedAt:row.feedback.recordedAt}));const view=await readRetainedRunHistory(store,'r');expect(projectHistoryRunReport(view)).toEqual(row.report);const exported=await exportRunArchive(store,'r');expect(readRunArchive(exported.bytes).readDisplay(exported.manifest.report).document).toEqual(row.report)}
 for(const row of feedbackVectors.invalidReports)expect(()=>readRunReport(row.report),row.name).toThrow()
})
function rewriteReport(bytes:Uint8Array,change:(report:any)=>void){const a:any=JSON.parse(new TextDecoder().decode(bytes)),mapping=a.manifest.mappings.find((row:any)=>row.kind==='report'),old=mapping.exported.digest,blob=a.objects.find((row:any)=>row.digest===old),report=JSON.parse(Buffer.from(blob.bytes,'base64').toString('utf8'));change(report);const value=historyJSONBytes(report),ref={digest:historyBytesDigest(value),bytes:value.length};mapping.source=ref;mapping.exported=ref;a.manifest.report=ref;Object.assign(a.manifest.members.find((row:any)=>row.digest===old),ref,{source:ref});blob.digest=ref.digest;blob.bytes=Buffer.from(value).toString('base64');a.manifest.mappings.sort((x:any,y:any)=>(x.source.digest+'|'+x.kind).localeCompare(y.source.digest+'|'+y.kind));a.manifest.members.sort((x:any,y:any)=>(x.source.digest+'|'+x.kind).localeCompare(y.source.digest+'|'+y.kind));a.objects.sort((x:any,y:any)=>x.digest.localeCompare(y.digest));const {digest:_,...body}=a.manifest;a.manifest.digest=historyJSONDigest(body);return historyJSONBytes(a)}
it('rejects self-consistent source-version, usage-total and route mismatches in an archive report',async()=>{
 const row=feedbackVectors.reports[3]!,store=new MemoryRunStore();await store.commit({runId:'r',expectedRevision:null,commitId:'source',checkpoint:historyJSONBytes(row.checkpoint),events:[]});await writeRunFeedback(store,await input(store));const {bytes}=await exportRunArchive(store,'r')
 for(const change of [(r:any)=>{r.checkpointVersion=2;delete r.usageTotals;delete r.route},(r:any)=>{r.usageTotals.inputTokens.known=7},(r:any)=>{r.route.reason='different'}])expect(()=>readRunArchive(rewriteReport(bytes,change))).toThrow(HistoryInputInvalid)
})

import {durableSession,durableOwner} from '../storage/writer'
import {contextualSession} from '../context/session'
import {normalize} from '../thinking'
import type {ModelMessage} from 'ai'
it('keeps active feedback out of actual prompts before and after compaction and explicit resume',async()=>{
 const store=new MemoryRunStore(),make=(resume=false):AssistantSession=>({prompt:'Current request',testPrompt:'',tools:[],hostTools:[],callTool:async()=>({content:[]}),model:{family:'gemini',model:'small',call:async()=>{throw Error('No provider')}},thinking:normalize('off','gemini'),signal:new AbortController().signal,policy:{compaction:'index'},context:{runStore:{store,id:'r',resume},configuration:{version:1,models:{small:{contextWindowTokens:8192,outputTokens:1024}}}}})
 const open=async(resume=false)=>{const admitted=await durableSession(make(resume));return {...await contextualSession(admitted),binding:admitted.context![durableOwner]!}}
 let run=await open(),history:ModelMessage[]=[{role:'user',content:'Task'}]
 const before=await store.load('r'),first=await writeRunFeedback(store,await input(store,{rating:'bad',remark:'PRIVATE-FEEDBACK-ONLY'})),view=await readRetainedRunHistory(store,'r')
 expect(first.delivery).toBe('deferred');expect(first.candidate).toBeUndefined();expect(view.metadata[0]!.record).toMatchObject({candidateRequested:true});expect(await store.load('r')).toEqual(before)
 const request=await run.context.messages(history,'host',[]);expect(canonicalJSON(request)).not.toContain('PRIVATE-FEEDBACK-ONLY')
 history=[...history,{role:'assistant',content:[{type:'tool-call',toolCallId:'one',toolName:'check',input:{}}]},{role:'tool',content:[{type:'tool-result',toolCallId:'one',toolName:'check',output:{type:'json',value:{ok:true}}}]}]
 for(let i=0;i<16;i++)history.push({role:'assistant',content:'earlier public discussion '.repeat(140)},{role:'user',content:'continue'})
 history=await run.context.messages(history,'host',[]);expect(canonicalJSON(history)).toContain('Completed tool calls must not be repeated');expect(canonicalJSON(history)).not.toContain('PRIVATE-FEEDBACK-ONLY')
 await run.binding.writer.save(run.binding,run.context.execution.snapshot());run.context.close();run=await open(true)
 const resumed=await run.context.messages(JSON.parse(JSON.stringify(history)),'host',[]);expect(canonicalJSON(resumed)).not.toContain('PRIVATE-FEEDBACK-ONLY');expect((await readRetainedRunHistory(store,'r')).metadata).toHaveLength(1);run.context.close()
})
it('defers active candidates, cancels current intent on good edits, and requires an explicit terminal revision',async()=>{
 const store=new MemoryRunStore(),checkpoint=structuredClone(feedbackVectors.reports[1]!.checkpoint) as any;checkpoint.status='running';delete checkpoint.reason
 await store.commit({runId:'r',expectedRevision:null,commitId:'active',checkpoint:historyJSONBytes(checkpoint),events:[]})
 const deferred=await prepareRunFeedback(store,await input(store,{rating:'bad'})),first=await commitPreparedRunFeedback(store,deferred);expect(first.candidateDeferred).toBe(true);expect(first.candidate).toBeUndefined()
 let old=(await readRetainedRunHistory(store,'r')).metadata.at(-1)!.record;if(old.kind!=='feedback')throw Error('fixture')
 await writeRunFeedback(store,await input(store,{commitId:'cancel',previous:historyJSONDigest(old.feedback),rating:'good'}));let current=(await readRetainedRunHistory(store,'r')).metadata.at(-1)!.record;expect(Object.hasOwn(current,'candidateRequested')).toBe(false)
 const replay=await commitPreparedRunFeedback(store,deferred);expect(replay.superseded).toBe(true);expect(replay.candidate).toBeUndefined()
 checkpoint.status='complete';checkpoint.reason='Finished';const native=(await store.load('r'))!;await store.commit({runId:'r',expectedRevision:native.revision,commitId:'terminal',checkpoint:historyJSONBytes(checkpoint),events:[]})
 if(current.kind!=='feedback')throw Error('fixture');const terminal=await writeRunFeedback(store,await input(store,{commitId:'explicit-terminal',previous:historyJSONDigest(current.feedback),rating:'good'}),{proposeGood:true});expect(terminal.candidate?.status).toBe('pending-review');expect(terminal.delivery).toBe('pending')
 const exported=await exportRunArchive(store,'r'),imported=readRunArchive(exported.bytes);expect((imported.readDisplay(imported.manifest.metadata[0]!).document as any).record.candidateRequested).toBe(true)
})

import candidateVectors from '../../../contract/fixtures/feedback/candidates.json'
import {readCorpusCandidate} from '../corpusCandidate'
import {readHistoryMetadata} from './metadata'
import {readHistoryRelationEvidence} from './relations'
it('reads independent pending-candidate, deferred-intent and scoped-evidence vectors and rejects malformed candidates',()=>{
 for(const c of candidateVectors.candidates)expect(readCorpusCandidate(historyJSONBytes(c))).toEqual(c)
 for(const m of candidateVectors.metadata)expect(readHistoryMetadata(historyJSONBytes(m))).toEqual(m)
 for(const e of candidateVectors.relations)expect(readHistoryRelationEvidence(historyJSONBytes(e))).toEqual(e)
 for(const row of candidateVectors.invalidCandidates)expect(()=>readCorpusCandidate(historyJSONBytes(row.candidate)),row.name).toThrow(HistoryInputInvalid)
 for(const bad of [{},null,{...candidateVectors.relations[0],extra:true}])expect(()=>readHistoryRelationEvidence(historyJSONBytes(bad))).toThrow(HistoryInputInvalid)
})
it('keeps privately staged candidates invisible and refuses their attachment after a rating edit',async()=>{
 const store=await completed(),prepared=await prepareRunFeedback(store,await input(store,{rating:'bad'})),ref=prepared.commit.record.kind==='feedback'?prepared.commit.record.candidate:undefined
 expect(ref).toBeDefined();await expect(store.readHistoryObject({kind:'run',id:'r'},prepared.source.revision,ref!.digest,0,ref!.bytes)).rejects.toThrow()
 await writeRunFeedback(store,await input(store,{commitId:'newer'}));await expect(commitPreparedRunFeedback(store,prepared)).rejects.toThrow();const view=await readRetainedRunHistory(store,'r');expect(view.metadata).toHaveLength(1);expect(view.metadata[0]!.record).not.toHaveProperty('candidate')
})

it('refuses raw candidate publication for nonterminal sources or false report source labels',async()=>{
 for(const status of ['running','complete']){const store=new MemoryRunStore(),checkpoint=structuredClone(feedbackVectors.reports[1]!.checkpoint) as any;checkpoint.status=status;await store.commit({runId:'r',expectedRevision:null,commitId:'source',checkpoint:historyJSONBytes(checkpoint),events:[]});const head=(await store.readHistoryHead('r'))!,candidate=structuredClone(candidateVectors.candidates[0]) as any;candidate.sourceView=head.revision;if(status==='running')candidate.review.report.checkpointVersion=2;const object=await historyObject(historyJSONBytes(candidate));await store.stageHistoryObjects({kind:'run',id:'r'},[object]);const record:any={...candidateVectors.metadata[0],candidate:{digest:object.digest,bytes:object.bytes}}
 await expect(store.commitHistoryMetadata({runId:'r',expectedHistoryRevision:head.revision,expectedMetadataRevision:null,commitId:'raw-candidate',record})).rejects.toThrow(HistoryInputInvalid);expect((await store.readHistoryHead('r'))!.metadata).toBeUndefined()
 }
 const record:any=structuredClone(candidateVectors.metadata[2]);delete record.candidateRequested;expect(()=>readHistoryMetadata(historyJSONBytes(record))).toThrow(HistoryInputInvalid)
})

it('proposes an opted-in good rating for exactly one finalized assistant message',async()=>{
 const store=await completed(),view=await readRetainedRunHistory(store,'r'),row=queryHistoryEvents(view,{types:['message']}).items[0]!
 const result=await writeRunFeedback(store,await input(store,{target:{kind:'assistant-message',eventSequence:row.sequence,eventDigest:row.source.digest}}),{proposeGood:true})
 expect(result.candidate?.feedback.target).toEqual({kind:'assistant-message',eventSequence:row.sequence,eventDigest:row.source.digest});expect(result.candidate?.review.messages).toEqual([{sequence:row.sequence,text:'Answer'}]);expect(result.delivery).toBe('pending')
})

it('refuses otherwise valid archive report version labels for each native checkpoint version',async()=>{
 for(const row of feedbackVectors.reports.slice(0,3)){const store=new MemoryRunStore();await store.commit({runId:'r',expectedRevision:null,commitId:'source',checkpoint:historyJSONBytes(row.checkpoint),events:[]});await writeRunFeedback(store,await input(store));const exported=await exportRunArchive(store,'r');expect(()=>readRunArchive(rewriteReport(exported.bytes,(r:any)=>{r.checkpointVersion=r.checkpointVersion===1?2:1}))).toThrow(HistoryInputInvalid)}
})
