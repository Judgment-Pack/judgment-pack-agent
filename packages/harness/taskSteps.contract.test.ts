import {it,expect} from 'vitest'
import vector from '../../contract/testdata/task-steps/records.json'
import nativeCheckpoints from '../../contract/vectors/plan/native-checkpoints.json'
import nativeEvents from '../../contract/vectors/plan/native-events.json'
import {readTaskCoordinatorBinding} from './taskSteps'
import {readExecution} from './execution/state'
import {writeRecord,CHECKPOINT_SCHEMA,RECORD_DEFAULTS} from './records'
import {writeRunEvent,readRunEvent} from './recordReader'
import {checkRecord} from './recordReader'
for(const row of vector.items)it(`#183 shared current record ${row.name}`,()=>{
 expect(readExecution(row.checkpoint)).toBeDefined()
 expect(()=>checkRecord('checkpoint',row.checkpoint)).not.toThrow()
 expect(()=>checkRecord('checkpoint',{...row.checkpoint,version:2})).toThrow()
})
for(const row of vector.invalid)it(`#183 shared malformed record ${row.name}`,()=>{
 const state=structuredClone(vector.items[row.base]!.checkpoint) as unknown as Record<string,unknown>
 let at:Record<string,unknown>=state;for(const key of row.path.slice(0,-1))at=at[String(key)] as Record<string,unknown>
 const key=String(row.path.at(-1));if('remove'in row)delete at[key];else at[key]=row.value
 expect(()=>readExecution(state)).toThrow()
})

for(const row of vector.events)it(`#183 shared current event ${row.name}`,()=>{expect(()=>checkRecord('event',row.event)).not.toThrow();if('nativeInvocation'in row.event)expect(()=>checkRecord('event',{...row.event,version:1})).toThrow()})

it('#219 rejects semantic coordinator bounds independently of structural schema',()=>{
 const pin={version:1,owner:'native-delegation',run:'run',execution:'run',scopeDigest:'sha256:'+'a'.repeat(64),limit:6,childReserve:2}
 expect(readTaskCoordinatorBinding(pin)).toEqual(pin)
 for(const changed of [{...pin,childReserve:7},{...pin,childReserve:0},{...pin,limit:0},{...pin,owner:'host'},{...pin,extra:true}])expect(()=>readTaskCoordinatorBinding(changed)).toThrow('invalid-coordinator-envelope')
})

it('#219 selects current versions for native pause and decision producers',()=>{
 for(const row of vector.items.filter(row=>row.name==='native-released-plan-pause'||row.name==='native-decided-plan-pause'))expect(writeRecord(CHECKPOINT_SCHEMA,{...row.checkpoint,version:2},RECORD_DEFAULTS).version).toBe(3)
 const event=vector.events.find(row=>row.name==='native-plan-decision-invocation')!.event
 expect(writeRunEvent({...event,version:undefined} as import('./engine').AssistantEvent).version).toBe(2)
})


it('#237 reads canonical native Plan receipt vectors unchanged',()=>{
 for(const checkpoint of nativeCheckpoints.items){
  expect(readExecution(checkpoint)).toEqual(checkpoint)
  expect(()=>checkRecord('checkpoint',checkpoint)).not.toThrow()
 }
 for(const event of nativeEvents.items)expect(readRunEvent(event)).toEqual(event)
})
