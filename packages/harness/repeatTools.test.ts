import {describe,it,expect} from 'vitest'
import vectors from '../../contract/testdata/repeat-tools/scenarios.json'
import {observeRepeat,repeatKey,validRepeatTools,type RepeatExecution} from './repeatTools'
import type {DurableDispatch} from './storage/writer'
const initial=(stopAt=5):RepeatExecution=>({execution:'execution',remindAt:1,stopAt,count:0,reminded:false,reminderDelivered:false,stopped:false,seen:[]})
describe('#204 shared handwritten vectors',()=>{
 for(const scenario of vectors.items)it(scenario.name,()=>{
  let state=initial(scenario.stopAt)
  for(const [i,input] of scenario.calls.entries()){
   const call=input as typeof input&{source?:string;tool?:string;reminder?:boolean;stopped?:boolean}
   const row:DurableDispatch={id:`call-${i}`,scope:{execution:'execution',role:i%2?'critic':'author'},kind:'host',source:call.source??'source',tool:call.tool??'lookup',request:JSON.stringify(call.args),requestDigest:'unused',fingerprint:'unused',state:'result',read:true,result:call.result,repeatResultJson:JSON.stringify(call.result)}
   const next=observeRepeat(state,row);state=next.state
   expect(state.count).toBe(call.count);expect(next.event?.reminder??false).toBe(call.reminder??false);expect(state.stopped).toBe(call.stopped??false)
   expect(observeRepeat(state,row).state).toEqual(state)
   expect(validRepeatTools({executions:[state]})).toBe(true)
  }
 })
 it('refuses unknown, unacknowledged and foreign-scope evidence without creating a digest',()=>{
  const row={id:'call',scope:{execution:'other',role:'author'},kind:'host',source:'source',tool:'lookup',request:'{}',state:'unknown',read:true} as DurableDispatch
  expect(()=>repeatKey(row)).toThrow();expect(()=>observeRepeat(initial(),row)).toThrow('scope')
  expect(validRepeatTools({executions:[{...initial(),count:1}]})).toBe(false)
  expect(validRepeatTools({executions:[{...initial(),reminderDelivered:true}]})).toBe(false)
 })
})
it('#204 exact result encodings and lossless raw argument identities',async()=>{
 const vectors=(await import('../../contract/testdata/repeat-tools/identities.json')).default
 for(const row of vectors.cases){
  const dispatch=(request:string,repeatResultJson:string):DurableDispatch=>({id:'physical',scope:{execution:'execution',role:'author'},kind:'host',source:'source',tool:'lookup',request,requestDigest:'unused',fingerprint:'unused',state:'result',read:true,result:JSON.parse(repeatResultJson),repeatResultJson})
  expect(repeatKey(dispatch(row.leftArgs,row.leftResult))===repeatKey(dispatch(row.rightArgs,row.rightResult)),row.name).toBe(row.same)
 }
})
it('#204 capacity never evicts original physical observations',()=>{
 let state=initial(400)
 const dispatch=(i:number):DurableDispatch=>({id:`row-${i}`,scope:{execution:'execution',role:'author'},kind:'host',source:'source',tool:'lookup',request:JSON.stringify({n:i}),requestDigest:'unused',fingerprint:'unused',state:'result',read:true,result:{},repeatResultJson:'{}'})
 for(let i=0;i<400;i++)state=observeRepeat(state,dispatch(i)).state
 expect(state.seen).toHaveLength(400);expect(state.seen[0]!.dispatchId).toBe('row-0');expect(()=>observeRepeat(state,dispatch(400))).toThrow('capacity')
})
