import {canonicalJSON} from '../context/chat'
import type {PrincipalBinding,PrincipalReference} from '../principal'
import {StorageConformanceFailure} from './conformance'
import {LeaseRefused,fenceOf,type LeaseFence} from './lease'
import {ResumeRefused,approvalBinding,pinsDigest,type ResumePins} from './resume'
import {WaitRefused,checkpointBytesDigest,wakeKeyDigest,type WaitClaim,type WaitPark,type WaitRunStore,type WakeEvidence} from './wait'
/* #265 M6: the wait conformance kit, for a host store kind's wait port beside its lease port (desk #609 and #670), as
 * #223's kit is for its lease port. Every guarantee of durable waits lives in the store's own transitions, and the
 * package re-checks none of them: the kit drives the port directly and names the first property a store breaks. The Go
 * twin is CheckRunStoreWaitConformance in go/context/run_wait_conformance.go. */

/** Builds the store under test. The kit hands it the store clock it moves; with `restart`, the factory returns a new
 * instance over the same durable state, as after the store's process restarted (a store whose waits end with its
 * process cannot pass). Fixtures are hand authored. */
export type WaitStoreFactory=(now:()=>Date,restart?:WaitRunStore)=>WaitRunStore|Promise<WaitRunStore>

const T0=Date.parse('2026-01-01T00:00:00.000Z'),MINUTE=60_000,HOUR=60*MINUTE
const iso=(ms:number)=>new Date(ms).toISOString()
const bytes=(text:string)=>new TextEncoder().encode(text)
/** An asked act call, as the writer records it, and the checkpoint carrying it. */
const APPROVAL={callId:'call-1',name:'tracker__create_issue',tool:'create_issue',source:{kind:'mcp',id:'tracker',trust:'public',toolListDigest:'sha256:71a65542f0fc25b85f4458bef0b54a2b11d921be4d6ca8cf3c9dc215cd790820',tool:'create_issue'},args:{title:'Mileage'},state:'asked'}
const checkpointWith=(approval:Record<string,unknown>)=>bytes(canonicalJSON({kit:'wait-conformance',approval}))
const PINS:ResumePins={model:{provider:'openai',model:'kit'},policy:'sha256:44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a',sources:[{source:'tracker',descriptorDigest:APPROVAL.source.toolListDigest}],artifacts:[]}
/** One key per wait (#265 M1). */
const KEYS={a:'kit.wait-a.Q7z2Xm9LpV4cR8tNb3Yh6Df1',b:'kit.wait-b.Hs8Pq3Lz6Wd1Rm9Tc4Vb7Nx2',c:'kit.wait-c.Ka5Ue2Jy8Gt3Wo6Bf9Mn1Sv4',d:'kit.wait-d.Zc3Ri7Oq1Xe5Pk9Ls2Gu6Ty8'} as const
/** ADR-0029: a delegated run's principals, and another subject's and root actor's. */
const SUB:PrincipalReference={kind:'person',issuer:'https://kit.example/',subject:'kit-subject'},PRINCIPAL:PrincipalBinding={sub:SUB,rootActor:'b1'.repeat(16),host:'kit-host'}
const OTHER_ACTOR={...PRINCIPAL,rootActor:'b2'.repeat(16)},OTHER_SUBJECT={...PRINCIPAL,sub:{...SUB,subject:'other'}}

const check=(property:string,value:unknown)=>{if(!value)throw new StorageConformanceFailure(property)}
const named=(error:unknown)=>error instanceof WaitRefused||error instanceof LeaseRefused||error instanceof ResumeRefused?error.reason:undefined
async function refuses(property:string,attempt:()=>Promise<unknown>,reason:string):Promise<void>{
 try{await attempt()}catch(error){check(property,named(error)===reason);return}
 throw new StorageConformanceFailure(property)
}

async function waitProperties(create:WaitStoreFactory):Promise<void>{
 let at=T0
 const now=()=>new Date(at)
 let store=await create(now)
 check('wait.declared',store.waitVersion===1&&(store.waitLifetime==='durable'||store.waitLifetime==='process')&&store.leaseVersion===1&&store.durability!=='single-writer-callback')
 // Each restart is a new instance; everything after it is decided by what the store kept.
 const restart=async(property:string)=>{const next=await create(now,store);check(property,next!==store);store=next}
 const asked=checkpointWith(APPROVAL),binding=approvalBinding(APPROVAL as never)
 const open=async(run:string)=>{
  const opened=await store.commit({runId:run,expectedRevision:null,commitId:`${run}-open`,checkpoint:asked,events:[]})
  return {revision:opened.revision,fence:fenceOf(await store.acquireLease({resource:run,owner:'worker-a',expectedRevision:opened.revision,durationMs:HOUR}))}
 }
 const parkOf=(run:string,revision:string,key:string,ttl:number,over:Partial<WaitPark>={}):WaitPark=>({version:1,resource:run,wait:'wait-1',reason:'approval',pending:{kind:'approval',callId:'call-1',name:APPROVAL.name,tool:APPROVAL.tool},condition:{event:{kind:'approval-decision',subject:'call-1'},wakeAt:iso(T0+MINUTE)},limits:{approvalTtlMs:ttl},approval:binding,continuation:`${run}-continue`,checkpoint:{revision,digest:checkpointBytesDigest(asked)},pins:PINS,wakeKeyDigest:wakeKeyDigest(key),release:[],...over})
 const decision=(run:string,key:string,evidence='decision-1'):WakeEvidence=>({version:1,resource:run,wait:'wait-1',evidence,kind:'event',event:{kind:'approval-decision',subject:'call-1'},key})
 const deadline=(run:string,key:string,evidence='deadline-1'):WakeEvidence=>({version:1,resource:run,wait:'wait-1',evidence,kind:'deadline',key})
 const claim=(run:string,evidence:string,owner:string,revision:string):WaitClaim=>({resource:run,wait:'wait-1',evidence,lease:{resource:run,owner,expectedRevision:revision,durationMs:24*HOUR},reconciled:[],revalidation:{version:1,resource:run,wait:'wait-1',pins:pinsDigest(PINS),evidence:'sha256:'+'e'.repeat(64),changes:[]}})
 const commitWith=(run:string,expectedRevision:string,commitId:string,approval:Record<string,unknown>,fence?:LeaseFence)=>{
  const input={runId:run,expectedRevision,commitId,checkpoint:checkpointWith(approval),events:[]}
  return fence?store.commitFenced(input,fence):store.commit(input)
 }

 // The lease release: a park the store refuses changes nothing and leaves the lease held; a committed one releases it in
 // the same transaction, so the parking driver's fence commits nothing more.
 const a=await open('wait-a'),b=await open('wait-b')
 await refuses('wait.park.fence',()=>store.parkWait(parkOf('wait-a',a.revision,KEYS.a,HOUR),{...a.fence,owner:'worker-x'}),'other-owner')
 await refuses('wait.park.head',()=>store.parkWait(parkOf('wait-a',a.revision,KEYS.a,HOUR,{checkpoint:{revision:a.revision,digest:'sha256:'+'0'.repeat(64)}}),a.fence),'checkpoint-changed')
 check('wait.park.refused-unchanged',await store.readWait('wait-a','wait-1')===undefined&&(await store.readLease('wait-a'))?.state==='held')
 const parked=await store.parkWait(parkOf('wait-a',a.revision,KEYS.a,HOUR),a.fence)
 check('wait.park.committed',parked.wait.state==='parked'&&parked.wait.parked.owner==='worker-a'&&parked.wait.parked.epoch===a.fence.epoch&&parked.wait.parked.at===iso(T0)&&parked.wait.deadline?.approval===iso(T0+HOUR)&&parked.attention.id===parked.wait.attention&&parked.attention.needs==='approval-decision')
 check('wait.park.lease-released',(await store.readLease('wait-a'))?.state==='released')
 await refuses('wait.park.fence-ends',()=>store.commitFenced({runId:'wait-a',expectedRevision:a.revision,commitId:'late',checkpoint:asked,events:[]},a.fence),'released')
 check('wait.park.repeat',canonicalJSON(await store.parkWait(parkOf('wait-a',a.revision,KEYS.a,HOUR),a.fence))===canonicalJSON(parked))
 await refuses('wait.park.repeat-other-fence',()=>store.parkWait(parkOf('wait-a',a.revision,KEYS.a,HOUR),{...a.fence,epoch:'9'}),'conflict')

 // The run closure: until its wait is resumed nothing leases the run or commits to it unfenced.
 await refuses('wait.closure.lease',()=>store.acquireLease({resource:'wait-a',owner:'worker-x',expectedRevision:a.revision,durationMs:HOUR}),'wait-pending')
 await refuses('wait.closure.commit',()=>store.commit({runId:'wait-a',expectedRevision:a.revision,commitId:'outside',checkpoint:null,events:[bytes('{}')]}),'wait-pending')

 // The key check: a key belongs to one wait, and a wake presents its own wait's key.
 await refuses('wait.key.reused',()=>store.parkWait(parkOf('wait-b',b.revision,KEYS.a,4*HOUR),b.fence),'key-reused')
 check('wait.key.reused-unchanged',await store.readWait('wait-b','wait-1')===undefined&&(await store.readLease('wait-b'))?.state==='held')
 await store.parkWait(parkOf('wait-b',b.revision,KEYS.b,4*HOUR),b.fence)
 await refuses('wait.key.other-wait',()=>store.wakeWait(decision('wait-b',KEYS.a)),'unauthenticated')
 check('wait.key.unchanged',(await store.readWait('wait-b','wait-1'))?.state==='parked')

 await restart('wait.restart.parked')
 check('wait.restart.parked',(await store.readWait('wait-a','wait-1'))?.state==='parked'&&canonicalJSON((await store.exportWaits('wait-a')).attention)===canonicalJSON([parked.attention]))
 await refuses('wait.restart.closure',()=>store.acquireLease({resource:'wait-a',owner:'worker-x',expectedRevision:a.revision,durationMs:HOUR}),'wait-pending')

 // The single winner: an event and a time wake at once, exactly one wins; a duplicate returns it, anything else is stale.
 at=T0+MINUTE
 const raced=await Promise.allSettled([store.wakeWait(decision('wait-a',KEYS.a)),store.wakeWait({version:1,resource:'wait-a',wait:'wait-1',evidence:'tick-1',kind:'time',key:KEYS.a})])
 const won=raced.filter(r=>r.status==='fulfilled')
 check('wait.wake.one-winner',won.length===1&&raced.some(r=>r.status==='rejected'&&named(r.reason)==='stale'))
 const winner=(won[0] as PromiseFulfilledResult<Awaited<ReturnType<WaitRunStore['wakeWait']>>>).value
 check('wait.wake.duplicate',canonicalJSON(await store.wakeWait(winner.kind==='event'?decision('wait-a',KEYS.a):{version:1,resource:'wait-a',wait:'wait-1',evidence:'tick-1',kind:'time',key:KEYS.a}))===canonicalJSON(winner))
 await refuses('wait.wake.stale',()=>store.wakeWait(decision('wait-a',KEYS.a,'decision-9')),'stale')

 // Claim once: two claims at once, one is granted the run's lease; the claim's lease ends at the approval's expiry.
 const claims=await Promise.allSettled([store.resumeWait(claim('wait-a',winner.evidence,'worker-b',a.revision)),store.resumeWait(claim('wait-a',winner.evidence,'worker-c',a.revision))])
 const granted=claims.filter(r=>r.status==='fulfilled')
 check('wait.claim.once',granted.length===1&&claims.some(r=>r.status==='rejected'&&named(r.reason)==='already-resumed'))
 const claimed=(granted[0] as PromiseFulfilledResult<Awaited<ReturnType<WaitRunStore['resumeWait']>>>).value
 check('wait.claim.result',claimed.wait.state==='resumed'&&claimed.wait.resumed?.owner===claimed.lease.owner&&claimed.wait.resumed.epoch===claimed.lease.epoch&&claimed.lease.epoch==='2'&&claimed.lease.state==='held')
 await refuses('wait.claim.again',()=>store.resumeWait(claim('wait-a',winner.evidence,'worker-d',a.revision)),'already-resumed')
 const fence=fenceOf(claimed.lease)
 check('wait.approval.claim-bounded',claimed.lease.expiry===iso(T0+HOUR))
 check('wait.approval.renew-bounded',(await store.renewLease(fence,24*HOUR)).expiry===iso(T0+HOUR))

 // The commit-time approval binding (#266 N3): fenced or not, a checkpoint that is not the reviewed call is refused.
 const head=(await store.load('wait-a'))!
 await refuses('wait.approval.binding',()=>commitWith('wait-a',head.revision,'rewrite',{...APPROVAL,args:{title:'Something else'}},fence),'approval-changed')
 await refuses('wait.approval.binding-unfenced',()=>commitWith('wait-a',head.revision,'rewrite-unfenced',{...APPROVAL,callId:'call-9'}),'approval-changed')
 const approved=await commitWith('wait-a',head.revision,'approved',{...APPROVAL,state:'approved'},fence)
 await store.releaseLease(fence)

 // The unsettled approval and its binding survive a restart.
 await restart('wait.restart.approval')
 const again=await store.acquireLease({resource:'wait-a',owner:'worker-b',expectedRevision:approved.revision,durationMs:24*HOUR})
 check('wait.restart.approval-ceiling',again.expiry===iso(T0+HOUR))
 await refuses('wait.restart.approval-binding',()=>commitWith('wait-a',approved.revision,'rewrite-after',{...APPROVAL,args:{title:'Something else'}},fenceOf(again)),'approval-changed')
 await store.releaseLease(fenceOf(again))

 // The approval's ceiling (#266 N1): at the expiry nothing leases the run, and the deadline wake, due at it, ends the
 // resumed wait with the call's outcome unknown; the run is closed for good.
 at=T0+HOUR-1
 await refuses('wait.unsettled.not-due',()=>store.wakeWait(deadline('wait-a',KEYS.a)),'not-due')
 at=T0+HOUR
 await refuses('wait.approval.expiry',()=>store.acquireLease({resource:'wait-a',owner:'worker-c',expectedRevision:approved.revision,durationMs:HOUR}),'deadline-passed')
 const ended=await store.wakeWait(deadline('wait-a',KEYS.a))
 check('wait.unsettled.ended',ended.kind==='deadline'&&ended.at===iso(T0+HOUR)&&(await store.readWait('wait-a','wait-1'))?.state==='expired')
 await refuses('wait.closure.closed',()=>store.acquireLease({resource:'wait-a',owner:'worker-c',expectedRevision:approved.revision,durationMs:HOUR}),'closed')
 await refuses('wait.closure.closed-commit',()=>store.commit({runId:'wait-a',expectedRevision:approved.revision,commitId:'closed',checkpoint:null,events:[bytes('{}')]}),'closed')

 // Settlement: the commit that records the call executed lifts the approval's ceiling, and that survives a restart.
 await store.wakeWait(decision('wait-b',KEYS.b))
 const second=await store.resumeWait(claim('wait-b','decision-1','worker-b',b.revision))
 check('wait.approval.second-claim',second.lease.expiry===iso(T0+4*HOUR))
 const executed=await commitWith('wait-b',b.revision,'executed',{...APPROVAL,state:'executed'},fenceOf(second.lease))
 await store.releaseLease(fenceOf(second.lease))
 await restart('wait.restart.settled')
 at=T0+5*HOUR
 const settled=await store.acquireLease({resource:'wait-b',owner:'worker-c',expectedRevision:executed.revision,durationMs:HOUR})
 check('wait.approval.settled',settled.expiry===iso(T0+6*HOUR))
 await store.releaseLease(fenceOf(settled))

 // The run's deadline: it lowers a held lease and bounds a renewal, a park stamps it, a later and earlier one reaches the
 // wait already parked (#266 M1), the claim is refused at it, and its deadline wake ends the wait and the run.
 const c=await open('wait-c')
 check('wait.deadline.limit',(await store.limitRun({version:1,resource:'wait-c',run:iso(T0+5*HOUR+30*MINUTE)})).run===iso(T0+5*HOUR+30*MINUTE))
 check('wait.deadline.lowered',(await store.readLease('wait-c'))?.expiry===iso(T0+5*HOUR+30*MINUTE))
 check('wait.deadline.renew',(await store.renewLease(c.fence,HOUR)).expiry===iso(T0+5*HOUR+30*MINUTE))
 const third=await store.parkWait(parkOf('wait-c',c.revision,KEYS.c,4*HOUR,{condition:{event:{kind:'approval-decision',subject:'call-1'}}}),c.fence)
 check('wait.deadline.stamped',third.wait.deadline?.at===iso(T0+5*HOUR+30*MINUTE)&&third.wait.deadline.by==='run')
 await store.wakeWait(decision('wait-c',KEYS.c))
 check('wait.deadline.earliest',(await store.limitRun({version:1,resource:'wait-c',run:iso(T0+9*HOUR)})).run===iso(T0+5*HOUR+30*MINUTE))
 await store.limitRun({version:1,resource:'wait-c',run:iso(T0+5*HOUR+10*MINUTE)})
 check('wait.deadline.restamped',(await store.readWait('wait-c','wait-1'))?.deadline?.at===iso(T0+5*HOUR+10*MINUTE))
 at=T0+5*HOUR+10*MINUTE
 await refuses('wait.deadline.claim',()=>store.resumeWait(claim('wait-c','decision-1','worker-b',c.revision)),'deadline-passed')
 await refuses('wait.deadline.acquire',()=>store.acquireLease({resource:'wait-c',owner:'worker-b',expectedRevision:c.revision,durationMs:HOUR}),'deadline-passed')
 check('wait.deadline.expired',(await store.wakeWait(deadline('wait-c',KEYS.c))).at===iso(T0+5*HOUR+10*MINUTE)&&(await store.readWait('wait-c','wait-1'))?.state==='expired')
 await restart('wait.restart.deadline')
 await refuses('wait.restart.closed',()=>store.acquireLease({resource:'wait-c',owner:'worker-b',expectedRevision:c.revision,durationMs:HOUR}),'closed')
 check('wait.restart.earliest',(await store.limitRun({version:1,resource:'wait-c',run:iso(T0+9*HOUR)})).run===iso(T0+5*HOUR+10*MINUTE))
 await refuses('wait.restart.key',()=>store.wakeWait(decision('wait-b',KEYS.a,'decision-x')),'unauthenticated')

 // ADR-0029: a store that declares it keeps a delegated run's principals with the wait and the lease, across a restart:
 // the wait's pins hold them, its claim is a lease of the same principals, and a later takeover keeps them.
 if(store.leasePrincipals===true){
  const P=await store.commit({runId:'wait-p',expectedRevision:null,commitId:'wait-p-open',checkpoint:asked,events:[]})
  const held=await store.acquireLease({resource:'wait-p',owner:'worker-a',expectedRevision:P.revision,durationMs:HOUR,principal:PRINCIPAL})
  check('wait.principal.lease',held.version===2&&canonicalJSON(held.principal)===canonicalJSON(PRINCIPAL))
  const pins={...PINS,principal:PRINCIPAL}
  await store.parkWait(parkOf('wait-p',P.revision,KEYS.d,4*HOUR,{pins}),fenceOf(held))
  check('wait.principal.pinned',canonicalJSON((await store.readWait('wait-p','wait-1'))?.pins.principal)===canonicalJSON(PRINCIPAL))
  await restart('wait.restart.principal')
  check('wait.restart.principal',canonicalJSON((await store.readWait('wait-p','wait-1'))?.pins.principal)===canonicalJSON(PRINCIPAL))
  await store.wakeWait(decision('wait-p',KEYS.d))
  const claimP=(owner:string,principal?:PrincipalBinding):WaitClaim=>{const base=claim('wait-p','decision-1',owner,P.revision);return {...base,lease:{...base.lease,...(principal?{principal}:{})},revalidation:{...base.revalidation,pins:pinsDigest(pins)}}}
  await refuses('wait.principal.claim-actor',()=>store.resumeWait(claimP('worker-b',OTHER_ACTOR)),'principal-changed')
  await refuses('wait.principal.claim-subject',()=>store.resumeWait(claimP('worker-b',OTHER_SUBJECT)),'principal-changed')
  await refuses('wait.principal.claim-none',()=>store.resumeWait(claimP('worker-b')),'principal-changed')
  check('wait.principal.claim-unchanged',(await store.readWait('wait-p','wait-1'))?.state==='woken')
  const resumed=await store.resumeWait(claimP('worker-b',PRINCIPAL))
  check('wait.principal.claim',resumed.wait.state==='resumed'&&resumed.lease.version===2&&canonicalJSON(resumed.lease.principal)===canonicalJSON(PRINCIPAL)&&resumed.lease.epoch==='2')
  const fenced=fenceOf(resumed.lease)
  await refuses('wait.principal.fence',()=>store.checkFence({...fenced,principal:OTHER_ACTOR}),'principal-changed')
  const done=await commitWith('wait-p',P.revision,'wait-p-done',{...APPROVAL,state:'executed'},fenced)
  await store.releaseLease(fenced)
  await restart('wait.restart.principal-lease')
  await refuses('wait.restart.principal-takeover',()=>store.acquireLease({resource:'wait-p',owner:'worker-c',expectedRevision:done.revision,durationMs:HOUR,principal:OTHER_ACTOR}),'principal-changed')
  check('wait.restart.principal-kept',(await store.acquireLease({resource:'wait-p',owner:'worker-c',expectedRevision:done.revision,durationMs:HOUR,principal:PRINCIPAL})).epoch==='3')
 }
}

/** #265 M6: certify a host store kind's wait port. Properties: the key check (a key belongs to one wait; a wake presents
 * its own wait's key), the single winner, the lease release inside a committed park (a refused one changes nothing),
 * claim-once, the run closure (lease and unfenced commit, wait-pending then closed), the deadline and approval ceilings
 * (acquire, renew, the claim, a later run limit reaching a parked wait, the deadline wake), the commit-time approval
 * binding and its settlement (#266 N3), and every one of them across restarts of the store; with leasePrincipals
 * (ADR-0029), the principals a delegated run's wait pins, its claim and a later takeover keep. Throws
 * StorageConformanceFailure naming the first property broken. */
export async function runStoreWaitConformance(create:WaitStoreFactory):Promise<void>{
 try{await waitProperties(create)}catch(error){if(error instanceof StorageConformanceFailure)throw error;throw new StorageConformanceFailure('wait.operation')}
}
