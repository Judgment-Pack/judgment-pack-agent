import {canonicalJSON} from '../context/chat'
import {StorageConformanceFailure} from './conformance'
import {GOAL_JOURNAL_LIMIT,GOAL_OBJECTIVE_LIMIT,GoalRefused,type GoalCommand,type GoalFinding,type GoalPlanSteps,type GoalStore,type RunGoal} from './goal'
import {JobRefused,jobIdentity,jobKeyDigest,type JobCompletionEvidence} from './job'
import type {JobRunStore} from './jobConformance'
import {LeaseRefused,fenceOf} from './lease'
import {pinsDigest,type ResumePins} from './resume'
import {WaitRefused,checkpointBytesDigest,wakeKeyDigest,type WaitClaim,type WaitPark} from './wait'
/* #230: the goal conformance kit, for a host store kind's goal port beside its lease, wait and job ports. Every guarantee of
 * an objective lives in the store's own transactions: the kit drives the port directly, compares the whole record after each
 * refusal, and names the first property a store breaks. The Go twin is CheckRunStoreGoalConformance in
 * go/context/run_goal_conformance.go. */
export type GoalRunStore=JobRunStore&GoalStore
/** Builds the store under test. The kit hands it the store clock it moves; with `restart`, the factory returns a new instance
 * over the same durable state, as after the store's process restarted. */
export type GoalStoreFactory=(now:()=>Date,restart?:GoalRunStore)=>GoalRunStore|Promise<GoalRunStore>
const T0=Date.parse('2026-01-01T00:00:00.000Z'),MINUTE=60_000,HOUR=60*MINUTE
const iso=(ms:number)=>new Date(ms).toISOString()
const bytes=(text:string)=>new TextEncoder().encode(text)
const KEYS=['kit.goal-a.Q7z2Xm9LpV4cR8tNb3Yh6Df1','kit.goal-b.Hs8Pq3Lz6Wd1Rm9Tc4Vb7Nx2','kit.goal-c.Ka5Ue2Jy8Gt3Wo6Bf9Mn1Sv4','kit.goal-d.Zc3Ri7Oq1Xe5Pk9Ls2Gu6Ty8'] as const
const WAKE='kit.goal-wake.Vx4Hd9Tn2Bq7Ym5Rc1Pz8Lf'
const KEY_UNPROVEN='kit.goal-e.Wd3Gk8Np1Rs6Tb9Hy2Lm5Qc',KEY_V='kit.goal-v.Fj2Ns7Kc4Pw9Rd1Xt6Hb3Mg',KEY_V2='kit.goal-v2.Lq5Ty8Ab3Ze6Wn1Gs4Pc9Uk'
const WAKE_V='kit.goal-wake-v.Hc7Rn2Xp9Kd4Tm1Sb6Wq3',WAKE_V2='kit.goal-wake-v2.Gd8Pt3Zm6Bx1Wk4Ns9Rq2'
const REQUEST=`sha256:${'a'.repeat(64)}`,PLAN_REVISION=`sha256:${'c'.repeat(64)}`
const PINS:ResumePins={model:{provider:'kit',model:'kit'},policy:`sha256:${'b'.repeat(64)}`,sources:[],artifacts:[]}
const PLAN:GoalPlanSteps={id:'kit-plan',revision:PLAN_REVISION,steps:[{step:'step-1',index:0},{step:'step-2',index:1}]}
const check=(property:string,value:unknown)=>{if(!value)throw new StorageConformanceFailure(property)}
const named=(error:unknown)=>error instanceof GoalRefused||error instanceof JobRefused||error instanceof LeaseRefused||error instanceof WaitRefused?error.reason:undefined
async function refuses(property:string,attempt:()=>Promise<unknown>,reason:string):Promise<void>{
 try{await attempt()}catch(error){check(property,named(error)===reason);return}
 throw new StorageConformanceFailure(property)
}
const same=(a:unknown,b:unknown)=>canonicalJSON(a)===canonicalJSON(b)
async function answers<T>(property:string,attempt:()=>Promise<T>):Promise<T>{try{return await attempt()}catch{throw new StorageConformanceFailure(property)}}

async function goalProperties(create:GoalStoreFactory):Promise<void>{
 let at=T0
 const now=()=>new Date(at)
 let store=await create(now)
 check('goal.declared',store.goalVersion===1&&(store.goalLifetime==='durable'||store.goalLifetime==='process')&&store.jobVersion===1&&store.waitVersion===1&&store.leaseVersion===1)
 const restart=async(property:string)=>{const next=await create(now,store);check(property,next!==store);store=next}
 let n=0
 const command=(run:string,kind:GoalCommand['kind'],expectedRevision:number|null,over:Partial<GoalCommand>={}):GoalCommand=>({version:1,resource:run,command:`${run}-command-${++n}`,actor:'kit-person',expectedRevision,kind,...over})
 // An achievement carries the revision its finding was read from: here, the command's own, unless a property says otherwise.
 const send=(c:GoalCommand,over:{plan?:GoalPlanSteps;refused?:GoalFinding;read?:number}={})=>store.commandGoal({version:1,command:c,...(c.kind==='achieve'&&c.expectedRevision!==null?{read:c.expectedRevision}:{}),...over})
 const head=async(run:string)=>{const held=(await store.load(run))!;return {revision:held.revision,digest:checkpointBytesDigest(held.checkpoint!)}}
 const open=async(run:string)=>{const opened=await store.commit({runId:run,expectedRevision:null,commitId:`${run}-open`,checkpoint:bytes(`{"kit":"${run}"}`),events:[]});return {revision:opened.revision,fence:fenceOf(await store.acquireLease({resource:run,owner:'worker-a',expectedRevision:opened.revision,durationMs:HOUR}))}}
 /** A refusal journals the command and changes nothing else: the whole record compared. With no record, nothing is written. */
 const journaled=async(property:string,c:GoalCommand,attempt:()=>Promise<unknown>,reason:string,step?:string)=>{
  const before=await store.readGoal(c.resource)
  await refuses(property,attempt,reason)
  const after=await store.readGoal(c.resource)
  if(!before){check(property,after===undefined);return}
  const last=after?.journal.at(-1)
  check(property,!!after&&same({...after,journal:after.journal.slice(0,-1)},before)&&last?.command===c.command&&last.kind===c.kind&&last.outcome===reason&&last.revision===before.revision&&last.at===iso(at)&&last.step===step)
 }
 /** A refusal that writes nothing at all. */
 const untouched=async(property:string,run:string,attempt:()=>Promise<unknown>,reason:string)=>{const before=await store.readGoal(run);await refuses(property,attempt,reason);check(property,same(await store.readGoal(run),before))}

 // Create: from none, under the store's clock and the command's actor; the record and the answer agree.
 const p=await open('run-p')
 const first=command('run-p','create',null,{objective:'objective-1'})
 const created=await answers('goal.create.commits',()=>send(first))
 const read=await store.readGoal('run-p')
 check('goal.create.commits',created.goal.revision===1&&created.goal.objectives.length===1&&same(created.goal.objectives[0],{id:'objective-1',state:'active',created:{command:first.command,actor:'kit-person',at:iso(at)}})&&created.entry.outcome==='accepted'&&created.entry.revision===1&&same(read,created.goal)&&same(read?.journal,[created.entry]))
 const stale=command('run-p','create',null,{objective:'objective-2'})
 await journaled('goal.create.cas',stale,()=>send(stale),'revision-changed')
 const twice=command('run-p','create',1,{objective:'objective-2'})
 await journaled('goal.create.exists',twice,()=>send(twice),'objective-exists')
 // A repeat answers as the first did; another command under its id is conflict; a refused one is refused again. Neither grows the journal.
 const length=(await store.readGoal('run-p'))!.journal.length
 const again=await answers('goal.repeat',()=>send(first))
 check('goal.repeat',same(again.entry,created.entry)&&(await store.readGoal('run-p'))!.journal.length===length)
 await untouched('goal.conflict','run-p',()=>send({...first,objective:'objective-9'}),'conflict')
 await untouched('goal.refusal.repeat','run-p',()=>send(stale),'revision-changed')
 // An objective's run is leased: an unfenced commit is refused once it has one, and only then.
 const before=(await store.load('run-p'))!.revision
 await refuses('goal.unfenced',()=>store.commit({runId:'run-p',expectedRevision:before,commitId:'run-p-unfenced',checkpoint:bytes('{"kit":"unfenced"}'),events:[]}),'lease-required')
 check('goal.unfenced',(await store.load('run-p'))!.revision===before)
 // The history path too: a store with history commits an objective's run under a fence only.
 const historyPort=store as unknown as {historyVersion?:number;commitHistory?:(input:unknown)=>Promise<unknown>}
 if(historyPort.historyVersion===1&&typeof historyPort.commitHistory==='function'){
  await refuses('goal.unfenced.history',()=>historyPort.commitHistory!({run:{runId:'run-p',expectedRevision:before,commitId:'run-p-unfenced-history',checkpoint:bytes('{"kit":"unfenced"}'),events:[]},observations:[]}),'lease-required')
  check('goal.unfenced.history',(await store.load('run-p'))!.revision===before)
 }
 const plain=await store.commit({runId:'run-plain',expectedRevision:null,commitId:'run-plain-1',checkpoint:bytes('{"kit":"plain"}'),events:[]})
 check('goal.unfenced',!!plain.revision)

 // M8: a pause moves a disposition; it refuses only an acquisition and a resume, and its answer reports the held lease.
 const pause=command('run-p','pause',1)
 const paused=await answers('goal.pause.commits',()=>send(pause))
 check('goal.pause.commits',paused.goal.revision===2&&paused.goal.objectives[0]!.state==='active'&&same(paused.goal.objectives[0]!.paused,{command:pause.command,actor:'kit-person',at:iso(at)})&&paused.lease?.owner==='worker-a'&&paused.lease.epoch===p.fence.epoch&&paused.lease.state==='held')
 const pauseTwice=command('run-p','pause',2)
 await journaled('goal.pause.twice',pauseTwice,()=>send(pauseTwice),'objective-paused')
 const held=await answers('goal.pause.held',()=>store.commitFenced({runId:'run-p',expectedRevision:before,commitId:'run-p-paused',checkpoint:bytes('{"kit":"paused"}'),events:[]},p.fence))
 await answers('goal.pause.held',()=>store.renewLease(p.fence,HOUR))
 await store.releaseLease(p.fence)
 const lease=await store.readLease('run-p')
 await refuses('goal.pause.acquire',()=>store.acquireLease({resource:'run-p',owner:'worker-b',expectedRevision:held.revision,durationMs:HOUR}),'objective-paused')
 check('goal.pause.acquire',same(await store.readLease('run-p'),lease))
 // A resume writes the disposition and nothing else: no head, lease, wait or job moves.
 const resume=command('run-p','resume',2)
 const heads=await store.load('run-p'),jobs=await store.exportJobs('run-p'),waits=await store.exportWaits('run-p')
 const resumed=await answers('goal.resume.commits',()=>send(resume))
 check('goal.resume.commits',resumed.goal.revision===3&&resumed.goal.objectives[0]!.paused===undefined&&(await store.load('run-p'))!.revision===heads!.revision&&same(await store.readLease('run-p'),lease)&&same(await store.exportJobs('run-p'),jobs)&&same(await store.exportWaits('run-p'),waits))
 const notPaused=command('run-p','resume',3)
 await journaled('goal.resume.none',notPaused,()=>send(notPaused),'not-paused')
 const p2=fenceOf(await answers('goal.resume.commits',()=>store.acquireLease({resource:'run-p',owner:'worker-b',expectedRevision:held.revision,durationMs:HOUR})))
 // Blocked is a state, and the only way back is unblock.
 const block=command('run-p','block',3,{condition:'kit-condition'})
 const blocked=await answers('goal.block',()=>send(block))
 check('goal.block',blocked.goal.objectives[0]!.state==='blocked'&&same(blocked.goal.objectives[0]!.blocked,{command:block.command,actor:'kit-person',at:iso(at),condition:'kit-condition'}))
 const blockTwice=command('run-p','block',4,{condition:'kit-condition'})
 await journaled('goal.block.twice',blockTwice,()=>send(blockTwice),'objective-blocked')
 const blockedAchieve=command('run-p','achieve',4,{head:await head('run-p')})
 await journaled('goal.block.achieve',blockedAchieve,()=>send(blockedAchieve),'objective-blocked')
 const unblock=command('run-p','unblock',4)
 const unblocked=await answers('goal.unblock',()=>send(unblock))
 check('goal.unblock',unblocked.goal.objectives[0]!.state==='active'&&unblocked.goal.objectives[0]!.blocked===undefined)
 const unblockTwice=command('run-p','unblock',5)
 await journaled('goal.unblock.none',unblockTwice,()=>send(unblockTwice),'not-blocked')
 const unbound=command('run-p','achieve',5,{head:await head('run-p')})
 await journaled('goal.achieve.unbound',unbound,()=>send(unbound),'plan-unbound')
 // Bind: against the head as the store holds it; a finding of the package is journaled as the refusal; once only.
 const staleHead=command('run-p','bind',5,{steps:['step-1','step-2'],head:{revision:before,digest:checkpointBytesDigest(bytes(`{"kit":"run-p"}`))}})
 await journaled('goal.bind.head',staleHead,()=>send(staleHead,{plan:PLAN}),'head-changed')
 const settled=command('run-p','bind',5,{steps:['step-1','step-2'],head:await head('run-p')})
 await journaled('goal.bind.finding',settled,()=>send(settled,{refused:{reason:'step-settled',step:'step-1'}}),'step-settled','step-1')
 const bind=command('run-p','bind',5,{steps:['step-1','step-2'],head:await head('run-p')})
 const bound=await answers('goal.bind.commits',()=>send(bind,{plan:PLAN}))
 check('goal.bind.commits',bound.goal.revision===6&&same(bound.goal.objectives[0]!.plan,{...PLAN,bound:{command:bind.command,actor:'kit-person',at:iso(at)}}))
 const bindTwice=command('run-p','bind',6,{steps:['step-1'],head:await head('run-p')})
 await journaled('goal.bind.twice',bindTwice,()=>send(bindTwice,{plan:PLAN}),'plan-bound')
 // Achieve: against the head, after the package's finding, with nothing waiting and every job settled.
 const achieveStale=command('run-p','achieve',6,{head:{revision:before,digest:checkpointBytesDigest(bytes(`{"kit":"run-p"}`))}})
 await journaled('goal.achieve.head',achieveStale,()=>send(achieveStale),'head-changed')
 // H-1: a finding read from another revision of the record is no finding of this one.
 const achieveRead=command('run-p','achieve',6,{head:await head('run-p')})
 await journaled('goal.achieve.read',achieveRead,()=>send(achieveRead,{read:5}),'revision-changed')
 const achieveFound=command('run-p','achieve',6,{head:await head('run-p')})
 await journaled('goal.achieve.finding',achieveFound,()=>send(achieveFound,{refused:{reason:'evidence-refused',step:'step-2'}}),'evidence-refused','step-2')
 const achieve=command('run-p','achieve',6,{head:await head('run-p')})
 const achieved=await answers('goal.achieve.commits',()=>send(achieve))
 check('goal.achieve.commits',achieved.goal.objectives[0]!.state==='achieved'&&same(achieved.goal.objectives[0]!.achieved,{command:achieve.command,actor:'kit-person',at:iso(at),head:achieve.head}))
 const terminal=command('run-p','pause',7)
 await journaled('goal.terminal',terminal,()=>send(terminal),'objective-terminal')
 // An objective's run stays leased once the objective has ended.
 const endedHead=(await store.load('run-p'))!.revision
 await refuses('goal.unfenced.terminal',()=>store.commit({runId:'run-p',expectedRevision:endedHead,commitId:'run-p-unfenced-terminal',checkpoint:bytes('{"kit":"unfenced"}'),events:[]}),'lease-required')
 check('goal.unfenced.terminal',(await store.load('run-p'))!.revision===endedHead)
 // M11: a later objective of the run never binds a Plan an earlier one bound.
 const reused=command('run-p','create',7,{objective:'objective-1'})
 await journaled('goal.create.reused',reused,()=>send(reused),'objective-reused')
 const second=await answers('goal.create.terminal',()=>send(command('run-p','create',7,{objective:'objective-2'})))
 check('goal.create.terminal',second.goal.objectives.length===2&&second.goal.objectives[0]!.state==='achieved'&&second.goal.objectives[1]!.state==='active')
 const rebind=command('run-p','bind',8,{steps:['step-1'],head:await head('run-p')})
 await journaled('goal.bind.reused',rebind,()=>send(rebind,{plan:{...PLAN,steps:[PLAN.steps[0]!]}}),'plan-reused')
 // H-2: a repeat is answered with the record as it is now, so that the package can see what has happened since.
 const nowP=await store.readGoal('run-p')
 const repeatNow=await answers('goal.repeat.current',()=>send(first))
 check('goal.repeat.current',same(repeatNow.entry,created.entry)&&same(repeatNow.goal,nowP))

 // Jobs at an achievement, two of them and a contradiction: unsettled ones block unless waived; failed ones are listed.
 const j=await open('run-j')
 await answers('goal.achieve.jobs',()=>send(command('run-j','create',null,{objective:'objective-j'})))
 let revision=(await store.load('run-j'))!.revision
 const register=async(operation:string,key:string)=>{const receipt=await store.commitFenced({runId:'run-j',expectedRevision:revision,commitId:`run-j-${operation}`,checkpoint:bytes(`{"kit":"${operation}"}`),events:[],job:{job:jobIdentity('run-j',operation),operation,source:'kit-source',tool:'kit-start',requestDigest:REQUEST,keyDigest:jobKeyDigest(key)}},j.fence);revision=receipt.revision;return jobIdentity('run-j',operation)}
 const complete=(job:string,key:string,over:Partial<JobCompletionEvidence>={}):JobCompletionEvidence=>({version:1,resource:'run-j',job,generation:1,evidence:'done-1',outcome:'succeeded',key,...over})
 const running=await register('op-1',KEYS[0]),failed=await register('op-2',KEYS[1]),contradicted=await register('op-3',KEYS[2]),pending=await register('op-4',KEY_UNPROVEN)
 await store.answerJob({version:1,resource:'run-j',job:running,generation:1,answer:'started',handle:'kit-handle'},j.fence)
 await store.answerJob({version:1,resource:'run-j',job:failed,generation:1,answer:'started',handle:'kit-handle'},j.fence)
 await store.completeJob(complete(failed,KEYS[1],{outcome:'failed'}))
 await store.reconcileJob({version:1,resource:'run-j',job:contradicted,generation:1,evidence:'find-1',finding:'not-started',key:KEYS[2]})
 await store.completeJob(complete(contradicted,KEYS[2]))
 const jobHead=await head('run-j')
 await answers('goal.achieve.jobs',()=>send(command('run-j','bind',1,{steps:['step-1'],head:jobHead}),{plan:{...PLAN,steps:[PLAN.steps[0]!]}}))
 const unsettled=command('run-j','achieve',2,{head:jobHead})
 await journaled('goal.achieve.jobs',unsettled,()=>send(unsettled),'jobs-unsettled')
 const waivedSettled=command('run-j','achieve',2,{head:jobHead,waive:[failed]})
 await journaled('goal.achieve.waiver',waivedSettled,()=>send(waivedSettled),'waiver-invalid')
 // An unproven job may be running: it is unsettled, as a started one is.
 const unprovenOpen=command('run-j','achieve',2,{head:jobHead,waive:[running]})
 await journaled('goal.achieve.unproven',unprovenOpen,()=>send(unprovenOpen),'jobs-unsettled')
 const waived=command('run-j','achieve',2,{head:jobHead,waive:[running,pending]})
 const done=await answers('goal.achieve.jobs',()=>send(waived))
 check('goal.achieve.jobs',same(done.goal.objectives[0]!.achieved,{command:waived.command,actor:'kit-person',at:iso(at),head:jobHead,waived:[running,pending],unsuccessful:[{job:failed,state:'completed',outcome:'failed'},{job:contradicted,state:'not-started',contradicted:true}]}))
 // Owner: a record is looked up by its run.
 check('goal.owner',(await store.readGoal('run-other'))===undefined&&(await store.readGoal('run-j'))!.objectives[0]!.id==='objective-j')

 // Waits: an achievement waits for none; an abandon records what it left; a later create is held to it (M10).
 const w=await open('run-w')
 await answers('goal.achieve.wait',()=>send(command('run-w','create',null,{objective:'objective-w'})))
 const wReceipt=await store.commitFenced({runId:'run-w',expectedRevision:w.revision,commitId:'run-w-op-1',checkpoint:bytes('{"kit":"run-w-op-1"}'),events:[],job:{job:jobIdentity('run-w','op-1'),operation:'op-1',source:'kit-source',tool:'kit-start',requestDigest:REQUEST,keyDigest:jobKeyDigest(KEYS[3])}},w.fence)
 const waitJob=jobIdentity('run-w','op-1')
 await store.answerJob({version:1,resource:'run-w',job:waitJob,generation:1,answer:'started',handle:'kit-handle'},w.fence)
 const park:WaitPark={version:1,resource:'run-w',wait:'wait-1',reason:'job',pending:{kind:'job',job:waitJob,generation:1},condition:{event:{kind:'job-completion',subject:waitJob}},continuation:'k',checkpoint:{revision:wReceipt.revision,digest:checkpointBytesDigest(bytes('{"kit":"run-w-op-1"}'))},pins:PINS,wakeKeyDigest:wakeKeyDigest(WAKE),release:[]}
 await store.parkWait(park,w.fence)
 const wHead=await head('run-w')
 await answers('goal.achieve.wait',()=>send(command('run-w','bind',1,{steps:['step-1'],head:wHead}),{plan:{...PLAN,id:'kit-plan-w'}}))
 const waiting=command('run-w','achieve',2,{head:wHead,waive:[waitJob]})
 await journaled('goal.achieve.wait',waiting,()=>send(waiting),'wait-pending')
 const abandon=command('run-w','abandon',2)
 const abandoned=await answers('goal.abandon.left',()=>send(abandon))
 check('goal.abandon.left',same(abandoned.goal.objectives[0]!.abandoned,{command:abandon.command,actor:'kit-person',at:iso(at),waits:[{wait:'wait-1',reason:'job',state:'parked'}],jobs:[{job:waitJob,state:'started'}]})&&abandoned.goal.objectives[0]!.state==='abandoned')
 const unresolved=command('run-w','create',3,{objective:'objective-w2'})
 await journaled('goal.create.unresolved',unresolved,()=>send(unresolved),'wait-unresolved')
 // The abandon's deliveries are journaled on the objective that command abandoned, and only there.
 const deliveries=[{job:waitJob,delivery:'sent' as const}]
 const delivered=await answers('goal.deliveries',()=>store.recordGoalDeliveries({version:1,resource:'run-w',command:abandon.command,deliveries}))
 const last=delivered.journal.at(-1)
 check('goal.deliveries',last?.outcome==='delivered'&&last.kind==='abandon'&&last.command===abandon.command&&same(last.deliveries,deliveries)&&delivered.revision===3)
 await untouched('goal.deliveries.other','run-w',()=>store.recordGoalDeliveries({version:1,resource:'run-w',command:'run-w-other',deliveries}),'invalid')
 // The wait resolved, the create goes through.
 await store.completeJob({version:1,resource:'run-w',job:waitJob,generation:1,evidence:'done-1',outcome:'succeeded',key:KEYS[3]})
 const claim:WaitClaim={resource:'run-w',wait:'wait-1',evidence:'done-1',lease:{resource:'run-w',owner:'worker-b',expectedRevision:wReceipt.revision,durationMs:HOUR},reconciled:[],revalidation:{version:1,resource:'run-w',wait:'wait-1',pins:pinsDigest(PINS),evidence:'sha256:'+'e'.repeat(64),changes:[]}}
 await answers('goal.create.unresolved',()=>store.resumeWait(claim))
 const recreated=await answers('goal.create.unresolved',()=>send(command('run-w','create',3,{objective:'objective-w2'})))
 check('goal.create.unresolved',recreated.goal.objectives.length===2&&recreated.goal.objectives[1]!.state==='active')

 // A woken wait is unresolved at an achievement, an abandon and a create; and a wait parked after the abandon holds the next
 // create too (M-1).
 const v=await open('run-v')
 await answers('goal.achieve.woken',()=>send(command('run-v','create',null,{objective:'objective-v'})))
 const vReceipt=await store.commitFenced({runId:'run-v',expectedRevision:v.revision,commitId:'run-v-op-1',checkpoint:bytes('{"kit":"run-v-op-1"}'),events:[],job:{job:jobIdentity('run-v','op-1'),operation:'op-1',source:'kit-source',tool:'kit-start',requestDigest:REQUEST,keyDigest:jobKeyDigest(KEY_V)}},v.fence)
 const vJob=jobIdentity('run-v','op-1')
 await store.answerJob({version:1,resource:'run-v',job:vJob,generation:1,answer:'started',handle:'kit-handle'},v.fence)
 await store.parkWait({...park,resource:'run-v',pending:{kind:'job',job:vJob,generation:1},condition:{event:{kind:'job-completion',subject:vJob}},checkpoint:{revision:vReceipt.revision,digest:checkpointBytesDigest(bytes('{"kit":"run-v-op-1"}'))},wakeKeyDigest:wakeKeyDigest(WAKE_V)},v.fence)
 await store.completeJob({version:1,resource:'run-v',job:vJob,generation:1,evidence:'done-1',outcome:'succeeded',key:KEY_V})
 check('goal.achieve.woken',(await store.readWait('run-v','wait-1'))?.state==='woken')
 const vHead=await head('run-v')
 await answers('goal.achieve.woken',()=>send(command('run-v','bind',1,{steps:['step-1'],head:vHead}),{plan:{...PLAN,id:'kit-plan-v'}}))
 const wokenAchieve=command('run-v','achieve',2,{head:vHead})
 await journaled('goal.achieve.woken',wokenAchieve,()=>send(wokenAchieve),'wait-pending')
 const vAbandoned=await answers('goal.abandon.woken',()=>send(command('run-v','abandon',2)))
 check('goal.abandon.woken',same(vAbandoned.goal.objectives[0]!.abandoned?.waits,[{wait:'wait-1',reason:'job',state:'woken'}]))
 const wokenCreate=command('run-v','create',3,{objective:'objective-v2'})
 await journaled('goal.create.woken',wokenCreate,()=>send(wokenCreate),'wait-unresolved')
 const vClaimed=await answers('goal.create.later',()=>store.resumeWait({...claim,resource:'run-v',lease:{...claim.lease,resource:'run-v',expectedRevision:vReceipt.revision},revalidation:{...claim.revalidation,resource:'run-v'}}))
 const vFence=fenceOf(vClaimed.lease)
 const vLater=await store.commitFenced({runId:'run-v',expectedRevision:vReceipt.revision,commitId:'run-v-op-2',checkpoint:bytes('{"kit":"run-v-op-2"}'),events:[],job:{job:jobIdentity('run-v','op-2'),operation:'op-2',source:'kit-source',tool:'kit-start',requestDigest:REQUEST,keyDigest:jobKeyDigest(KEY_V2)}},vFence)
 const vJob2=jobIdentity('run-v','op-2')
 await store.answerJob({version:1,resource:'run-v',job:vJob2,generation:1,answer:'started',handle:'kit-handle'},vFence)
 await store.parkWait({...park,resource:'run-v',wait:'wait-2',pending:{kind:'job',job:vJob2,generation:1},condition:{event:{kind:'job-completion',subject:vJob2}},checkpoint:{revision:vLater.revision,digest:checkpointBytesDigest(bytes('{"kit":"run-v-op-2"}'))},wakeKeyDigest:wakeKeyDigest(WAKE_V2)},vFence)
 const laterCreate=command('run-v','create',3,{objective:'objective-v2'})
 await journaled('goal.create.later',laterCreate,()=>send(laterCreate),'wait-unresolved')
 // An objective that ends paused keeps no pause: its run can be leased again.
 const z=await open('run-z')
 await answers('goal.terminal.pause',()=>send(command('run-z','create',null,{objective:'objective-z'})))
 await answers('goal.terminal.pause',()=>send(command('run-z','pause',1)))
 const zGone=await answers('goal.terminal.pause',()=>send(command('run-z','abandon',2)))
 await store.releaseLease(z.fence)
 check('goal.terminal.pause',zGone.goal.objectives[0]!.paused===undefined&&(await store.readGoal('run-z'))!.objectives[0]!.paused===undefined)
 const zHead=(await store.load('run-z'))!.revision
 await answers('goal.terminal.pause',()=>store.acquireLease({resource:'run-z',owner:'worker-b',expectedRevision:zHead,durationMs:HOUR}))

 // A paused run is resumed by no claim either.
 const q=await open('run-q')
 await answers('goal.pause.resume-wait',()=>send(command('run-q','create',null,{objective:'objective-q'})))
 const qReceipt=await store.commitFenced({runId:'run-q',expectedRevision:q.revision,commitId:'run-q-op-1',checkpoint:bytes('{"kit":"run-q-op-1"}'),events:[],job:{job:jobIdentity('run-q','op-1'),operation:'op-1',source:'kit-source',tool:'kit-start',requestDigest:REQUEST,keyDigest:jobKeyDigest('kit.goal-q.Mb6Sj1Wg8Ku3Ne7Ax2Qo5Di')}},q.fence)
 const qJob=jobIdentity('run-q','op-1')
 await store.answerJob({version:1,resource:'run-q',job:qJob,generation:1,answer:'started',handle:'kit-handle'},q.fence)
 await store.parkWait({...park,resource:'run-q',pending:{kind:'job',job:qJob,generation:1},condition:{event:{kind:'job-completion',subject:qJob}},checkpoint:{revision:qReceipt.revision,digest:checkpointBytesDigest(bytes('{"kit":"run-q-op-1"}'))},wakeKeyDigest:wakeKeyDigest('kit.goal-qw.Ct9Fh4Lp7Xr2Gv6Jz1Ys3Em')},q.fence)
 await store.completeJob({version:1,resource:'run-q',job:qJob,generation:1,evidence:'done-1',outcome:'succeeded',key:'kit.goal-q.Mb6Sj1Wg8Ku3Ne7Ax2Qo5Di'})
 await answers('goal.pause.resume-wait',()=>send(command('run-q','pause',1)))
 const wait=await store.readWait('run-q','wait-1')
 await refuses('goal.pause.resume-wait',()=>store.resumeWait({...claim,resource:'run-q',lease:{...claim.lease,resource:'run-q',expectedRevision:qReceipt.revision},revalidation:{...claim.revalidation,resource:'run-q'}}),'objective-paused')
 check('goal.pause.resume-wait',same(await store.readWait('run-q','wait-1'),wait)&&wait?.state==='woken')

 // No record: a command is refused and nothing is written; an inspection reads nothing.
 await untouched('goal.none','run-none',()=>send(command('run-none','pause',null)),'no-objective')
 await untouched('goal.none','run-none',()=>send(command('run-none','pause',1)),'revision-changed')
 check('goal.inspect.none',(await store.inspectGoal({version:1,resource:'run-none',command:'look-0',actor:'kit-viewer'}))===undefined&&(await store.readGoal('run-none'))===undefined)
 // M12: an inspection is counted, coalesced into its last row, and moves no revision.
 const looked=(await store.readGoal('run-p'))!
 await store.inspectGoal({version:1,resource:'run-p',command:'look-1',actor:'kit-viewer'})
 await store.inspectGoal({version:1,resource:'run-p',command:'look-1',actor:'kit-viewer'})
 at+=MINUTE
 const inspected=await answers('goal.inspect.count',()=>store.inspectGoal({version:1,resource:'run-p',command:'look-2',actor:'kit-viewer'}))
 check('goal.inspect.count',same(inspected?.inspections,{count:2,last:{command:'look-2',actor:'kit-viewer',at:iso(at)}})&&inspected!.revision===looked.revision&&same(inspected!.journal,looked.journal)&&same(await store.readGoal('run-p'),inspected))
 // M12: past the journal's bound, refusals go first, and no command is refused for size.
 const m=await open('run-m')
 const mFirst=await answers('goal.journal.bound',()=>send(command('run-m','create',null,{objective:'objective-m'})))
 for(let i=0;i<GOAL_JOURNAL_LIMIT+6;i++)await refuses('goal.journal.bound',()=>send(command('run-m','resume',1)),'not-paused')
 const full=(await store.readGoal('run-m'))!
 check('goal.journal.bound',full.journal.length===GOAL_JOURNAL_LIMIT&&full.discarded===7&&same(full.journal[0],mFirst.entry))
 const mPause=await answers('goal.journal.bound',()=>send(command('run-m','pause',1)))
 await store.releaseLease(m.fence)
 check('goal.journal.bound',mPause.goal.journal.length===GOAL_JOURNAL_LIMIT&&mPause.goal.discarded===8&&same(mPause.goal.journal[0],mFirst.entry)&&same(mPause.goal.journal.at(-1),mPause.entry))
 // A run's objectives are bounded, and the bound is a named refusal.
 await open('run-x')
 let xRevision:number|null=null
 for(let i=0;i<GOAL_OBJECTIVE_LIMIT;i++){
  const made=await answers('goal.exhausted',()=>send(command('run-x','create',xRevision,{objective:`objective-x${i}`})))
  const gone=await answers('goal.exhausted',()=>send(command('run-x','abandon',made.goal.revision)))
  xRevision=gone.goal.revision
 }
 const exhausted=command('run-x','create',xRevision,{objective:'objective-x-last'})
 await journaled('goal.exhausted',exhausted,()=>send(exhausted),'goals-exhausted')
 void p2

 // A restart keeps every record, every disposition and every rule.
 const records:Record<string,RunGoal|undefined>={}
 for(const run of ['run-p','run-j','run-w','run-q','run-m','run-x'])records[run]=await store.readGoal(run)
 await restart('goal.restart.instance')
 for(const [run,record] of Object.entries(records))check('goal.restart.record',same(await store.readGoal(run),record))
 const mHead=(await store.load('run-m'))!.revision
 await refuses('goal.restart.paused',()=>store.acquireLease({resource:'run-m',owner:'worker-z',expectedRevision:mHead,durationMs:HOUR}),'objective-paused')
 await refuses('goal.restart.unfenced',()=>store.commit({runId:'run-m',expectedRevision:mHead,commitId:'run-m-unfenced',checkpoint:bytes('{"kit":"unfenced"}'),events:[]}),'lease-required')
 const repeated=await answers('goal.restart.repeat',()=>send(waived))
 check('goal.restart.repeat',same(repeated.entry,done.entry))
 await untouched('goal.restart.conflict','run-j',()=>send({...waived,waive:[failed]}),'conflict')
 const after=command('run-m','resume',2)
 const resumedAfter=await answers('goal.restart.commits',()=>send(after))
 check('goal.restart.commits',resumedAfter.goal.revision===3&&resumedAfter.goal.objectives[0]!.paused===undefined)
 await answers('goal.restart.commits',()=>store.acquireLease({resource:'run-m',owner:'worker-z',expectedRevision:mHead,durationMs:HOUR}))
}
/** Runs the goal properties against a store kind. A store whose records end with its process fails the restart properties. */
export async function runStoreGoalConformance(create:GoalStoreFactory):Promise<void>{
 try{await goalProperties(create)}catch(error){if(error instanceof StorageConformanceFailure)throw error;throw new StorageConformanceFailure('goal.operation')}
}
