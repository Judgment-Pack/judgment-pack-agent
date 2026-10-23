import assert from 'node:assert/strict'
import test from 'node:test'
import * as z from 'zod'
import * as core from 'zod/v4/core'

// Tiny, handwritten inputs: the guard is element work, never memory exhaustion.
const collections=[
 {name:'array max',schema:element=>z.array(element).max(2),input:values=>values},
 {name:'array exact',schema:element=>z.array(element).length(2),input:values=>values},
 {name:'set max',schema:element=>z.set(element).max(2),input:values=>new Set(values)},
 {name:'set exact',schema:element=>z.set(element).size(2),input:values=>new Set(values)},
 {name:'map max',schema:(element,key)=>z.map(key,element).max(2),input:values=>new Map(values.map(value=>[value,value]))},
 {name:'map exact',schema:(element,key)=>z.map(key,element).size(2),input:values=>new Map(values.map(value=>[value,value]))}
]
for(const mode of ['interpreted','async interpreted','compiled wrapper','compiled function']){
 for(const collection of collections){
  test(`Zod ${mode}: ${collection.name} rejects before element callbacks`,async()=>{
   let values=0,keys=0,laterChecks=0
   const element=z.string().refine(()=>{values++;return true})
   const key=z.string().refine(()=>{keys++;return true})
   const original=collection.schema(element,key).refine(()=>{laterChecks++;return true})
   let schema=original,compiled
   if(mode==='compiled wrapper'){
    assert.equal(typeof core.compile,'function','the fixed candidate must expose strict compilation')
    schema=core.compile(original,{strict:true})
    assert.notEqual(schema,original,'a silent interpreter fallback cannot cover compilation')
   }
   if(mode==='compiled function'){
    assert.equal(typeof core.compileFn,'function','the fixed candidate must expose the compiled gate')
    compiled=core.compileFn(original)
   }
   const oversized=collection.input(['a','b','c'])
   if(compiled)assert.equal(compiled(oversized),core.INVALID)
   else{
    const result=mode==='async interpreted'?await schema.safeParseAsync(oversized):schema.safeParse(oversized)
    assert.equal(result.success,false)
    assert.deepEqual(result.error.issues.map(issue=>issue.code),['too_big'])
   }
   assert.equal(values,0,'oversized collection must reject before value callbacks')
   assert.equal(keys,0,'oversized collection must reject before map-key callbacks')
   assert.equal(laterChecks,0,'oversized raw input must not reach later checks')

   const permitted=collection.input(['a','b'])
   if(compiled)assert.notEqual(compiled(permitted),core.INVALID)
   else assert.equal((mode==='async interpreted'?await schema.safeParseAsync(permitted):schema.safeParse(permitted)).success,true)
   assert.equal(values,2,'positive control must execute each value callback')
   assert.equal(keys,collection.name.startsWith('map')?2:0)
   assert.equal(laterChecks,1)
  })
 }
}
