import {describe,it,expect} from 'vitest'
import {StorageConformanceFailure} from './conformance'
import {JobRefused,jobIdentity,type JobLifetime,type JobRegistration} from './job'
import {runStoreJobConformance,type JobRunStore,type JobStoreFactory} from './jobConformance'
import {LeaseRefused,fenceOf,type LeaseFence,type LeaseLifetime} from './lease'
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
 'job.answer.late':(store,{leases,jobs})=>wrap(store,'answerJob',original=>(input,fence,signal)=>{if(fence.owner==='worker-a'&&fence.resource===input.resource&&jobs.read(input.resource,input.job)?.registered.epoch===fence.epoch)leases.check(fence);return original(input,fence,signal)}),
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
 'job.wake.resumed':(store,{waits})=>wrap(store,'completeJob',original=>async(evidence,signal)=>{const answer=await original(evidence,signal),row=waits.rows.get(evidence.resource)?.[0];if(evidence.resource==='run-m'&&row?.state==='resumed')row.state='woken';return answer}),
 'job.wake.again':(store,{waits})=>wrap(store,'completeJob',original=>async(evidence,signal)=>{const answer=await original(evidence,signal),row=waits.rows.get(evidence.resource)?.[1];if(evidence.resource==='run-m'&&row?.state==='woken'){row.state='parked';delete row.disposition}return answer}),
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
/** The history path, where a store's claims really commit: registers a job while skipping the check `when` selects. */
const bypass=(when:(reg:JobRegistration,jobs:Held,run:string)=>boolean):Fault=>(store,{jobs})=>wrap(store,'commitHistoryFenced',original=>async(input,fence,signal)=>{
 const reg=input.run.job
 if(!reg||!when(reg,jobs,input.run.runId))return original(input,fence,signal)
 const receipt=await original({...input,run:withoutJob(input.run)},fence,signal);jobs.register(input.run.runId,reg,fence);return receipt
})
const pathOf=(path:'fenced'|'history')=>({method:path==='fenced'?'commitFenced':'commitHistoryFenced',claim:(input:Held)=>path==='fenced'?input:input.run})
/** A compare-and-set that fails still registers the job. */
const staleRegisters=(path:'fenced'|'history'):Fault=>(store,{jobs})=>{const {method,claim}=pathOf(path);wrap(store,method,original=>async(input,fence,signal)=>{
 try{return await original(input,fence,signal)}catch(error){const c=claim(input);if(c.job&&(await store.load(c.runId))?.revision!==c.expectedRevision)jobs.register(c.runId,c.job,fence);throw error}
})}
/** A repeated commit id with another registration is answered, and registers again. */
const repeatRegisters=(path:'fenced'|'history'):Fault=>(store,{jobs})=>{const {method,claim}=pathOf(path),seen=new Map<string,unknown>();wrap(store,method,original=>async(input,fence,signal)=>{
 const c=claim(input)
 if(c.job&&seen.has(c.commitId)){jobs.register(c.runId,c.job,fence);return seen.get(c.commitId)}
 const receipt=await original(input,fence,signal);seen.set(c.commitId,receipt);return receipt
})}
const firstReads=(name:string,keyOf:(...args:any[])=>string):Fault=>store=>{const first=new Map<string,unknown>();wrap(store,name,original=>async(...args)=>{const key=keyOf(...args);if(!first.has(key))first.set(key,await original(...args));return structuredClone(first.get(key))})}
/** What a store does wrong beyond a single property, each naming the property the kit must fail it on. */
const STORES:[string,string,Fault][]=[
 ['E3 the history path skips the identity check','job.history.identity',bypass((reg,_,run)=>reg.job!==jobIdentity(run,reg.operation))],
 ['E3 the history path skips the key check','job.history.key',bypass((reg,jobs)=>jobs.keyHeld(reg.keyDigest))],
 ['E3 the history path skips the limit','job.history.limit',bypass((_,jobs,run)=>(jobs.rows.get(run)?.length??0)>=64)],
 ['E3b the history path does not check the fence','job.history.fence',store=>wrap(store,'commitHistoryFenced',original=>async(input,fence,signal)=>original(input,input.run.job?fenceOf((await store.readLease(input.run.runId))!):fence,signal))],
 ['E4 two heads: the history path answers a receipt and never moves the head','job.history.head',(store,{jobs})=>wrap(store,'commitHistoryFenced',original=>async(input,fence,signal)=>{if(!input.run.job)return original(input,fence,signal);jobs.register(input.run.runId,input.run.job,fence);return {commitId:input.run.commitId}})],
 ['E5 a compare-and-set that fails still registers, fenced','job.stale.fenced',staleRegisters('fenced')],
 ['E5 a compare-and-set that fails still registers, history','job.stale.history',staleRegisters('history')],
 ['E6 the export drops contradictions','job.export.reads',store=>wrap(store,'exportJobs',original=>async(...args)=>{const held=await original(...args);for(const row of held.jobs)delete row.contradictions;return held})],
 ['E7 a re-claim takes an expired fence of the same owner','job.reclaim.expired',(store,{jobs})=>wrap(store,'reclaimJob',original=>async(resource,job,fence,signal)=>{const lease=await store.readLease(resource);return lease&&fence.owner===lease.owner&&fence.epoch===lease.epoch?jobs.reclaim(resource,job,fence):original(resource,job,fence,signal)})],
 ['E7 a re-claim takes a stale fence of the same owner','job.reclaim.stale',(store,{jobs})=>wrap(store,'reclaimJob',original=>async(resource,job,fence,signal)=>{const lease=await store.readLease(resource);return lease&&fence.owner===lease.owner&&fence.epoch!==lease.epoch?jobs.reclaim(resource,job,fence):original(resource,job,fence,signal)})],
 ['E8 a repeated commit id with another registration registers again, fenced','job.repeat.fenced',repeatRegisters('fenced')],
 ['E8 a repeated commit id with another registration registers again, history','job.repeat.history',repeatRegisters('history')],
 ['E9 an answer finds its job across runs by owner and epoch','job.answer.across',(store,{jobs})=>wrap(store,'answerJob',original=>(answer,fence,signal)=>{for(const [resource,rows] of jobs.rows as Map<string,Held[]>)if(rows.some(row=>row.job===answer.job))return original({...answer,resource},fence,signal);return original(answer,fence,signal)})],
 ['An answer ignores the fence\'s run','job.answer.run',store=>wrap(store,'answerJob',original=>(answer,fence,signal)=>original(answer,{...fence,resource:answer.resource},signal))],
 ['E10 a completion wakes a wait past its deadline','job.wake.deadline',(_,{waits})=>{const original=waits.wakeJob.bind(waits);waits.wakeJob=(resource:string,job:string,evidence:Held)=>{const row=waits.rows.get(resource)?.find((r:Held)=>r.state==='parked'&&r.pending.job===job);if(row?.deadline){row.disposition={...evidence,kind:'event',at:waits.now()};row.state='woken';return}original(resource,job,evidence)}}],
 ['E11 a park accepts another run\'s started job','job.park.other',(_,{jobs})=>{const original=jobs.admitPark.bind(jobs);jobs.admitPark=(resource:string,job:string)=>{for(const rows of jobs.rows.values())for(const row of rows)if(row.job===job&&row.resource!==resource)return row;return original(resource,job)}}],
 ['E12 listJobs answers as of the first read','job.list.fresh',firstReads('listJobs',(resource,after,limit)=>`${resource}/${after}/${limit}`)],
 ['E13 readJob answers as of the first read','job.read.fresh',firstReads('readJob',(resource,job)=>`${resource}/${job}`)],
 ['A repeated contradiction id with other content is taken','job.contradiction.conflict',store=>wrap(store,'completeJob',original=>async(evidence,signal)=>{try{return await original(evidence,signal)}catch(error){if(evidence.resource==='run-d'&&error instanceof JobRefused&&error.reason==='conflict')return store.readJob(evidence.resource,evidence.job);throw error}})],
]
/** The third instance is the one the second restart builds. */
const afterTheLastRestart=(by:Fault):Fault=>{let instance=0;return (store,tables)=>{if(++instance===3)by(store,tables)}}
const cancelOf=(jobs:Held,input:Held)=>jobs.rows.get(input.resource)?.find((r:Held)=>r.job===input.job)
const REQUESTED={by:'tool',operation:'cancel-x',at:'2026-01-01T00:00:00.000Z',owner:'worker-a',epoch:'1'}
const askTakesEffect=(reason:string):Fault=>(store,{jobs})=>wrap(store,'requestJobCancel',original=>async(input,fence,signal)=>{try{return await original(input,fence,signal)}catch(error){if((error as {reason?:string}).reason===reason){const row=cancelOf(jobs,input);if(row&&row.state==='started')row.cancel={requested:REQUESTED}};throw error}})
const skipsFence=(when:(fence:Held,lease:Held|undefined)=>boolean):Fault=>(store,{jobs})=>wrap(store,'requestJobCancel',original=>async(input,fence,signal)=>when(fence,await store.readLease(input.resource))?jobs.requestCancel(input,fence):original(input,fence,signal))
STORES.push(
 ['A cancel from another owner is refused and took effect','job.cancel.fence',askTakesEffect('other-owner')],
 ['A cancel takes a stale fence of the same owner','job.cancel.stale',skipsFence((fence,lease)=>lease?.owner===fence.owner&&lease?.epoch!==fence.epoch)],
 ['A cancel takes an expired fence','job.cancel.expired',skipsFence((fence,lease)=>lease?.owner===fence.owner&&lease?.epoch===fence.epoch&&fence.resource==='run-cancel-stale')],
 ['A cancel of an unproven job is taken','job.cancel.unproven',store=>wrap(store,'requestJobCancel',original=>async(input,fence,signal)=>{const row=await store.readJob(input.resource,input.job);return row?.state==='unproven'?{...row,cancel:{requested:REQUESTED}}:original(input,fence,signal)})],
 ['A cancel of a completed job is taken','job.cancel.settled',store=>wrap(store,'requestJobCancel',original=>async(input,fence,signal)=>{const row=await store.readJob(input.resource,input.job);return row?.state==='completed'?row:original(input,fence,signal)})],
 ['A cancel of a closed job is taken','job.cancel.closed',store=>wrap(store,'requestJobCancel',original=>async(input,fence,signal)=>{const row=await store.readJob(input.resource,input.job);return row?.state==='not-started'?row:original(input,fence,signal)})],
 ['A cancel moves the state','job.cancel.records',store=>wrap(store,'requestJobCancel',original=>async(input,fence,signal)=>({...await original(input,fence,signal),state:'completed'}))],
 ['A later request replaces the first','job.cancel.first',(store,{jobs})=>wrap(store,'requestJobCancel',original=>async(input,fence,signal)=>{const row=cancelOf(jobs,input);if(row?.cancel)delete row.cancel;return original(input,fence,signal)})],
 ['A word with the wrong key is refused and took effect','job.cancel.key',(store,{jobs})=>wrap(store,'acknowledgeJobCancel',original=>async(input,signal)=>{try{return await original(input,signal)}catch(error){const row=cancelOf(jobs,input);if((error as {reason?:string}).reason==='unauthenticated'&&row?.cancel)row.cancel.acknowledged={evidence:input.evidence,outcome:input.outcome,at:'2026-01-01T00:00:00.000Z',digest:`sha256:${'a'.repeat(64)}`};throw error}})],
 ['A word with no cancel is taken','job.cancel.none',(store,{jobs})=>wrap(store,'acknowledgeJobCancel',original=>async(input,signal)=>{const row=cancelOf(jobs,input);return row&&!row.cancel?structuredClone(row):original(input,signal)})],
 ['A word moves the state','job.cancel.word',store=>wrap(store,'acknowledgeJobCancel',original=>async(input,signal)=>({...await original(input,signal),state:'completed'}))],
 ['The same word again is refused','job.cancel.same',store=>{let once=false;wrap(store,'acknowledgeJobCancel',original=>async(input,signal)=>{if(input.evidence==='word-1'&&input.outcome==='accepted'&&input.resource==='run-cancel'&&!String(input.key).startsWith('kit.job-wrong')){if(once)throw new JobRefused('stale');const held=await original(input,signal);once=true;return held};return original(input,signal)})}],
 ['The same word\'s id with other content is taken','job.cancel.conflict',(store,{jobs})=>wrap(store,'acknowledgeJobCancel',original=>async(input,signal)=>{try{return await original(input,signal)}catch(error){const row=cancelOf(jobs,input);if((error as {reason?:string}).reason==='conflict'&&row)return structuredClone(row);throw error}})],
 ['Another word after the first is taken','job.cancel.again',(store,{jobs})=>wrap(store,'acknowledgeJobCancel',original=>async(input,signal)=>{try{return await original(input,signal)}catch(error){const row=cancelOf(jobs,input);if((error as {reason?:string}).reason==='stale'&&row)return structuredClone(row);throw error}})],
 ['A completion drops the cancel','job.cancel.completes',(store,{jobs})=>wrap(store,'completeJob',original=>async(input,signal)=>{const held=await original(input,signal),row=cancelOf(jobs,input);if(input.resource==='run-cancel'&&row){delete row.cancel;const {cancel:_,...rest}=held;return rest};return held})],
 ['A word wakes the wait','job.cancel.nowake',(store,{waits})=>wrap(store,'acknowledgeJobCancel',original=>async(input,signal)=>{const held=await original(input,signal);const row=waits.rows.get(input.resource)?.[0];if(row?.state==='parked'){row.state='woken';row.disposition={evidence:input.evidence,kind:'event',at:'2026-01-01T00:00:00.000Z',digest:`sha256:${'a'.repeat(64)}`}};return held})],
 ['An abandon lowers the lease at the same epoch','job.abandon.lease',store=>wrap(store,'abandonJobs',original=>async(input,signal)=>{const before=await store.readLease(input.resource);const held=await original(input,signal);return before&&held.lease?{...held,lease:{...held.lease,epoch:before.epoch}}:held})],
 ['An abandon commits to the run','job.abandon.nocommit',store=>wrap(store,'abandonJobs',original=>async(input,signal)=>{const head=(await store.load(input.resource))!;const lease=(await store.readLease(input.resource))!;if(input.resource==='run-abandon')await store.commitFenced({runId:input.resource,expectedRevision:head.revision,commitId:'abandon',checkpoint:head.checkpoint,events:[]},fenceOf(lease));return original(input,signal)})],
 ['An abandon leaves old workers their fence','job.abandon.epoch',store=>wrap(store,'commitFenced',original=>async(input,fence,signal)=>input.commitId==='run-abandon-op-late'?{commitId:input.commitId}:original(input,fence,signal))],
 ['An abandon asks no cancel','job.abandon.asks',(store,{jobs})=>wrap(store,'abandonJobs',original=>async(input,signal)=>{const held=await original(input,signal);for(const row of jobs.rows.get(input.resource)??[])delete row.cancel;return {...held,jobs:held.jobs.map((row:Held)=>{const {cancel:_,...rest}=row;return rest})}})],
 ['An abandon asks a cancel of what has no handle or has settled','job.abandon.only',(store,{jobs})=>wrap(store,'abandonJobs',original=>async(input,signal)=>{const held=await original(input,signal);for(const row of jobs.rows.get(input.resource)??[])if(row.state!=='started')row.cancel={requested:REQUESTED};return held})],
 ['An abandon answers no jobs','job.abandon.jobs',store=>wrap(store,'abandonJobs',original=>async(input,signal)=>({...await original(input,signal),jobs:[]}))],
 ['An abandon past its deadline grants a lease or says nothing','job.abandon.past',(store,{jobs})=>wrap(store,'abandonJobs',original=>async(input,signal)=>{const held=await original(input,signal);if(input.resource==='run-abandon-late')for(const row of jobs.rows.get(input.resource)??[])delete row.cancel;return held})],
 ['An abandon drops the run\'s principals','job.abandon.principal',store=>wrap(store,'abandonJobs',original=>async(input,signal)=>{const held=await original(input,signal);if(!held.lease?.principal)return held;const {principal:_,...lease}=held.lease;return {...held,lease:{...lease,version:1}}})],
 ['A restart loses a cancel','job.restart.record',afterTheLastRestart((_,{jobs})=>{for(const list of jobs.rows.values())for(const row of list)delete row.cancel})],
)
const tookEffect=(method:string,when:(input:Held)=>boolean,effect:(row:Held,input:Held)=>void):Fault=>(store,{jobs})=>wrap(store,method,original=>async(input,...rest)=>{
 try{return await original(input,...rest)}catch(error){if(when(input)){const row=jobs.rows.get(input.resource)?.find((r:Held)=>r.job===input.job);if(row)effect(row,input)};throw error}
})
const WRONG_KEY=(input:Held)=>input.key!==undefined&&!/^job\.|^kit\./.test(String(input.key))||String(input.key).startsWith('kit.job-wrong')
const staleOnceSettled=(settled:string):Fault=>store=>{const last=new Map<string,unknown>();wrap(store,'readJob',original=>async(resource,job,signal)=>{const held=await original(resource,job,signal);if(held?.state!==settled){last.set(job,held);return held};return structuredClone(last.get(job)??held)})}
STORES.push(
 ['R-3 a completion with the wrong key is refused and took effect','job.complete.key',tookEffect('completeJob',WRONG_KEY,(row,input)=>{row.state='completed';row.completion={evidence:input.evidence,outcome:input.outcome,at:'2026-01-01T00:00:00.000Z',digest:`sha256:${'a'.repeat(64)}`}})],
 ['R-3 a reconciliation with the wrong key is refused and took effect','job.reconcile.key',tookEffect('reconcileJob',WRONG_KEY,(row,input)=>{row.state='not-started';row.closed={by:'reconcile',at:'2026-01-01T00:00:00.000Z',digest:`sha256:${'a'.repeat(64)}`,evidence:input.evidence}})],
 ['R-3 an answer from another owner is refused and took effect','job.answer.other',(store,{jobs})=>wrap(store,'answerJob',original=>async(input,fence,signal)=>{try{return await original(input,fence,signal)}catch(error){if(fence.owner==='worker-x'){const row=jobs.rows.get(input.resource)?.find((r:Held)=>r.job===input.job);if(row){row.state='started';row.handle=input.handle;row.started={by:'answer',at:'2026-01-01T00:00:00.000Z',digest:`sha256:${'a'.repeat(64)}`}}};throw error}})],
 ['R-3 a completion wakes the run\'s parked wait, whichever job it is for','job.wake.other',(_,{waits})=>{const original=waits.wakeJob.bind(waits);waits.wakeJob=(resource:string,_job:string,evidence:Held)=>{for(const row of waits.rows.get(resource)??[])if(row.state==='parked'&&row.pending.kind==='job')return original(resource,row.pending.job,evidence)}}],
 ['R-3 readJob is stale once a job is completed','job.read.completed',staleOnceSettled('completed')],
 ['R-3 readJob is stale once a job is not-started','job.read.closed',staleOnceSettled('not-started')],
 ['R-3 an answer ignores the epoch: any fence of the first owner answers','job.answer.epoch',(store,{jobs})=>wrap(store,'answerJob',original=>(input,fence,signal)=>{const row=jobs.rows.get(input.resource)?.find((r:Held)=>r.job===input.job);return original(input,row&&fence.owner===(row.reclaimed??row.registered).owner?{...fence,epoch:(row.reclaimed??row.registered).epoch}:fence,signal)})],
 ['R-3 after a restart a repeated commit id is forgotten','job.restart.commit',afterTheLastRestart(store=>wrap(store,'commitFenced',original=>async(input,fence,signal)=>input.commitId==='run-after-op-1'?{commitId:input.commitId}:original(input,fence,signal)))],
 ['R-3 after a restart a wake key is free for a job','job.restart.wakekey',afterTheLastRestart((store,{jobs})=>wrap(store,'commitFenced',original=>async(input,fence,signal)=>{if(input.commitId==='run-after-op-wake'){jobs.register(input.runId,input.job,fence);return {commitId:input.commitId}};return original(input,fence,signal)}))],
 ['R-3 after a restart a job key is free for a wait','job.restart.jobkey',afterTheLastRestart(store=>wrap(store,'parkWait',original=>async(park,fence,signal)=>park.resource==='run-after'?{wait:park}:original(park,fence,signal)))],
 ['R-3 after a restart a completion is unauthenticated','job.restart.auth',afterTheLastRestart(store=>wrap(store,'completeJob',original=>async(input,signal)=>input.resource==='run-after'?store.readJob(input.resource,input.job):original(input,signal)))],
)
/** What a restart loses, each naming the property the kit must fail it on. */
const LOST:[string,string,NonNullable<Parameters<typeof kind>[1]>][]=[
 ['E1 jobs registered through the history path do not survive','job.restart.job',{then:fresh=>{fresh.jobs.rows.delete('run-history')}}],
 ['E2 a completion does not survive: the job reads as started again','job.restart.record',{then:fresh=>{for(const list of fresh.jobs.rows.values())for(const row of list)if(row.state==='completed'&&row.started){row.state='started';delete row.completion}}}],
 ['E2b a restart loses the handle','job.restart.record',{then:fresh=>{for(const list of fresh.jobs.rows.values())for(const row of list)if(row.state==='started'){row.handle='another-handle'}}}],
 ['E2b a restart loses who re-claimed','job.restart.record',{then:fresh=>{for(const list of fresh.jobs.rows.values())for(const row of list)delete row.reclaimed}}],
 ['An open wait does not survive','job.restart.wait',{then:fresh=>{fresh.waits.rows.clear()}}],
 ['R-3 a restart turns a woken, resumed or timed-out wait back to parked','job.restart.wait',{then:fresh=>{for(const list of fresh.waits.rows.values())for(const row of list)if(row.state!=='parked'){row.state='parked';delete row.disposition}}}],
]
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
 for(const [name,property,fault] of STORES)it(`${name}: fails ${property}`,async()=>{
  await expect(runStoreJobConformance(kind(fault))).rejects.toEqual(fails(property))
 })
 for(const [name,property,after] of LOST)it(`${name}: fails ${property}`,async()=>{
  await expect(runStoreJobConformance(kind(undefined,after))).rejects.toEqual(fails(property))
 })
 it('a store whose port throws something unnamed fails as an operation',async()=>{
  await expect(runStoreJobConformance(()=>{throw Error('down')})).rejects.toBeInstanceOf(StorageConformanceFailure)
 })
})
