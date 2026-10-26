import {expect,it} from 'vitest'
import vectors from '../../../contract/fixtures/history/task-identity.json'
import {readExecution} from '../execution/state'
import {MemoryRunStore} from '../storage/run'
import {prepareHistoryOriginal,stageHistoryObservation} from './recording'
import {readRunArchive,exportRunArchive} from './archive'
import {historyJSONBytes,HistoryInputInvalid} from './codec'
for(const row of vectors.cases)it(`protects current task identity ${row.name}`,()=>{
 expect(readExecution(vectors.checkpoint)).toEqual(vectors.checkpoint)
 const bytes=historyJSONBytes(row.archive)
 if(row.valid)expect(Array.from(readRunArchive(bytes,row.expectedDigest).canonicalBytes())).toEqual(Array.from(bytes));else expect(()=>readRunArchive(bytes,row.expectedDigest)).toThrow(HistoryInputInvalid)
})
it('keeps permitted plan prose and unrelated source objects redactable',async()=>{
 const store=new MemoryRunStore(),id=vectors.checkpoint.id
 await store.commit({runId:id,expectedRevision:null,commitId:'task-source',checkpoint:historyJSONBytes(vectors.checkpoint),events:[]})
 const prepared=await prepareHistoryOriginal({execution:id,role:'author'},'user-context','{"dependsOn":["PRIVATE"],"planState":{"taskSchedule":{"run":"PRIVATE"},"taskEnvelope":{"run":"PRIVATE"}}}','context',0)
 await stageHistoryObservation(store,id,prepared);const head=(await store.load(id))!;await store.commitHistory({run:{runId:id,expectedRevision:head.revision,commitId:'user-original',checkpoint:head.checkpoint,events:[]},observations:[prepared.ref]})
 const result=await exportRunArchive(store,id,{members:{outputPatterns:{value:[{name:'prose',pattern:'First secret prose'},{name:'user',pattern:'PRIVATE'}]}} as never}),view=readRunArchive(result.bytes),checkpoint=view.readDisplay(view.head.checkpoint!,'checkpoint').document as typeof vectors.checkpoint
 expect(checkpoint.plan[0]!.step).toBe('[redacted: prose]');expect(checkpoint.plan[1]!.dependsOn).toEqual(['step-1']);expect(checkpoint.planState.taskSchedule).toEqual(vectors.checkpoint.planState.taskSchedule);expect(checkpoint.planState.taskEnvelope).toEqual(vectors.checkpoint.planState.taskEnvelope)
 const chunk=view.manifest.mappings.find(m=>m.kind==='source-chunk')!;expect(view.readDisplay(chunk.source,chunk.kind).document).toContain('[redacted: user]')
})
