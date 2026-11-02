import {canonicalJSON} from '../context/chat'
import {StorageConformanceFailure} from './conformance'
import {JobRefused,jobIdentity,jobKeyDigest,type JobCompletionEvidence,type JobPage,type JobReconciliation,type JobStore,type RunJob} from './job'
import {LeaseRefused,fenceOf,type LeaseFence} from './lease'
import {pinsDigest,type ResumePins} from './resume'
import {WaitRefused,checkpointBytesDigest,wakeKeyDigest,type RunWait,type WaitPark,type WaitRunStore,type WakeEvidence} from './wait'
/* #229: the job conformance kit, for a host store kind's job port beside its lease and wait ports. Every guarantee of a
 * job lives in the store's own transactions, and the package re-checks only what it reads back: the kit drives the port
 * directly and names the first property a store breaks. The Go twin is CheckRunStoreJobConformance in
 * go/context/run_job_conformance.go. */

export type JobRunStore=WaitRunStore&JobStore
/** Builds the store under test. The kit hands it the store clock it moves; with `restart`, the factory returns a new
 * instance over the same durable state, as after the store's process restarted. */
export type JobStoreFactory=(now:()=>Date,restart?:JobRunStore)=>JobRunStore|Promise<JobRunStore>
const T0=Date.parse('2026-01-01T00:00:00.000Z'),MINUTE=60_000,HOUR=60*MINUTE
const iso=(ms:number)=>new Date(ms).toISOString()
const bytes=(text:string)=>new TextEncoder().encode(text)
const KEYS=['kit.job-a.Q7z2Xm9LpV4cR8tNb3Yh6Df1','kit.job-b.Hs8Pq3Lz6Wd1Rm9Tc4Vb7Nx2','kit.job-c.Ka5Ue2Jy8Gt3Wo6Bf9Mn1Sv4','kit.job-d.Zc3Ri7Oq1Xe5Pk9Ls2Gu6Ty8','kit.job-e.Vx4Hd9Tn2Bq7Ym5Rc1Pz8Lf','kit.job-f.Mb6Sj1Wg8Ku3Ne7Ax2Qo5Di','kit.job-g.Ct9Fh4Lp7Xr2Gv6Jz1Ys3Em','kit.job-h.Ur5Ib8Oa3Tk1Nw6Pd9Hc2Vl','kit.job-i.Ge7Zq2My5Rf4Xs9Ku1Bn6Ta','kit.job-j.Ld3Pv8Hj6Cw1Oi4Se7Gx9Fm'] as const
const WAKES=['kit.wake-a.Q7z2Xm9LpV4cR8tNb3Yh6Df1','kit.wake-b.Hs8Pq3Lz6Wd1Rm9Tc4Vb7Nx2','kit.wake-c.Ka5Ue2Jy8Gt3Wo6Bf9Mn1Sv4','kit.wake-d.Zc3Ri7Oq1Xe5Pk9Ls2Gu6Ty8','kit.wake-e.Vx4Hd9Tn2Bq7Ym5Rc1Pz8Lf','kit.wake-f.Mb6Jt1Cw8Qa3Ne5Xk2Hr9Ug7'] as const
const WRONG='kit.job-wrong.Ge7Zq2My5Rf4Xs9Ku1Bn6Ta'
const REQUEST=`sha256:${'a'.repeat(64)}`
const check=(property:string,value:unknown)=>{if(!value)throw new StorageConformanceFailure(property)}
const named=(error:unknown)=>error instanceof JobRefused||error instanceof LeaseRefused||error instanceof WaitRefused?error.reason:undefined
async function refuses(property:string,attempt:()=>Promise<unknown>,reason:string):Promise<void>{
 try{await attempt()}catch(error){check(property,named(error)===reason);return}
 throw new StorageConformanceFailure(property)
}
const same=(a:unknown,b:unknown)=>canonicalJSON(a)===canonicalJSON(b)
/** What must answer: a refusal or a throw here is that property broken, whatever it says. */
async function answers<T>(property:string,attempt:()=>Promise<T>):Promise<T>{try{return await attempt()}catch{throw new StorageConformanceFailure(property)}}
const PINS:ResumePins={model:{provider:'kit',model:'kit'},policy:`sha256:${'b'.repeat(64)}`,sources:[],artifacts:[]}

async function jobProperties(create:JobStoreFactory):Promise<void>{
 let at=T0
 const now=()=>new Date(at)
 let store=await create(now)
 check('job.declared',store.jobVersion===1&&(store.jobLifetime==='durable'||store.jobLifetime==='process')&&store.waitVersion===1&&store.leaseVersion===1)
 const restart=async(property:string)=>{const next=await create(now,store);check(property,next!==store);store=next}
 const touched=new Set<string>()
 const open=async(run:string)=>{touched.add(run);const head=await store.commit({runId:run,expectedRevision:null,commitId:`${run}-open`,checkpoint:bytes('{"kit":"job"}'),events:[]});return {revision:head.revision,fence:fenceOf(await store.acquireLease({resource:run,owner:'worker-a',expectedRevision:head.revision,durationMs:HOUR}))}}
 /** A start's claim commit, carrying its job's registration. */
 const claim=(run:string,revision:string,operation:string,key:string,commitId=`${run}-${operation}`)=>({runId:run,expectedRevision:revision,commitId,checkpoint:bytes(`{"kit":"${operation}"}`),events:[],job:{job:jobIdentity(run,operation),operation,source:'kit-source',tool:'kit-start',requestDigest:REQUEST,keyDigest:jobKeyDigest(key)}})
 const register=async(run:string,operation:string,key:string,fence:LeaseFence,revision:string)=>{const receipt=await store.commitFenced(claim(run,revision,operation,key),fence);return {job:jobIdentity(run,operation),revision:receipt.revision}}
 const answerOf=(run:string,job:string,handle='kit-handle')=>({version:1 as const,resource:run,job,generation:1 as const,answer:'started' as const,handle})
 const complete=(run:string,job:string,key:string,over:Partial<JobCompletionEvidence>={}):JobCompletionEvidence=>({version:1,resource:run,job,generation:1,evidence:'done-1',outcome:'succeeded',key,...over})
 const reconcile=(run:string,job:string,key:string,over:Partial<JobReconciliation>={}):JobReconciliation=>({version:1,resource:run,job,generation:1,evidence:'find-1',finding:'not-started',key,...over})
 const parkOf=async(run:string,job:string,wake:string,over:Partial<WaitPark>={}):Promise<WaitPark>=>{const head=(await store.load(run))!;return {version:1,resource:run,wait:'wait-1',reason:'job',pending:{kind:'job',job,generation:1},condition:{event:{kind:'job-completion',subject:job}},continuation:'k',checkpoint:{revision:head.revision,digest:checkpointBytesDigest(head.checkpoint!)},pins:PINS,wakeKeyDigest:wakeKeyDigest(wake),release:[],...over}}
 const readWait=async(run:string):Promise<RunWait>=>(await store.readWait(run,'wait-1'))!

 // Registration: with its claim or not at all, only under a fence.
 const a=await open('run-a')
 const first=await register('run-a','op-1',KEYS[0],a.fence,a.revision)
 const job1=await store.readJob('run-a',first.job)
 check('job.register.commits',job1?.state==='unproven'&&job1.operation==='op-1'&&job1.keyDigest===jobKeyDigest(KEYS[0])&&job1.registered.owner==='worker-a'&&job1.registered.epoch===a.fence.epoch&&job1.registered.at===iso(at)&&(await store.load('run-a'))?.revision===first.revision)
 const unfencedHead=(await store.load('run-a'))!.revision
 await refuses('job.register.unfenced',()=>store.commit(claim('run-a',unfencedHead,'op-unfenced',WRONG)),'invalid')
 check('job.register.unfenced',(await store.readJob('run-a',jobIdentity('run-a','op-unfenced')))===undefined&&(await store.load('run-a'))?.revision===unfencedHead)
 await refuses('job.register.fence',()=>store.commitFenced(claim('run-a',unfencedHead,'op-stale',WRONG),{...a.fence,owner:'worker-x'}),'other-owner')
 check('job.register.fence',(await store.readJob('run-a',jobIdentity('run-a','op-stale')))===undefined)
 await refuses('job.register.identity',()=>store.commitFenced({...claim('run-a',unfencedHead,'op-bad',WRONG),job:{...claim('run-a',unfencedHead,'op-bad',WRONG).job,job:jobIdentity('run-a','op-other')}},a.fence),'invalid')
 check('job.register.identity',(await store.load('run-a'))?.revision===unfencedHead)
 // A digest another job holds: refused, and the claim did not commit either.
 await refuses('job.key.job',()=>store.commitFenced(claim('run-a',unfencedHead,'op-2',KEYS[0]),a.fence),'key-reused')
 check('job.register.atomic',(await store.load('run-a'))?.revision===unfencedHead&&(await store.readJob('run-a',jobIdentity('run-a','op-2')))===undefined)
 // A store with the history capability registers on that path too.
 const history=store as unknown as {historyVersion?:number;commitHistoryFenced?:(input:unknown,fence:LeaseFence)=>Promise<{revision:string}>}
 if(history.historyVersion===1&&history.commitHistoryFenced){
  await history.commitHistoryFenced({run:claim('run-a',unfencedHead,'op-history',KEYS[8]),observations:[]},a.fence)
  check('job.register.history',(await store.readJob('run-a',jobIdentity('run-a','op-history')))?.state==='unproven')
 }
 // The history path, where a store with that capability really commits its claims (the writer's path): every check holds there.
 // Through the store of the moment: a restart replaces it.
 const viaHistory=history.historyVersion===1&&history.commitHistoryFenced?(input:ReturnType<typeof claim>,fence:LeaseFence)=>(store as unknown as Required<typeof history>).commitHistoryFenced({run:input,observations:[]},fence):undefined
 const hist=await open('run-history')
 if(viaHistory){
  await viaHistory(claim('run-history',hist.revision,'op-1',keyFor(100)),hist.fence)
  const moved=(await store.load('run-history'))!.revision
  check('job.history.head',moved!==hist.revision&&(await store.readJob('run-history',jobIdentity('run-history','op-1')))?.state==='unproven')
  await refuses('job.history.fence',()=>viaHistory(claim('run-history',moved,'op-other',WRONG),{...hist.fence,owner:'worker-x'}),'other-owner')
  check('job.history.fence',(await store.readJob('run-history',jobIdentity('run-history','op-other')))===undefined)
  await refuses('job.history.identity',()=>viaHistory({...claim('run-history',moved,'op-bad',WRONG),job:{...claim('run-history',moved,'op-bad',WRONG).job,job:jobIdentity('run-history','op-other')}},hist.fence),'invalid')
  await refuses('job.history.key',()=>viaHistory(claim('run-history',moved,'op-2',keyFor(100)),hist.fence),'key-reused')
  check('job.history.key',(await store.load('run-history'))?.revision===moved&&(await store.readJob('run-history',jobIdentity('run-history','op-2')))===undefined)
 }
 // On both paths: a compare-and-set that fails registers nothing, and a repeated commit id must carry the same registration.
 for(const [path,commit,n] of [['fenced',(input:ReturnType<typeof claim>,fence:LeaseFence)=>store.commitFenced(input,fence),110],['history',viaHistory,120]] as const){
  if(!commit)continue
  const run=`run-cas-${path}`,cas=await open(run),firstKey=keyFor(n),otherKey=keyFor(n+1)
  await commit(claim(run,cas.revision,'op-1',firstKey),cas.fence)
  const after=(await store.load(run))!.revision
  let refusedStale=false
  try{await commit(claim(run,cas.revision,'op-stale',otherKey),cas.fence)}catch{refusedStale=true}
  check(`job.stale.${path}`,refusedStale&&(await store.readJob(run,jobIdentity(run,'op-stale')))===undefined&&(await store.load(run))?.revision===after)
  let refusedOther=false
  try{await commit(claim(run,cas.revision,'op-2',otherKey,`${run}-op-1`),cas.fence)}catch{refusedOther=true}
  check(`job.repeat.${path}`,refusedOther&&(await store.readJob(run,jobIdentity(run,'op-2')))===undefined&&(await store.exportJobs(run)).jobs.length===1)
 }
 // Owner: a job is looked up by its run and its identity together.
 check('job.owner.read',(await store.readJob('run-b',first.job))===undefined)
 check('job.owner.list',(await store.listJobs('run-b',null,10)).jobs.length===0&&(await store.listJobs('run-a',null,10)).jobs.some(row=>row.job===first.job))
 await refuses('job.owner.complete',()=>store.completeJob(complete('run-b',first.job,KEYS[0])),'no-job')
 await refuses('job.owner.reconcile',()=>store.reconcileJob(reconcile('run-b',first.job,KEYS[0])),'no-job')
 // The typed answer: the fence that admitted it, current or not. M2.
 const c=await open('run-c')
 const late=await register('run-c','op-1',KEYS[1],c.fence,c.revision)
 await store.readJob('run-c',late.job);await store.listJobs('run-c',null,10)
 await refuses('job.answer.other',()=>store.answerJob(answerOf('run-c',late.job),{...c.fence,owner:'worker-x'}),'unauthenticated')
 await refuses('job.answer.run',()=>store.answerJob(answerOf('run-c',late.job),{...c.fence,resource:'run-other'}),'unauthenticated')
 await refuses('job.answer.across',()=>store.answerJob(answerOf('run-other',late.job),c.fence),'no-job')
 at+=2*HOUR
 const answered=await answers('job.answer.late',()=>store.answerJob(answerOf('run-c',late.job),c.fence))
 check('job.answer.late',answered.state==='started'&&answered.handle==='kit-handle'&&answered.started?.by==='answer')
 check('job.read.fresh',same(await store.readJob('run-c',late.job),answered))
 check('job.list.fresh',same((await store.listJobs('run-c',null,10)).jobs[0],answered))
 await refuses('job.answer.conflict',()=>store.answerJob(answerOf('run-c',late.job,'kit-other'),c.fence),'conflict')
 check('job.answer.same',same(await answers('job.answer.same',()=>store.answerJob(answerOf('run-c',late.job),c.fence)),answered))
 // Completion and its refusals, with no lease at all.
 const held=(await store.readLease('run-c'))!
 check('job.answer.late',held.expiry<=iso(at))
 await refuses('job.complete.key',()=>store.completeJob(complete('run-c',late.job,WRONG)),'unauthenticated')
 const done=await store.completeJob(complete('run-c',late.job,KEYS[1]))
 check('job.complete.settles',done.state==='completed'&&done.completion?.outcome==='succeeded'&&done.completion.at===iso(at))
 check('job.complete.duplicate',same(await answers('job.complete.duplicate',()=>store.completeJob(complete('run-c',late.job,KEYS[1]))),done))
 await refuses('job.complete.conflict',()=>store.completeJob(complete('run-c',late.job,KEYS[1],{outcome:'failed'})),'conflict')
 await refuses('job.complete.stale',()=>store.completeJob(complete('run-c',late.job,KEYS[1],{evidence:'done-2'})),'stale')
 // Reconciliation: the key and no lease; first write wins; a contradiction is kept. H1, M1.
 const d=await open('run-d')
 const closing=await register('run-d','op-1',KEYS[2],d.fence,d.revision)
 await store.releaseLease(d.fence)
 const unknown=await answers('job.reconcile.no-lease',()=>store.reconcileJob(reconcile('run-d',closing.job,KEYS[2],{evidence:'find-0',finding:'unknown'})))
 check('job.reconcile.unknown',unknown.state==='unproven')
 await refuses('job.reconcile.key',()=>store.reconcileJob(reconcile('run-d',closing.job,WRONG)),'unauthenticated')
 const closed=await store.reconcileJob(reconcile('run-d',closing.job,KEYS[2]))
 check('job.reconcile.no-lease',closed.state==='not-started'&&closed.closed?.evidence==='find-1')
 check('job.reconcile.same',same(await answers('job.reconcile.same',()=>store.reconcileJob(reconcile('run-d',closing.job,KEYS[2]))),closed))
 await refuses('job.reconcile.conflict',()=>store.reconcileJob(reconcile('run-d',closing.job,KEYS[2],{finding:'started',handle:'h'})),'conflict')
 const contradicted=await answers('job.contradiction.completion',()=>store.completeJob(complete('run-d',closing.job,KEYS[2])))
 check('job.contradiction.completion',contradicted.state==='not-started'&&contradicted.contradictions?.[0]?.kind==='completed'&&contradicted.contradictions[0].evidence==='done-1'&&contradicted.contradictions[0].outcome==='succeeded')
 await refuses('job.contradiction.conflict',()=>store.completeJob(complete('run-d',closing.job,KEYS[2],{outcome:'failed'})),'conflict')
 const twice=await store.reconcileJob(reconcile('run-d',closing.job,KEYS[2],{evidence:'find-2',finding:'started',handle:'late'}))
 check('job.contradiction.start',twice.state==='not-started'&&twice.contradictions?.length===2&&twice.contradictions[1]?.handle==='late')
 // A key belongs to one job or one wait, whichever came first.
 const w=await open('run-w')
 await refuses('job.wait.key',async()=>store.parkWait(await parkOf('run-w',first.job,KEYS[0],{}),w.fence),'key-reused')
 check('job.wait.key',(await store.readLease('run-w'))?.state==='held')
 // The re-claim of a grant. H2.
 const e=await open('run-e')
 const reclaim=await register('run-e','op-1',KEYS[3],e.fence,e.revision)
 await refuses('job.reclaim.fence',()=>store.reclaimJob('run-e',reclaim.job,{...e.fence,owner:'worker-x'}),'other-owner')
 check('job.reclaim.fence',(await store.readJob('run-e',reclaim.job))?.reclaimed===undefined)
 const taken=await store.reclaimJob('run-e',reclaim.job,e.fence)
 check('job.reclaim.records',taken.reclaimed?.owner==='worker-a'&&taken.reclaimed.epoch===e.fence.epoch&&taken.state==='unproven')
 await store.reconcileJob(reconcile('run-e',reclaim.job,KEYS[3]))
 await refuses('job.reclaim.settled',()=>store.reclaimJob('run-e',reclaim.job,e.fence),'job-settled')
 // Under an expired and a stale fence of the same owner.
 const ex=await open('run-expired')
 const lapsed=await register('run-expired','op-1',keyFor(130),ex.fence,ex.revision)
 at+=2*HOUR
 await refuses('job.reclaim.expired',()=>store.reclaimJob('run-expired',lapsed.job,ex.fence),'expired')
 check('job.reclaim.expired',(await store.readJob('run-expired',lapsed.job))?.reclaimed===undefined)
 const st=await open('run-stale')
 const staled=await register('run-stale','op-1',keyFor(131),st.fence,st.revision)
 await store.releaseLease(st.fence)
 await store.acquireLease({resource:'run-stale',owner:'worker-a',expectedRevision:staled.revision,durationMs:HOUR})
 await refuses('job.reclaim.stale',()=>store.reclaimJob('run-stale',staled.job,st.fence),'stale-epoch')
 check('job.reclaim.stale',(await store.readJob('run-stale',staled.job))?.reclaimed===undefined)
 // The answer follows the re-claim: the fence that re-sent it answers, and the first one no longer does.
 const f=await open('run-f')
 const moved=await register('run-f','op-1',KEYS[4],f.fence,f.revision)
 await store.releaseLease(f.fence)
 const later=fenceOf(await store.acquireLease({resource:'run-f',owner:'worker-b',expectedRevision:moved.revision,durationMs:HOUR}))
 await store.reclaimJob('run-f',moved.job,later)
 await refuses('job.reclaim.answer',()=>store.answerJob(answerOf('run-f',moved.job),f.fence),'unauthenticated')
 check('job.reclaim.answer',(await store.answerJob(answerOf('run-f',moved.job),later)).state==='started')
 // The park: the job in the park's own transaction. The lost wake-up.
 const g=await open('run-g')
 const unproven=await register('run-g','op-1',KEYS[5],g.fence,g.revision)
 await refuses('job.park.unproven',async()=>store.parkWait(await parkOf('run-g',unproven.job,WAKES[0]),g.fence),'job-unproven')
 check('job.park.unproven',(await store.readLease('run-g'))?.state==='held'&&(await store.readWait('run-g','wait-1'))===undefined)
 await store.answerJob(answerOf('run-g',unproven.job),g.fence)
 await store.completeJob(complete('run-g',unproven.job,KEYS[5]))
 await refuses('job.park.settled',async()=>store.parkWait(await parkOf('run-g',unproven.job,WAKES[0]),g.fence),'job-settled')
 check('job.park.settled',(await store.readLease('run-g'))?.state==='held'&&(await store.readWait('run-g','wait-1'))===undefined)
 await refuses('job.park.absent',async()=>store.parkWait(await parkOf('run-g',jobIdentity('run-g','no-such'),WAKES[0]),g.fence),'no-job')
 // The wake: a completion, in its own transaction; nothing else.
 const i=await open('run-i')
 const waited=await register('run-i','op-1',KEYS[6],i.fence,i.revision)
 await store.answerJob(answerOf('run-i',waited.job),i.fence)
 await store.parkWait(await parkOf('run-i',waited.job,WAKES[2]),i.fence)
 const other=await open('run-other-park')
 await refuses('job.park.other',async()=>store.parkWait(await parkOf('run-other-park',waited.job,keyFor(132)),other.fence),'no-job')
 check('job.park.other',(await store.readWait('run-other-park','wait-1'))===undefined)
 // A digest a wait holds: refused, and the claim did not commit either.
 const headG=(await store.load('run-g'))!.revision
 await refuses('job.key.wait',()=>store.commitFenced(claim('run-g',headG,'op-2',WAKES[2]),g.fence),'key-reused')
 check('job.key.wait',(await store.load('run-g'))?.revision===headG)
 const event:WakeEvidence={version:1,resource:'run-i',wait:'wait-1',evidence:'forged',kind:'event',event:{kind:'job-completion',subject:waited.job},key:WAKES[2]}
 await refuses('job.wake.event',()=>store.wakeWait(event),'mismatch')
 check('job.wake.event',(await readWait('run-i')).state==='parked')
 // A restart keeps every job and every wait as they were, all of each: a job completed before it is read after it.
 const allJobs=async(run:string)=>{const out:RunJob[]=[];for(let after:string|null=null;;){const page:JobPage=await store.listJobs(run,after,100);out.push(...page.jobs);if(page.next===null)return out;after=page.next}}
 const before=new Map<string,{jobs:RunJob[];waits:unknown}>()
 for(const run of touched)before.set(run,{jobs:await allJobs(run),waits:await store.exportWaits(run)})
 await restart('job.restart.instance')
 for(const [run,held] of before){
  for(const job of held.jobs){
   const after=await store.readJob(run,job.job)
   check('job.restart.job',after!==undefined)
   check('job.restart.contradiction',same(after?.contradictions,job.contradictions))
   check('job.restart.record',same(after,job))
  }
  check('job.restart.wait',same(await store.exportWaits(run),held.waits))
 }
 check('job.restart.contradiction',same((await store.readJob('run-d',closing.job))?.contradictions,twice.contradictions))
 const r=await open('run-r')
 await refuses('job.restart.key',()=>store.commitFenced(claim('run-r',r.revision,'op-after',KEYS[0]),r.fence),'key-reused')
 const wokenJob=await store.completeJob(complete('run-i',waited.job,KEYS[6],{outcome:'failed'}))
 const woken=await readWait('run-i')
 check('job.wake.completion',woken.state==='woken'&&woken.disposition?.evidence==='done-1'&&woken.disposition.kind==='event'&&woken.disposition.digest===wokenJob.completion?.digest)
 // A replay wakes nothing: the claimed wait stays claimed.
 const head=(await store.load('run-i'))!
 await store.resumeWait({resource:'run-i',wait:'wait-1',evidence:'done-1',lease:{resource:'run-i',owner:'worker-b',expectedRevision:head.revision,durationMs:HOUR},reconciled:[],revalidation:{version:1,resource:'run-i',wait:'wait-1',pins:pinsDigest(PINS),evidence:`sha256:${'c'.repeat(64)}`,changes:[]}})
 await store.completeJob(complete('run-i',waited.job,KEYS[6],{outcome:'failed'}))
 check('job.wake.duplicate',(await readWait('run-i')).state==='resumed')
 // Late evidence: from the timeout only the timeout ends it, and that does not close the run.
 const j=await open('run-j')
 const timed=await register('run-j','op-1',KEYS[7],j.fence,j.revision)
 await store.answerJob(answerOf('run-j',timed.job),j.fence)
 await store.parkWait(await parkOf('run-j',timed.job,WAKES[3],{timeoutAt:iso(at+MINUTE)}),j.fence)
 at+=MINUTE
 await store.completeJob(complete('run-j',timed.job,KEYS[7]))
 check('job.wake.late',(await readWait('run-j')).state==='parked')
 await store.wakeWait({version:1,resource:'run-j',wait:'wait-1',evidence:'timeout-1',kind:'timeout',key:WAKES[3]})
 const timedOut=await readWait('run-j')
 check('job.timeout.state',timedOut.state==='timed-out')
 const headJ=(await store.load('run-j'))!
 const claimed=await answers('job.timeout.continues',()=>store.resumeWait({resource:'run-j',wait:'wait-1',evidence:'timeout-1',lease:{resource:'run-j',owner:'worker-b',expectedRevision:headJ.revision,durationMs:HOUR},reconciled:[],revalidation:{version:1,resource:'run-j',wait:'wait-1',pins:pinsDigest(PINS),evidence:`sha256:${'c'.repeat(64)}`,changes:[]}}))
 check('job.timeout.continues',claimed.wait.state==='resumed')
 await store.releaseLease(fenceOf(claimed.lease))
 check('job.timeout.continues',(await answers('job.timeout.continues',()=>store.acquireLease({resource:'run-j',owner:'worker-c',expectedRevision:headJ.revision,durationMs:HOUR}))).state==='held')
 // A completion after the wait ended reopens nothing.
 const m=await open('run-m')
 const after=await register('run-m','op-1',KEYS[9],m.fence,m.revision)
 await store.answerJob(answerOf('run-m',after.job),m.fence)
 await store.parkWait(await parkOf('run-m',after.job,WAKES[4],{timeoutAt:iso(at+MINUTE)}),m.fence)
 at+=MINUTE
 await store.wakeWait({version:1,resource:'run-m',wait:'wait-1',evidence:'timeout-1',kind:'timeout',key:WAKES[4]})
 const resumedM=await store.resumeWait({resource:'run-m',wait:'wait-1',evidence:'timeout-1',lease:{resource:'run-m',owner:'worker-b',expectedRevision:(await store.load('run-m'))!.revision,durationMs:HOUR},reconciled:[],revalidation:{version:1,resource:'run-m',wait:'wait-1',pins:pinsDigest(PINS),evidence:`sha256:${'c'.repeat(64)}`,changes:[]}})
 // The run waits on the same job again: the completion wakes that wait, not the one that ended.
 await store.parkWait(await parkOf('run-m',after.job,WAKES[5],{wait:'wait-2'}),fenceOf(resumedM.lease))
 await store.completeJob(complete('run-m',after.job,KEYS[9]))
 check('job.wake.resumed',(await readWait('run-m')).state==='resumed')
 check('job.wake.again',(await store.readWait('run-m','wait-2'))?.state==='woken')
 // From its deadline a job wait wakes for nothing either.
 const n=await open('run-deadline')
 const dated=await register('run-deadline','op-1',keyFor(133),n.fence,n.revision)
 await store.answerJob(answerOf('run-deadline',dated.job),n.fence)
 await store.parkWait(await parkOf('run-deadline',dated.job,keyFor(134),{limits:{waitMs:MINUTE}}),n.fence)
 at+=MINUTE
 await store.completeJob(complete('run-deadline',dated.job,keyFor(133)))
 check('job.wake.deadline',(await readWait('run-deadline')).state==='parked')
 // The run's limit: its sixty-fifth job is refused, with its claim.
 const k=await open('run-k')
 let moving=k.revision
 for(let n=0;n<64;n++)moving=(await store.commitFenced(claim('run-k',moving,`op-${n}`,keyFor(n)),k.fence)).revision
 await refuses('job.limit',()=>store.commitFenced(claim('run-k',moving,'op-64',keyFor(64)),k.fence),'jobs-exhausted')
 check('job.limit',(await store.load('run-k'))?.revision===moving&&(await store.exportJobs('run-k')).jobs.length===64)
 if(viaHistory){
  const l=await open('run-limit-history')
  let rolling=l.revision
  for(let n=0;n<64;n++){await viaHistory(claim('run-limit-history',rolling,`op-${n}`,keyFor(200+n)),l.fence);rolling=(await store.load('run-limit-history'))!.revision}
  await refuses('job.history.limit',()=>viaHistory(claim('run-limit-history',rolling,'op-64',keyFor(264)),l.fence),'jobs-exhausted')
  check('job.history.limit',(await store.load('run-limit-history'))?.revision===rolling&&(await store.exportJobs('run-limit-history')).jobs.length===64)
 }
 const page=await store.listJobs('run-k',null,10)
 check('job.list.page',page.jobs.length===10&&page.next===page.jobs[9]!.job&&(await store.listJobs('run-k',page.next,100)).jobs.length===54)
 // What an export says is what the reads say, run by run.
 for(const run of touched){
  const exported=(await store.exportJobs(run)).jobs
  check('job.export.reads',same(exported,await allJobs(run)))
 }
}
/** Sixty-five keys, each a fresh printable secret with at least sixteen distinct characters. */
const keyFor=(n:number)=>`kit.limit.${String(n).padStart(3,'0')}.Q7z2Xm9LpV4cR8tNb3Yh6Df1`
/** Certifies a store kind's job port (#229). Throws StorageConformanceFailure naming the first property it breaks. */
export async function runStoreJobConformance(create:JobStoreFactory):Promise<void>{
 try{await jobProperties(create)}catch(error){if(error instanceof StorageConformanceFailure)throw error;throw new StorageConformanceFailure('job.operation')}
}
