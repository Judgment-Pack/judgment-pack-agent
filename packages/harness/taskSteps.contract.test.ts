import {it,expect} from 'vitest'
import vector from '../../contract/testdata/task-steps/records.json'
import {readExecution} from './execution/state'
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

for(const row of vector.events)it(`#183 shared current event ${row.name}`,()=>{expect(()=>checkRecord('event',row.event)).not.toThrow()})
