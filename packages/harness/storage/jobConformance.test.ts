import {describe,it,expect} from 'vitest'
import {StorageConformanceFailure} from './conformance'
import {JobRefused,type JobLifetime,type JobRegistration} from './job'
import {runStoreJobConformance,type JobRunStore,type JobStoreFactory} from './jobConformance'
import {LeaseRefused,type LeaseFence,type LeaseLifetime} from './lease'
import {MemoryRunStore} from './run'
import {WaitRefused,type WaitLifetime} from './wait'

/* #229: the job conformance kit over the memory kind, a real restart over a fresh store instance, and for every property a
 * store that leaves that one guarantee out. The Go twin is go/context/run_job_conformance_test.go. */
class DurableJobStore extends MemoryRunStore {override readonly leaseLifetime:LeaseLifetime='durable';override readonly waitLifetime:WaitLifetime='durable';override readonly jobLifetime:JobLifetime='durable'}
type Held=Record<string,any>
function restarted(old:MemoryRunStore,fresh:MemoryRunStore,drop:string[]=[]):MemoryRunStore{
 const from=old as unknown as Held,to=fresh as unknown as Held
 to.runs=structuredClone(from.runs)
 for(const table of ['leases','waits','journal','jobs'])for(const [name,value] of Object.entries(from[table] as Held))if(typeof value!=='function'&&!drop.includes(`${table}.${name}`))(to[table] as Held)[name]=structuredClone(value)
 return fresh
}
/** One fault on a store instance. It is applied again to the store a restart builds. */
type Fault=(store:Held,tables:{jobs:Held;waits:Held;leases:Held})=>void
const kind=(fault?:Fault,after?:{drop?:string[];then?:(fresh:Held)=>void;sameInstance?:boolean}):JobStoreFactory=>(now,from)=>{
 if(from&&after?.sameInstance)return from
 const fresh=new DurableJobStore({now}) as unknown as Held
 if(from){restarted(from as unknown as MemoryRunStore,fresh as unknown as MemoryRunStore,after?.drop);after?.then?.(fresh)}
 fault?.(fresh,{jobs:fresh.jobs,waits:fresh.waits,leases:fresh.leases})
 return fresh as unknown as JobRunStore
}
const wrap=(store:Held,name:string,by:(original:(...args:any[])=>any)=>(...args:any[])=>any)=>{const original=store[name].bind(store);store[name]=by(original)}
const withoutJob=({job:_,...rest}:{job?:JobRegistration}&Held)=>rest
/** For each property of the kit, a store that breaks exactly it. */
const FAULTS:Record<string,Fault>={
 'job.declared':store=>{Object.defineProperty(store,'jobVersion',{value:2})},
 'job.register.commits':store=>wrap(store,'commitFenced',original=>(input,fence,signal)=>original(withoutJob(input),fence,signal)),
 'job.register.unfenced':store=>wrap(store,'commit',original=>(input,signal)=>original(withoutJob(input),signal)),
 'job.register.fence':(store,{jobs})=>wrap(store,'commitFenced',original=>(input,fence,signal)=>{if(input.job&&fence.owner==='worker-x')jobs.register(input.runId,input.job,fence);return original(input,fence,signal)}),
 'job.register.identity':(_,{jobs})=>{const original=jobs.admit.bind(jobs);jobs.admit=(resource:string,input:JobRegistration,elsewhere:boolean)=>input.operation==='op-bad'?input:original(resource,input,elsewhere)},
 'job.register.atomic':store=>wrap(store,'commitFenced',original=>async(input,fence,signal)=>{try{return await original(input,fence,signal)}catch(error){if(error instanceof JobRefused&&error.reason==='key-reused'){await original(withoutJob(input),fence,signal)};throw error}}),
 'job.register.history':store=>wrap(store,'commitHistoryFenced',original=>(input,fence,signal)=>original({...input,run:withoutJob(input.run)},fence,signal)),
 'job.key.job':(_,{jobs})=>{jobs.keyHeld=()=>false},
 'job.key.wait':(_,{waits})=>{waits.keyHeld=()=>false},
 'job.wait.key':(store,{jobs})=>wrap(store,'parkWait',original=>async(park,fence,signal)=>{const held=jobs.keyHeld;jobs.keyHeld=()=>false;try{return await original(park,fence,signal)}finally{jobs.keyHeld=held}}),
 'job.owner.read':(store,{jobs})=>{store.readJob=async(_:string,job:string)=>{for(const list of jobs.rows.values()){const row=list.find((r:Held)=>r.job===job);if(row)return structuredClone(row)}return undefined}},
 'job.owner.list':(store,{jobs})=>{store.listJobs=async()=>({jobs:structuredClone([...jobs.rows.values()].flat()),next:null})},
 'job.owner.complete':store=>wrap(store,'completeJob',original=>(evidence,signal)=>original({...evidence,resource:'run-a'},signal)),
 'job.owner.reconcile':store=>wrap(store,'reconcileJob',original=>(evidence,signal)=>original({...evidence,resource:'run-a'},signal)),
 'job.answer.other':(_,{jobs})=>{const original=jobs.answer.bind(jobs);jobs.answer=(input:Held,fence:LeaseFence)=>original(input,{...fence,owner:'worker-a'})},
 'job.answer.late':(store,{leases})=>wrap(store,'answerJob',original=>(input,fence,signal)=>{if(fence.owner==='worker-a')leases.check(fence);return original(input,fence,signal)}),
 'job.answer.conflict':(_,{jobs})=>{const original=jobs.answer.bind(jobs);jobs.answer=(input:Held,fence:LeaseFence)=>{try{return original(input,fence)}catch(error){if(error instanceof JobRefused&&error.reason==='conflict')return structuredClone(jobs.read(input.resource,input.job));throw error}}},
 'job.answer.same':store=>{const seen=new Set<string>();wrap(store,'answerJob',original=>async(input,fence,signal)=>{if(seen.has(input.job+input.handle))throw new JobRefused('stale');const answer=await original(input,fence,signal);seen.add(input.job+input.handle);return answer})},
 'job.complete.key':(_,{jobs})=>{const original=jobs.complete.bind(jobs);jobs.complete=(input:Held)=>{const held=jobs.authenticate;jobs.authenticate=()=>undefined;try{return original(input)}finally{jobs.authenticate=held}}},
 'job.complete.settles':store=>wrap(store,'completeJob',original=>(evidence,signal)=>original({...evidence,outcome:'unknown'},signal)),
 'job.complete.duplicate':(_,{jobs})=>{jobs.retained=()=>undefined},
 'job.complete.conflict':(_,{jobs})=>{jobs.retained=(row:Held,evidence:string)=>[row.started,row.closed,row.completion].some((f:Held)=>f?.evidence===evidence)?structuredClone(row):undefined},
 'job.complete.stale':store=>wrap(store,'completeJob',original=>async(evidence,signal)=>{try{return await original(evidence,signal)}catch(error){if(error instanceof JobRefused&&error.reason==='stale')return store.readJob(evidence.resource,evidence.job);throw error}}),
 'job.reconcile.unknown':store=>wrap(store,'reconcileJob',original=>(evidence,signal)=>original(evidence.finding==='unknown'?{...evidence,finding:'not-started'}:evidence,signal)),
 'job.reconcile.key':(_,{jobs})=>{const original=jobs.reconcile.bind(jobs);jobs.reconcile=(input:Held)=>{const held=jobs.authenticate;jobs.authenticate=()=>undefined;try{return original(input)}finally{jobs.authenticate=held}}},
 'job.reconcile.no-lease':(store,{leases})=>wrap(store,'reconcileJob',original=>(evidence,signal)=>{if(evidence.resource==='run-d'&&leases.read(evidence.resource)?.state!=='held')throw new LeaseRefused('no-lease');return original(evidence,signal)}),
 'job.reconcile.same':store=>{const seen=new Set<string>();wrap(store,'reconcileJob',original=>async(evidence,signal)=>{if(evidence.finding==='not-started'&&seen.has(evidence.evidence))throw new JobRefused('stale');const answer=await original(evidence,signal);seen.add(evidence.evidence);return answer})},
 'job.reconcile.conflict':store=>wrap(store,'reconcileJob',original=>async(evidence,signal)=>{try{return await original(evidence,signal)}catch(error){if(error instanceof JobRefused&&error.reason==='conflict')return store.readJob(evidence.resource,evidence.job);throw error}}),
 'job.contradiction.completion':(_,{jobs})=>{jobs.contradict=()=>{throw new JobRefused('stale')}},
 'job.contradiction.start':(_,{jobs})=>{const original=jobs.contradict.bind(jobs);jobs.contradict=(row:Held,entry:Held)=>entry.kind==='started'?structuredClone(row):original(row,entry)},
 'job.reclaim.fence':(store,{jobs})=>{store.reclaimJob=async(resource:string,job:string,fence:LeaseFence)=>jobs.reclaim(resource,job,fence)},
 'job.reclaim.records':(store,{jobs})=>{store.reclaimJob=async(resource:string,job:string,fence:LeaseFence)=>{if(fence.owner==='worker-x')throw new LeaseRefused('other-owner');const row=jobs.read(resource,job);if(row.state!=='unproven')throw new JobRefused('job-settled');return row}},
 'job.reclaim.settled':(store,{jobs})=>wrap(store,'reclaimJob',original=>async(resource,job,fence,signal)=>{try{return await original(resource,job,fence,signal)}catch(error){if(error instanceof JobRefused&&error.reason==='job-settled')return jobs.read(resource,job);throw error}}),
 'job.reclaim.answer':(_,{jobs})=>{const original=jobs.answer.bind(jobs);jobs.answer=(input:Held,fence:LeaseFence)=>{const row=jobs.rows.get(input.resource)?.find((r:Held)=>r.job===input.job),held=row?.reclaimed;if(row)delete row.reclaimed;try{return original(input,fence)}finally{if(row&&held)row.reclaimed=held}}},
 'job.park.unproven':(_,{jobs})=>{const original=jobs.admitPark.bind(jobs);jobs.admitPark=(resource:string,job:string)=>{try{return original(resource,job)}catch(error){if(error instanceof JobRefused&&error.reason==='job-unproven')return jobs.read(resource,job);throw error}}},
 'job.park.settled':(_,{jobs})=>{const original=jobs.admitPark.bind(jobs);jobs.admitPark=(resource:string,job:string)=>{try{return original(resource,job)}catch(error){if(error instanceof JobRefused&&error.reason==='job-settled')return jobs.read(resource,job);throw error}}},
 'job.park.absent':(_,{jobs})=>{const original=jobs.admitPark.bind(jobs);jobs.admitPark=(resource:string,job:string)=>{try{return original(resource,job)}catch(error){if(error instanceof JobRefused&&error.reason==='no-job')return undefined;throw error}}},
 'job.wake.event':(store,{waits})=>wrap(store,'wakeWait',original=>async(evidence,signal)=>{if(evidence.kind==='event'&&evidence.evidence==='forged'){waits.wakeJob(evidence.resource,evidence.event.subject,{evidence:evidence.evidence,digest:`sha256:${'e'.repeat(64)}`});return waits.read(evidence.resource,evidence.wait).disposition}return original(evidence,signal)}),
 'job.wake.completion':(store,{jobs})=>{store.completeJob=async(evidence:Held)=>jobs.complete(evidence).job},
 'job.wake.duplicate':(store,{waits})=>wrap(store,'completeJob',original=>async(evidence,signal)=>{const answer=await original(evidence,signal),row=waits.rows.get(evidence.resource)?.[0];if(row?.state==='resumed')row.state='woken';return answer}),
 'job.wake.late':(_,{waits})=>{waits.wakeJob=(resource:string,job:string,evidence:Held)=>{const row=waits.rows.get(resource)?.find((r:Held)=>r.state==='parked'&&r.pending.job===job);if(row){row.disposition={...evidence,kind:'event',at:waits.now()};row.state='woken'}}},
 'job.timeout.state':(store,{waits})=>wrap(store,'wakeWait',original=>async(evidence,signal)=>{const answer=await original(evidence,signal),row=waits.rows.get(evidence.resource)?.[0];if(evidence.kind==='timeout'&&row)row.state='parked';return answer}),
 'job.timeout.continues':store=>wrap(store,'resumeWait',original=>async(claim,signal)=>{if(claim.evidence==='timeout-1')throw new WaitRefused('timed-out');return original(claim,signal)}),
 'job.limit':(_,{jobs})=>{const original=jobs.admit.bind(jobs);jobs.admit=(resource:string,input:JobRegistration,elsewhere:boolean)=>{const rows=jobs.rows.get(resource);if(rows&&rows.length>=64){jobs.rows.set(resource,rows.slice(0,1));try{return original(resource,input,elsewhere)}finally{jobs.rows.set(resource,rows)}}return original(resource,input,elsewhere)}},
 'job.list.page':(store,{jobs})=>{store.listJobs=async(resource:string,after:string|null)=>after===null?{jobs:structuredClone(jobs.rows.get(resource)??[]),next:null}:{jobs:[],next:null}},
}
const RESTARTS:Record<string,Parameters<typeof kind>[1]>={
 'job.restart.instance':{sameInstance:true},
 'job.restart.job':{drop:['jobs.rows']},
 'job.restart.contradiction':{then:fresh=>{for(const list of fresh.jobs.rows.values())for(const row of list)delete row.contradictions}},
 'job.restart.key':{then:fresh=>{fresh.jobs.keyHeld=()=>false}},
}
const fails=(property:string)=>expect.objectContaining({name:'StorageConformanceFailure',property})
describe('the job conformance kit (#229)',()=>{
 it('the memory kind passes, restarted over a copy of its state',async()=>{
  await expect(runStoreJobConformance(kind())).resolves.toBeUndefined()
 })
 for(const [property,fault] of Object.entries(FAULTS))it(`a store that breaks ${property} fails that property`,async()=>{
  await expect(runStoreJobConformance(kind(fault))).rejects.toEqual(fails(property))
 })
 for(const [property,after] of Object.entries(RESTARTS))it(`a restart that loses it fails ${property}`,async()=>{
  await expect(runStoreJobConformance(kind(undefined,after))).rejects.toEqual(fails(property))
 })
 it('a store whose port throws something unnamed fails as an operation',async()=>{
  await expect(runStoreJobConformance(()=>{throw Error('down')})).rejects.toBeInstanceOf(StorageConformanceFailure)
 })
})
