import {canonicalJSON} from '../context/chat'
import {StorageConformanceFailure} from './conformance'
import {JOB_ABANDON_OWNER,JobRefused,jobCancelOperation,jobIdentity,jobKeyDigest,type JobCancelAcknowledgement,type JobCancelAsk,type JobCompletionEvidence,type JobPage,type JobReconciliation,type JobStore,type RunJob} from './job'
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
 try{await attempt()}catch(error){check(property,reason===''||named(error)===reason);return}
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
 const allJobs=async(run:string)=>{const out:RunJob[]=[];for(let after:string|null=null;;){const page:JobPage=await store.listJobs(run,after,100);out.push(...page.jobs);if(page.next===null)return out;after=page.next}}
 /** A refusal changes nothing: the job reads as it did. */
 const unchanged=async(run:string,job:string,property:string,attempt:()=>Promise<unknown>,reason:string)=>{const before=await store.readJob(run,job);await refuses(property,attempt,reason);check(property,same(await store.readJob(run,job),before))}

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
 await unchanged('run-c',late.job,'job.answer.other',()=>store.answerJob(answerOf('run-c',late.job),{...c.fence,owner:'worker-x'}),'unauthenticated')
 await unchanged('run-c',late.job,'job.answer.epoch',()=>store.answerJob(answerOf('run-c',late.job),{...c.fence,epoch:String(Number(c.fence.epoch)+1)}),'unauthenticated')
 await unchanged('run-c',late.job,'job.answer.run',()=>store.answerJob(answerOf('run-c',late.job),{...c.fence,resource:'run-other'}),'unauthenticated')
 await unchanged('run-c',late.job,'job.answer.across',()=>store.answerJob(answerOf('run-other',late.job),c.fence),'no-job')
 at+=2*HOUR
 const answered=await answers('job.answer.late',()=>store.answerJob(answerOf('run-c',late.job),c.fence))
 check('job.answer.late',answered.state==='started'&&answered.handle==='kit-handle'&&answered.started?.by==='answer')
 check('job.read.fresh',same(await store.readJob('run-c',late.job),answered))
 check('job.list.fresh',same((await store.listJobs('run-c',null,10)).jobs[0],answered))
 await unchanged('run-c',late.job,'job.answer.conflict',()=>store.answerJob(answerOf('run-c',late.job,'kit-other'),c.fence),'conflict')
 check('job.answer.same',same(await answers('job.answer.same',()=>store.answerJob(answerOf('run-c',late.job),c.fence)),answered))
 // Completion and its refusals, with no lease at all.
 const held=(await store.readLease('run-c'))!
 check('job.answer.late',held.expiry<=iso(at))
 await unchanged('run-c',late.job,'job.complete.key',()=>store.completeJob(complete('run-c',late.job,WRONG)),'unauthenticated')
 const done=await store.completeJob(complete('run-c',late.job,KEYS[1]))
 check('job.complete.settles',done.state==='completed'&&done.completion?.outcome==='succeeded'&&done.completion.at===iso(at))
 check('job.read.completed',same(await store.readJob('run-c',late.job),done))
 check('job.complete.duplicate',same(await answers('job.complete.duplicate',()=>store.completeJob(complete('run-c',late.job,KEYS[1]))),done))
 await unchanged('run-c',late.job,'job.complete.conflict',()=>store.completeJob(complete('run-c',late.job,KEYS[1],{outcome:'failed'})),'conflict')
 await unchanged('run-c',late.job,'job.complete.stale',()=>store.completeJob(complete('run-c',late.job,KEYS[1],{evidence:'done-2'})),'stale')
 // Reconciliation: the key and no lease; first write wins; a contradiction is kept. H1, M1.
 const d=await open('run-d')
 const closing=await register('run-d','op-1',KEYS[2],d.fence,d.revision)
 await store.releaseLease(d.fence)
 const unknown=await answers('job.reconcile.no-lease',()=>store.reconcileJob(reconcile('run-d',closing.job,KEYS[2],{evidence:'find-0',finding:'unknown'})))
 check('job.reconcile.unknown',unknown.state==='unproven')
 await unchanged('run-d',closing.job,'job.reconcile.key',()=>store.reconcileJob(reconcile('run-d',closing.job,WRONG)),'unauthenticated')
 const closed=await store.reconcileJob(reconcile('run-d',closing.job,KEYS[2]))
 check('job.reconcile.no-lease',closed.state==='not-started'&&closed.closed?.evidence==='find-1')
 check('job.read.closed',same(await store.readJob('run-d',closing.job),closed))
 check('job.reconcile.same',same(await answers('job.reconcile.same',()=>store.reconcileJob(reconcile('run-d',closing.job,KEYS[2]))),closed))
 await unchanged('run-d',closing.job,'job.reconcile.conflict',()=>store.reconcileJob(reconcile('run-d',closing.job,KEYS[2],{finding:'started',handle:'h'})),'conflict')
 const contradicted=await answers('job.contradiction.completion',()=>store.completeJob(complete('run-d',closing.job,KEYS[2])))
 check('job.contradiction.completion',contradicted.state==='not-started'&&contradicted.contradictions?.[0]?.kind==='completed'&&contradicted.contradictions[0].evidence==='done-1'&&contradicted.contradictions[0].outcome==='succeeded')
 await unchanged('run-d',closing.job,'job.contradiction.conflict',()=>store.completeJob(complete('run-d',closing.job,KEYS[2],{outcome:'failed'})),'conflict')
 const twice=await store.reconcileJob(reconcile('run-d',closing.job,KEYS[2],{evidence:'find-2',finding:'started',handle:'late'}))
 check('job.contradiction.start',twice.state==='not-started'&&twice.contradictions?.length===2&&twice.contradictions[1]?.handle==='late')
 // A key belongs to one job or one wait, whichever came first.
 const w=await open('run-w')
 await refuses('job.wait.key',async()=>store.parkWait(await parkOf('run-w',first.job,KEYS[0],{}),w.fence),'key-reused')
 check('job.wait.key',(await store.readLease('run-w'))?.state==='held')
 // The re-claim of a grant. H2.
 const e=await open('run-e')
 const reclaim=await register('run-e','op-1',KEYS[3],e.fence,e.revision)
 await unchanged('run-e',reclaim.job,'job.reclaim.fence',()=>store.reclaimJob('run-e',reclaim.job,{...e.fence,owner:'worker-x'}),'other-owner')
 check('job.reclaim.fence',(await store.readJob('run-e',reclaim.job))?.reclaimed===undefined)
 const taken=await store.reclaimJob('run-e',reclaim.job,e.fence)
 check('job.reclaim.records',taken.reclaimed?.owner==='worker-a'&&taken.reclaimed.epoch===e.fence.epoch&&taken.state==='unproven')
 await store.reconcileJob(reconcile('run-e',reclaim.job,KEYS[3]))
 await unchanged('run-e',reclaim.job,'job.reclaim.settled',()=>store.reclaimJob('run-e',reclaim.job,e.fence),'job-settled')
 // Under an expired and a stale fence of the same owner.
 const ex=await open('run-expired')
 const lapsed=await register('run-expired','op-1',keyFor(130),ex.fence,ex.revision)
 at+=2*HOUR
 await unchanged('run-expired',lapsed.job,'job.reclaim.expired',()=>store.reclaimJob('run-expired',lapsed.job,ex.fence),'expired')
 check('job.reclaim.expired',(await store.readJob('run-expired',lapsed.job))?.reclaimed===undefined)
 const st=await open('run-stale')
 const staled=await register('run-stale','op-1',keyFor(131),st.fence,st.revision)
 await store.releaseLease(st.fence)
 await store.acquireLease({resource:'run-stale',owner:'worker-a',expectedRevision:staled.revision,durationMs:HOUR})
 await unchanged('run-stale',staled.job,'job.reclaim.stale',()=>store.reclaimJob('run-stale',staled.job,st.fence),'stale-epoch')
 check('job.reclaim.stale',(await store.readJob('run-stale',staled.job))?.reclaimed===undefined)
 // A cancel: asked for under a current fence, of a started job, moving no state. A stale fence asks nothing: unlike a start's answer.
 const askOf=(run:string,job:string,operation='cancel-1'):JobCancelAsk=>({version:1,resource:run,job,generation:1,by:'tool',operation})
 const wordOf=(run:string,job:string,key:string,over:Partial<JobCancelAcknowledgement>={}):JobCancelAcknowledgement=>({version:1,resource:run,job,generation:1,evidence:'word-1',outcome:'accepted',receipt:'kit-word-receipt',key,...over})
 const can=await open('run-cancel')
 const canJob=await register('run-cancel','op-1',keyFor(150),can.fence,can.revision)
 await unchanged('run-cancel',canJob.job,'job.cancel.unproven',()=>store.requestJobCancel(askOf('run-cancel',canJob.job),can.fence),'job-unproven')
 await store.answerJob(answerOf('run-cancel',canJob.job),can.fence)
 await unchanged('run-cancel',canJob.job,'job.cancel.none',()=>store.acknowledgeJobCancel(wordOf('run-cancel',canJob.job,keyFor(150))),'no-cancel')
 await unchanged('run-cancel',canJob.job,'job.cancel.fence',()=>store.requestJobCancel(askOf('run-cancel',canJob.job),{...can.fence,owner:'worker-x'}),'other-owner')
 // A second job of the run: the answer must be the job asked for.
 const otherCan=await register('run-cancel','op-2',keyFor(159),can.fence,canJob.revision)
 await store.answerJob(answerOf('run-cancel',otherCan.job,'kit-handle-2'),can.fence)
 const beforeAsk=await store.readJob('run-cancel',canJob.job),otherBefore=await store.readJob('run-cancel',otherCan.job)
 // C6: a fence of another run asks nothing of this run's job.
 const elsewhere=await open('run-cancel-elsewhere')
 await unchanged('run-cancel',canJob.job,'job.cancel.run',()=>store.requestJobCancel(askOf('run-cancel',canJob.job),elsewhere.fence),'resource-mismatch')
 const asked=await answers('job.cancel.records',()=>store.requestJobCancel(askOf('run-cancel',canJob.job),can.fence))
 // F-2b, F-21: the answer is the record asked of, with its handle, marker, source and tool as they were, and the other job is
 // untouched: its record reads whole as before.
 check('job.cancel.which',asked.job===canJob.job&&asked.resource==='run-cancel'&&asked.handle===beforeAsk?.handle&&asked.handleDigested===beforeAsk?.handleDigested&&asked.source===beforeAsk?.source&&asked.tool===beforeAsk?.tool&&same(await store.readJob('run-cancel',otherCan.job),otherBefore))
 check('job.cancel.records',asked.state==='started'&&asked.cancel?.requested.by==='tool'&&asked.cancel.requested.operation==='cancel-1'&&asked.cancel.requested.owner==='worker-a'&&asked.cancel.requested.epoch===can.fence.epoch&&asked.cancel.requested.at===iso(at))
 check('job.cancel.first',same(await answers('job.cancel.first',()=>store.requestJobCancel(askOf('run-cancel',canJob.job,'cancel-2'),can.fence)),asked))
 await unchanged('run-cancel',canJob.job,'job.cancel.key',()=>store.acknowledgeJobCancel(wordOf('run-cancel',canJob.job,WRONG)),'unauthenticated')
 const worded=await answers('job.cancel.word',()=>store.acknowledgeJobCancel(wordOf('run-cancel',canJob.job,keyFor(150))))
 check('job.cancel.word',worded.state==='started'&&worded.cancel?.acknowledged?.outcome==='accepted'&&worded.cancel.acknowledged.evidence==='word-1'&&worded.cancel.acknowledged.at===iso(at)&&worded.cancel.acknowledged.receipt==='kit-word-receipt')
 check('job.cancel.same',same(await answers('job.cancel.same',()=>store.acknowledgeJobCancel(wordOf('run-cancel',canJob.job,keyFor(150)))),worded))
 await unchanged('run-cancel',canJob.job,'job.cancel.conflict',()=>store.acknowledgeJobCancel(wordOf('run-cancel',canJob.job,keyFor(150),{outcome:'refused'})),'conflict')
 await unchanged('run-cancel',canJob.job,'job.cancel.again',()=>store.acknowledgeJobCancel(wordOf('run-cancel',canJob.job,keyFor(150),{evidence:'word-2'})),'stale')
 const stopped=await store.completeJob(complete('run-cancel',canJob.job,keyFor(150),{outcome:'cancelled'}))
 check('job.cancel.completes',stopped.state==='completed'&&same(stopped.cancel,worded.cancel))
 await unchanged('run-cancel',canJob.job,'job.cancel.settled',()=>store.requestJobCancel(askOf('run-cancel',canJob.job,'cancel-3'),can.fence),'job-settled')
 const closedCan=await open('run-cancel-closed')
 const closedJob=await register('run-cancel-closed','op-1',keyFor(151),closedCan.fence,closedCan.revision)
 await store.reconcileJob(reconcile('run-cancel-closed',closedJob.job,keyFor(151)))
 await unchanged('run-cancel-closed',closedJob.job,'job.cancel.closed',()=>store.requestJobCancel(askOf('run-cancel-closed',closedJob.job),closedCan.fence),'job-settled')
 const staleCan=await open('run-cancel-stale')
 const staleJob=await register('run-cancel-stale','op-1',keyFor(152),staleCan.fence,staleCan.revision)
 await store.answerJob(answerOf('run-cancel-stale',staleJob.job),staleCan.fence)
 await store.releaseLease(staleCan.fence)
 const newEpoch=fenceOf(await store.acquireLease({resource:'run-cancel-stale',owner:'worker-a',expectedRevision:staleJob.revision,durationMs:HOUR}))
 await unchanged('run-cancel-stale',staleJob.job,'job.cancel.stale',()=>store.requestJobCancel(askOf('run-cancel-stale',staleJob.job),staleCan.fence),'stale-epoch')
 at+=2*HOUR
 await unchanged('run-cancel-stale',staleJob.job,'job.cancel.expired',()=>store.requestJobCancel(askOf('run-cancel-stale',staleJob.job),newEpoch),'expired')
 // C7: a handle kept as its digest keeps its marker, on the answer and on every read (and across the restart, below).
 const dig=await open('run-digested'),digJob=await register('run-digested','op-1',keyFor(160),dig.fence,dig.revision)
 const digest=`sha256:${'d'.repeat(64)}`
 const digAnswer=await answers('job.answer.digested',()=>store.answerJob({...answerOf('run-digested',digJob.job,digest),handleDigested:true},dig.fence))
 check('job.answer.digested',digAnswer.handleDigested===true&&(await store.readJob('run-digested',digJob.job))?.handleDigested===true&&(await store.listJobs('run-digested',null,10)).jobs[0]?.handleDigested===true)
 // #316 review, MEDIUM-1: a reconciliation's digest keeps its marker as an answer's does (and across the restart, below), so a
 // cancel is never handed the digest as the host's reference.
 const rdig=await open('run-reconciled-digested'),rdigJob=await register('run-reconciled-digested','op-1',keyFor(168),rdig.fence,rdig.revision)
 const rdigFound=await answers('job.reconcile.digested',()=>store.reconcileJob(reconcile('run-reconciled-digested',rdigJob.job,keyFor(168),{finding:'started',handle:digest,handleDigested:true})))
 check('job.reconcile.digested',rdigFound.state==='started'&&rdigFound.handle===digest&&rdigFound.handleDigested===true&&(await store.readJob('run-reconciled-digested',rdigJob.job))?.handleDigested===true&&(await store.listJobs('run-reconciled-digested',null,10)).jobs[0]?.handleDigested===true)
 // C3: an abandon's command never rewrites a tool's first request.
 const fr=await open('run-cancel-first'),firstJob=await register('run-cancel-first','op-1',keyFor(161),fr.fence,fr.revision)
 await store.answerJob(answerOf('run-cancel-first',firstJob.job),fr.fence)
 const toolAsk=await store.requestJobCancel(askOf('run-cancel-first',firstJob.job,'cancel-tool'),fr.fence)
 await answers('job.abandon.first',()=>store.abandonJobs({version:1,resource:'run-cancel-first',command:'cmd-first',durationMs:HOUR}))
 check('job.abandon.first',same((await store.readJob('run-cancel-first',firstJob.job))?.cancel,toolAsk.cancel))
 // An abandon's own transaction: the lease under the reserved owner at a new epoch, and a cancel asked for each started job.
 const ab=await open('run-abandon')
 const abStarted=await register('run-abandon','op-1',keyFor(153),ab.fence,ab.revision)
 const abUnproven=await register('run-abandon','op-2',keyFor(154),ab.fence,abStarted.revision)
 const abDone=await register('run-abandon','op-3',keyFor(155),ab.fence,abUnproven.revision)
 const abSecond=await register('run-abandon','op-4',keyFor(167),ab.fence,abDone.revision)
 await store.answerJob(answerOf('run-abandon',abStarted.job),ab.fence)
 // #325: one started job's handle is a marked digest, so an abandon's answer that drops the marker is not the record
 // (job.abandon.jobs).
 await store.answerJob({...answerOf('run-abandon',abSecond.job,`sha256:${'e'.repeat(64)}`),handleDigested:true},ab.fence)
 await store.answerJob(answerOf('run-abandon',abDone.job),ab.fence)
 await store.completeJob(complete('run-abandon',abDone.job,keyFor(155)))
 const beforeAbandon=(await store.load('run-abandon'))!.revision
 const abTaken=await answers('job.abandon.lease',()=>store.abandonJobs({version:1,resource:'run-abandon',command:'cmd-1',durationMs:HOUR}))
 check('job.abandon.lease',abTaken.lease?.owner===JOB_ABANDON_OWNER&&abTaken.lease.state==='held'&&Number(abTaken.lease.epoch)>Number(ab.fence.epoch)&&(await store.readLease('run-abandon'))?.epoch===abTaken.lease.epoch)
 check('job.abandon.nocommit',(await store.load('run-abandon'))?.revision===beforeAbandon)
 await refuses('job.abandon.epoch',()=>store.commitFenced(claim('run-abandon',beforeAbandon,'op-late',WRONG),ab.fence),'stale-epoch')
 for(const started of [abStarted,abSecond]){
  const abasked=await store.readJob('run-abandon',started.job)
  check('job.abandon.asks',abasked?.state==='started'&&abasked.cancel?.requested.by==='command'&&abasked.cancel.requested.command==='cmd-1'&&abasked.cancel.requested.owner===JOB_ABANDON_OWNER&&abasked.cancel.requested.epoch===abTaken.lease?.epoch)
  check('job.abandon.operation',abasked?.cancel?.requested.operation===jobCancelOperation('cmd-1',started.job))
 }
 // C14: another abandon is another new epoch: the first abandon's fence goes stale too.
 const abAgain=await answers('job.abandon.again',()=>store.abandonJobs({version:1,resource:'run-abandon',command:'cmd-again',durationMs:HOUR}))
 check('job.abandon.again',abAgain.lease!==undefined&&abTaken.lease!==undefined&&Number(abAgain.lease.epoch)>Number(abTaken.lease.epoch))
 check('job.abandon.only',(await store.readJob('run-abandon',abUnproven.job))?.cancel===undefined&&(await store.readJob('run-abandon',abDone.job))?.cancel===undefined)
 check('job.abandon.jobs',same(abAgain.jobs,await allJobs('run-abandon')))
 // F-9: an abandon the store refuses has moved nothing. F-10: one on a run the store has never seen takes no lease.
 const leaseBeforeInvalid=await store.readLease('run-abandon')
 await refuses('job.abandon.invalid',()=>store.abandonJobs({version:1,resource:'run-abandon',command:'',durationMs:HOUR}),'invalid')
 check('job.abandon.invalid',same(await store.readLease('run-abandon'),leaseBeforeInvalid))
 const unseen=await answers('job.abandon.unknown',()=>store.abandonJobs({version:1,resource:'run-abandon-never',command:'cmd-never',durationMs:HOUR}))
 check('job.abandon.unknown',unseen.lease===undefined&&unseen.jobs.length===0&&(await store.readLease('run-abandon-never'))===undefined)
 // #316 review, MEDIUM-3: a run leased before its first commit is a run the store has seen. Its abandon takes the lease at a
 // new epoch, and the worker's first claim, under the fence it held, is refused.
 touched.add('run-abandon-leased')
 const freshFence=fenceOf(await store.acquireLease({resource:'run-abandon-leased',owner:'worker-a',expectedRevision:null,durationMs:HOUR}))
 const freshTaken=await answers('job.abandon.leased',()=>store.abandonJobs({version:1,resource:'run-abandon-leased',command:'cmd-leased',durationMs:HOUR}))
 check('job.abandon.leased',freshTaken.lease?.owner===JOB_ABANDON_OWNER&&Number(freshTaken.lease.epoch)>Number(freshFence.epoch)&&freshTaken.jobs.length===0&&(await store.readLease('run-abandon-leased'))?.epoch===freshTaken.lease.epoch)
 await refuses('job.abandon.leased',()=>store.commitFenced({...claim('run-abandon-leased','','op-1',keyFor(169)),expectedRevision:null},freshFence),'stale-epoch')
 check('job.abandon.leased',(await store.load('run-abandon-leased'))===undefined&&(await allJobs('run-abandon-leased')).length===0)
 // A delegated run's principals stay on the abandon's lease: a takeover is a new epoch of the same principals.
 if((store as {leasePrincipals?:true}).leasePrincipals===true){
  const principal={sub:{kind:'person' as const,issuer:'https://host.example/',subject:'kit-subject'},rootActor:'b1'.repeat(16),host:'kit-host'}
  const head=await store.commit({runId:'run-abandon-principal',expectedRevision:null,commitId:'run-abandon-principal-open',checkpoint:bytes('{"kit":"job"}'),events:[]})
  await store.acquireLease({resource:'run-abandon-principal',owner:'worker-a',expectedRevision:head.revision,durationMs:HOUR,principal})
  const held=await store.abandonJobs({version:1,resource:'run-abandon-principal',command:'cmd-3',durationMs:HOUR})
  check('job.abandon.principal',held.lease?.version===2&&same(held.lease.principal,principal))
 }
 const dl=await open('run-abandon-deadline'),dlJob=await register('run-abandon-deadline','op-1',keyFor(162),dl.fence,dl.revision)
 await store.answerJob(answerOf('run-abandon-deadline',dlJob.job),dl.fence)
 await store.limitRun({version:1,resource:'run-abandon-deadline',run:iso(at+10*MINUTE)})
 const dlTaken=await answers('job.abandon.deadline',()=>store.abandonJobs({version:1,resource:'run-abandon-deadline',command:'cmd-dl',durationMs:HOUR}))
 check('job.abandon.deadline',dlTaken.lease!==undefined&&Date.parse(dlTaken.lease.expiry)<=at+10*MINUTE)
 const pk=await open('run-abandon-parked'),pkJob=await register('run-abandon-parked','op-1',keyFor(163),pk.fence,pk.revision)
 await store.answerJob(answerOf('run-abandon-parked',pkJob.job),pk.fence)
 await store.parkWait(await parkOf('run-abandon-parked',pkJob.job,keyFor(164)),pk.fence)
 const parkedWait=await readWait('run-abandon-parked')
 const pkTaken=await answers('job.abandon.parked',()=>store.abandonJobs({version:1,resource:'run-abandon-parked',command:'cmd-pk',durationMs:HOUR}))
 check('job.abandon.parked',pkTaken.lease?.owner===JOB_ABANDON_OWNER&&(await store.readJob('run-abandon-parked',pkJob.job))?.cancel?.requested.command==='cmd-pk')
 check('job.abandon.wait',same(await readWait('run-abandon-parked'),parkedWait))
 const cl=await open('run-abandon-closed'),clJob=await register('run-abandon-closed','op-1',keyFor(165),cl.fence,cl.revision)
 await store.answerJob(answerOf('run-abandon-closed',clJob.job),cl.fence)
 await store.parkWait(await parkOf('run-abandon-closed',clJob.job,keyFor(166)),cl.fence)
 await store.limitRun({version:1,resource:'run-abandon-closed',run:iso(at+MINUTE)})
 at+=MINUTE
 await store.wakeWait({version:1,resource:'run-abandon-closed',wait:'wait-1',evidence:'deadline-1',kind:'deadline',key:keyFor(166)})
 const clTaken=await answers('job.abandon.closed',()=>store.abandonJobs({version:1,resource:'run-abandon-closed',command:'cmd-cl',durationMs:HOUR}))
 check('job.abandon.closed',clTaken.lease===undefined&&(await store.readJob('run-abandon-closed',clJob.job))?.cancel?.requested.undelivered===true)
 // Past a deadline no lease can be granted: each cancel stays undelivered, and says so.
 const lost=await open('run-abandon-late')
 const lostJob=await register('run-abandon-late','op-1',keyFor(156),lost.fence,lost.revision)
 await store.answerJob(answerOf('run-abandon-late',lostJob.job),lost.fence)
 await store.limitRun({version:1,resource:'run-abandon-late',run:iso(at+MINUTE)})
 at+=MINUTE
 const late2=await answers('job.abandon.past',()=>store.abandonJobs({version:1,resource:'run-abandon-late',command:'cmd-2',durationMs:HOUR}))
 const undelivered=await store.readJob('run-abandon-late',lostJob.job)
 check('job.abandon.past',late2.lease===undefined&&undelivered?.cancel?.requested.undelivered===true&&undelivered.cancel.requested.owner===undefined)
 // The answer follows the re-claim: the fence that re-sent it answers, and the first one no longer does.
 const f=await open('run-f')
 const moved=await register('run-f','op-1',KEYS[4],f.fence,f.revision)
 await store.releaseLease(f.fence)
 const later=fenceOf(await store.acquireLease({resource:'run-f',owner:'worker-b',expectedRevision:moved.revision,durationMs:HOUR}))
 await store.reclaimJob('run-f',moved.job,later)
 await unchanged('run-f',moved.job,'job.reclaim.answer',()=>store.answerJob(answerOf('run-f',moved.job),f.fence),'unauthenticated')
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
 const restartWhole=async()=>{
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
 }
 await restartWhole()
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
 // Neither a cancel nor the host's word on it wakes a wait.
 const quiet=await open('run-cancel-wait')
 const quietJob=await register('run-cancel-wait','op-1',keyFor(157),quiet.fence,quiet.revision)
 await store.answerJob(answerOf('run-cancel-wait',quietJob.job),quiet.fence)
 await store.requestJobCancel({version:1,resource:'run-cancel-wait',job:quietJob.job,generation:1,by:'tool',operation:'cancel-1'},quiet.fence)
 await store.parkWait(await parkOf('run-cancel-wait',quietJob.job,keyFor(158)),quiet.fence)
 await store.acknowledgeJobCancel({version:1,resource:'run-cancel-wait',job:quietJob.job,generation:1,evidence:'word-1',outcome:'accepted',key:keyFor(157)})
 check('job.cancel.nowake',(await readWait('run-cancel-wait')).state==='parked')
 // A completion wakes the wait on its own job, not the run's parked wait whichever job it is for.
 const two=await open('run-two')
 const one=await register('run-two','op-1',keyFor(135),two.fence,two.revision)
 const another=await register('run-two','op-2',keyFor(136),two.fence,one.revision)
 await store.answerJob(answerOf('run-two',one.job),two.fence);await store.answerJob(answerOf('run-two',another.job),two.fence)
 await store.parkWait(await parkOf('run-two',one.job,keyFor(137)),two.fence)
 await store.completeJob(complete('run-two',another.job,keyFor(136)))
 check('job.wake.other',(await readWait('run-two')).state==='parked')
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
 // One more restart, with waits woken, resumed and timed out by now: all read as they were, and every key and commit id holds.
 const timedLast=await open('run-timed')
 const lastJob=await register('run-timed','op-1',keyFor(140),timedLast.fence,timedLast.revision)
 await store.answerJob(answerOf('run-timed',lastJob.job),timedLast.fence)
 await store.parkWait(await parkOf('run-timed',lastJob.job,keyFor(141),{timeoutAt:iso(at+MINUTE)}),timedLast.fence)
 at+=MINUTE
 await store.wakeWait({version:1,resource:'run-timed',wait:'wait-1',evidence:'timeout-1',kind:'timeout',key:keyFor(141)})
 check('job.timeout.state',(await readWait('run-timed')).state==='timed-out')
 const again=await open('run-after')
 const kept=await register('run-after','op-1',keyFor(142),again.fence,again.revision)
 await store.answerJob(answerOf('run-after',kept.job),again.fence)
 await restartWhole()
 const headAfter=(await store.load('run-after'))!.revision
 await refuses('job.restart.commit',()=>store.commitFenced(claim('run-after',headAfter,'op-other',keyFor(143),'run-after-op-1'),again.fence),'')
 check('job.restart.commit',(await store.readJob('run-after',jobIdentity('run-after','op-other')))===undefined&&(await store.load('run-after'))?.revision===headAfter)
 await refuses('job.restart.wakekey',()=>store.commitFenced(claim('run-after',headAfter,'op-wake',keyFor(141)),again.fence),'key-reused')
 await refuses('job.restart.jobkey',async()=>store.parkWait(await parkOf('run-after',kept.job,keyFor(142)),again.fence),'key-reused')
 await unchanged('run-after',kept.job,'job.restart.auth',()=>store.completeJob(complete('run-after',kept.job,WRONG)),'unauthenticated')
}
/** Sixty-five keys, each a fresh printable secret with at least sixteen distinct characters. */
const keyFor=(n:number)=>`kit.limit.${String(n).padStart(3,'0')}.Q7z2Xm9LpV4cR8tNb3Yh6Df1`
/** Certifies a store kind's job port (#229). Throws StorageConformanceFailure naming the first property it breaks. */
export async function runStoreJobConformance(create:JobStoreFactory):Promise<void>{
 try{await jobProperties(create)}catch(error){if(error instanceof StorageConformanceFailure)throw error;throw new StorageConformanceFailure('job.operation')}
}
