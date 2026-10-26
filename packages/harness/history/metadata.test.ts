import {it,expect} from 'vitest'
import {MemoryRunStore} from '../storage/run'
import {StorageConflict} from '../storage/common'
import {historyJSONBytes,historyJSONDigest,HistoryInputInvalid,readCanonicalHistoryJSON} from './codec'
import {readRunFeedback,readHistoryMetadata,readHistoryObservation,readRunCapture} from './metadata'
import type {RunFeedback} from './types'
const feedback=(extra:Partial<RunFeedback>={}):RunFeedback=>({version:1,id:'f',runId:'r',sequence:1,previous:null,target:{kind:'run'},rating:'bad',remark:'',actor:'host-user',recordedAt:'2026-10-25T01:02:03.000Z',...extra})
it('atomically appends feedback metadata without changing execution bytes, revision, cursor or event order',async()=>{
 const store=new MemoryRunStore(),first=await store.commit({runId:'r',expectedRevision:null,commitId:'one',checkpoint:historyJSONBytes({value:false}),events:[historyJSONBytes({version:1,scope:{execution:'r',role:'author'},event:{version:1,type:'message',text:'Done'}})]}),head=(await store.readHistoryHead('r'))!,before=await store.load('r'),events=await store.readEvents('r',null,100)
 const input={runId:'r',expectedHistoryRevision:head.revision,expectedMetadataRevision:null,commitId:'feedback-one',record:{version:1 as const,kind:'feedback' as const,feedback:feedback()}},written=await store.commitHistoryMetadata(input)
 expect(written.executionRevision).toBe(first.revision);expect(written.revision).not.toBe(head.revision);expect(written.commitCount).toBe(head.commitCount);expect(written.eventCount).toBe(head.eventCount);expect(written.root).toBe(head.root);expect(await store.load('r')).toEqual(before);expect(await store.readEvents('r',null,100)).toEqual(events)
 expect(await store.commitHistoryMetadata(input)).toEqual(written);expect((await store.readHistoryMetadataPage('r',head.revision,0,100)).items).toEqual([])
 const rows=await store.readHistoryMetadataPage('r',written.revision,0,100);expect(rows.total).toBe(1);expect(rows.items[0]!.record).toEqual(input.record);expect(rows.items[0]!.sequence).toBe(1);expect(rows.items[0]!.previousDigest).toBeNull()
})
it('refuses stale composite/metadata CAS and mismatched feedback chains without any partial publication',async()=>{
 const store=new MemoryRunStore();await store.commit({runId:'r',expectedRevision:null,commitId:'one',checkpoint:historyJSONBytes({test:1}),events:[]});const head=(await store.readHistoryHead('r'))!,first=feedback(),initial={runId:'r',expectedHistoryRevision:head.revision,expectedMetadataRevision:null,commitId:'feedback-one',record:{version:1 as const,kind:'feedback' as const,feedback:first}},saved=await store.commitHistoryMetadata(initial)
 await expect(store.commitHistoryMetadata({...initial,commitId:'competing'})).rejects.toThrow(StorageConflict)
 await expect(store.commitHistoryMetadata({...initial,expectedMetadataRevision:saved.metadata!.revision,commitId:'metadata-only-stale-head',record:{...initial.record,feedback:feedback({id:'other'})}})).rejects.toThrow(StorageConflict)
 await expect(store.commitHistoryMetadata({...initial,expectedHistoryRevision:saved.revision,commitId:'wrong-metadata-revision'})).rejects.toThrow(StorageConflict)
 const next={...initial,expectedHistoryRevision:saved.revision,expectedMetadataRevision:saved.metadata!.revision,commitId:'feedback-two',record:{...initial.record,feedback:feedback({sequence:2,previous:historyJSONDigest(first),rating:'good',remark:'Changed after review'})}},second=await store.commitHistoryMetadata(next)
 expect((await store.readHistoryMetadataPage('r',second.revision,0,100)).items.map(row=>row.record.kind==='feedback'&&row.record.feedback.rating)).toEqual(['bad','good'])
 for(const [index,edit] of [feedback({runId:'unrelated'}),feedback({sequence:3,previous:historyJSONDigest(next.record.feedback),target:{kind:'assistant-message',eventSequence:1,eventDigest:'sha256:'+'a'.repeat(64)}}),feedback({sequence:3,previous:'sha256:'+'a'.repeat(64)})].entries())await expect(store.commitHistoryMetadata({...next,expectedHistoryRevision:second.revision,expectedMetadataRevision:second.metadata!.revision,commitId:'bad-'+index,record:{...next.record,feedback:edit}})).rejects.toThrow(HistoryInputInvalid)
 expect((await store.readHistoryHead('r'))!.revision).toBe(second.revision);expect((await store.load('r'))!.revision).toBe(head.executionRevision)
})
it('binds message feedback to exact retained assistant event sequence and digest',async()=>{
 const store=new MemoryRunStore();await store.commit({runId:'r',expectedRevision:null,commitId:'one',checkpoint:historyJSONBytes({test:1}),events:[historyJSONBytes({version:1,scope:{execution:'r',role:'author'},event:{version:1,type:'message',text:'Done'}}),historyJSONBytes({version:1,scope:{execution:'r',role:'author'},event:{version:1,type:'end'}})]});const head=(await store.readHistoryHead('r'))!,page=await store.readHistoryPage('r',head.revision,0,100),event=page.items[0]!.events[0]!,record={version:1 as const,kind:'feedback' as const,feedback:feedback({target:{kind:'assistant-message',eventSequence:1,eventDigest:event.digest}})},saved=await store.commitHistoryMetadata({runId:'r',expectedHistoryRevision:head.revision,expectedMetadataRevision:null,commitId:'message-feedback',record})
 expect(saved.metadata!.count).toBe(1)
 const end=page.items[0]!.events[1]!,bad={...record,feedback:feedback({id:'other',target:{kind:'assistant-message',eventSequence:2,eventDigest:end.digest}})}
 await expect(store.commitHistoryMetadata({runId:'r',expectedHistoryRevision:saved.revision,expectedMetadataRevision:saved.metadata!.revision,commitId:'wrong-kind',record:bad})).rejects.toThrow(HistoryInputInvalid)
})
it('reads closed bounded sidecars and refuses unknown, noncanonical, lossy and unsupported forms',()=>{
 const row=feedback();expect(readRunFeedback(historyJSONBytes(row))).toEqual(row);expect(readHistoryMetadata(historyJSONBytes({version:1,kind:'feedback',feedback:row}))).toEqual({version:1,kind:'feedback',feedback:row})
 for(const value of [{...row,unexpected:true},{...row,version:2},{...row,recordedAt:'2026-02-30T01:02:03.000Z'},{...row,sequence:0},{...row,target:{kind:'assistant-message',eventSequence:1,eventDigest:'unbound'}}])expect(()=>readRunFeedback(historyJSONBytes(value))).toThrow(HistoryInputInvalid)
 expect(()=>readCanonicalHistoryJSON(new TextEncoder().encode('{"n":9007199254740993}'),100,'finite')).toThrow(HistoryInputInvalid);expect(readCanonicalHistoryJSON(historyJSONBytes({n:0.1}),100,'finite')).toEqual({n:0.1})
 expect(()=>readHistoryObservation(historyJSONBytes({version:1,kind:'relation',scope:{execution:'r',role:'author'},dispatchId:'d',target:{kind:'job',id:'j'},evidence:{digest:'sha256:'+'a'.repeat(64),bytes:1},unknown:1}))).toThrow(HistoryInputInvalid)
 expect(()=>readRunCapture(historyJSONBytes({version:2}))).toThrow(HistoryInputInvalid)
})
