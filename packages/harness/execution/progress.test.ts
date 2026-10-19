import {expect,it} from 'vitest'
import {ExecutionProgressTracker} from './progress'
import type {Operation} from './state'
const operation=(name='read',args:unknown={id:'one'},result:unknown={value:false}):Operation=>({id:crypto.randomUUID(),name,args,status:'complete',result:{structuredContent:result}})
it('detects unchanged alternating tools despite argument key order, without treating new work as repetition',()=>{
 const progress=new ExecutionProgressTracker(),read=operation('read',{id:'one',type:'pack'}),validate=operation('validate')
 progress.observe(read);progress.observe(validate)
 progress.observe(operation('read',{type:'pack',id:'one'}));progress.observe(validate)
 expect(progress.stalled).toBe(false);expect(progress.guidance).toContain('unchanged')
 progress.observe(read);expect(progress.stalled).toBe(true)
 progress.observe(operation('read',{id:'one',type:'pack'},{value:true}));expect(progress.stalled).toBe(false)
})
it('preserves missing, null, false, zero and changed arguments as distinct observations',()=>{
 const progress=new ExecutionProgressTracker()
 for(const value of [{}, {value:null}, {value:false}, {value:0}, {value:'0'}]){progress.observe(operation('read',{},value));expect(progress.stalled).toBe(false)}
 for(let i=0;i<4;i++){progress.observe(operation('evaluate',{id:i}));expect(progress.stalled).toBe(false)}
})
it('requires new repetition after explicit continuation and does not count pending or interrupted calls',()=>{
 const progress=new ExecutionProgressTracker(),read=operation()
 progress.seed([read,read,read,read]);expect(progress.stalled).toBe(false)
 progress.observe(read);progress.observe(read);expect(progress.stalled).toBe(false)
 progress.observe({...read,status:'interrupted'});progress.observe(read);expect(progress.stalled).toBe(false)
})

it('permits one corrective step and never counts its refused duplicate as progress',()=>{
 const progress=new ExecutionProgressTracker(),read=operation('read',{a:0,b:false})
 for(let i=0;i<4;i++)progress.observe(read)
 expect(progress.nextStep(5)).toBe('recover')
 expect(progress.rejects('read',{b:false,a:0})).toBe(true)
 expect(progress.rejects('read',{a:'0',b:false})).toBe(false)
 expect(progress.rejects('evaluate',{a:0,b:false})).toBe(false)
 progress.observe({...read,status:'failed',result:{structuredContent:{failure:{version:1,code:'repeated-work',source:'desk',outcome:'not-started'}}}})
 expect(progress.stalled).toBe(true);expect(progress.nextStep(4)).toBe('finish')
})
it('resumes useful work after correction but grants no second corrective loop',()=>{
 const progress=new ExecutionProgressTracker(),read=operation()
 for(let i=0;i<4;i++)progress.observe(read)
 expect(progress.nextStep(10)).toBe('recover')
 progress.observe(operation('evaluate'));expect(progress.nextStep(9)).toBe('work')
 for(let i=0;i<3;i++)progress.observe(read)
 expect(progress.nextStep(8)).toBe('finish')
})
it('keeps the last request for a final answer and isolates an independent reviewer',()=>{
 const progress=new ExecutionProgressTracker(),read=operation()
 for(let i=0;i<4;i++)progress.observe(read)
 expect(progress.nextStep(1)).toBe('finish')
 progress.beginPhase();expect(progress.nextStep(5)).toBe('work')
 progress.observe(read);expect(progress.guidance).toBe('')
})
