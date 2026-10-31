import {canonicalJSON} from '../context/chat'
import {StorageConformanceFailure} from './conformance'
import {LeaseRefused,effectIdempotencyKey,fenceOf,type EffectOperation,type LeaseFence,type ReauthDisposition} from './lease'
import {ResumeRefused,approvalBinding,pinsDigest,type ResumePins} from './resume'
import {ReauthorizationRefused,type Reauthorized} from './reauth'
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
const KEYS={a:'kit.wait-a.Q7z2Xm9LpV4cR8tNb3Yh6Df1',b:'kit.wait-b.Hs8Pq3Lz6Wd1Rm9Tc4Vb7Nx2',c:'kit.wait-c.Ka5Ue2Jy8Gt3Wo6Bf9Mn1Sv4'} as const

const check=(property:string,value:unknown)=>{if(!value)throw new StorageConformanceFailure(property)}
const named=(error:unknown)=>error instanceof WaitRefused||error instanceof LeaseRefused||error instanceof ResumeRefused||error instanceof ReauthorizationRefused?error.reason:undefined
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
}

/* #226: the re-authorization properties. The run's head holds the never-admitted dispatch as the writer records it; the
 * disposition names it, under the lease that parks it. */
const NEVER_ADMITTED={version:1,code:'authentication',source:'tool',outcome:'not-started'}
const OPERATION=(run:string):EffectOperation=>({run,operation:'op-1',source:'sha256:'+'5'.repeat(64),tool:'create_issue',requestDigest:'sha256:'+'6'.repeat(64)})
const dispatched=(run:string,state='unknown')=>({id:'op-1',kind:'host',read:false,state,source:OPERATION(run).source,tool:'create_issue',requestDigest:OPERATION(run).requestDigest,...(state==='unknown'?{failure:NEVER_ADMITTED}:{})})
const PRINCIPAL='sha256:'+'7'.repeat(64)
async function reauthProperties(create:WaitStoreFactory):Promise<void>{
 let at=T0
 const now=()=>new Date(at)
 let store=await create(now)
 const restart=async(property:string)=>{const next=await create(now,store);check(property,next!==store);store=next}
 const head=(run:string,state='unknown',approval?:Record<string,unknown>)=>bytes(canonicalJSON({kit:'reauth-conformance',durable:{dispatches:[dispatched(run,state)]},...(approval?{approval}:{})}))
 const open=async(run:string,checkpoint:Uint8Array)=>{
  const opened=await store.commit({runId:run,expectedRevision:null,commitId:`${run}-open`,checkpoint,events:[]})
  return {revision:opened.revision,checkpoint,fence:fenceOf(await store.acquireLease({resource:run,owner:'worker-a',expectedRevision:opened.revision,durationMs:HOUR}))}
 }
 const disposition=(run:string,fence:LeaseFence):ReauthDisposition=>({version:1,resource:run,operation:OPERATION(run),idempotencyKey:effectIdempotencyKey(OPERATION(run)),fence,cause:'expired',connection:'kit-conn',principal:PRINCIPAL,permissions:['issues:read','issues:write'],requires:['issues:write']})
 const parkOf=(run:string,held:{revision:string;checkpoint:Uint8Array},proof:ReauthDisposition,key:string,pins:ResumePins=PINS):WaitPark=>({version:1,resource:run,wait:'wait-r',reason:'reauthorization',pending:{kind:'reauthorization',operation:'op-1',source:OPERATION(run).source,tool:'create_issue',connection:'kit-conn',cause:'expired'},condition:{event:{kind:'reauthorized',subject:'kit-conn'}},continuation:`${run}-continue`,checkpoint:{revision:held.revision,digest:checkpointBytesDigest(held.checkpoint)},pins,wakeKeyDigest:wakeKeyDigest(key),release:[],reauthorization:proof})
 const refreshed=(over:Partial<Reauthorized>={}):Reauthorized=>({actor:'person-owner',at:iso(at),principal:PRINCIPAL,permissions:['issues:write'],digest:'sha256:'+'8'.repeat(64),...over})
 const claim=(run:string,over:Partial<WaitClaim>={}):WaitClaim=>({resource:run,wait:'wait-r',evidence:'refreshed-1',lease:{resource:run,owner:'worker-b',expectedRevision:null,durationMs:24*HOUR},reconciled:[],revalidation:{version:1,resource:run,wait:'wait-r',pins:pinsDigest(PINS),evidence:'sha256:'+'e'.repeat(64),changes:[]},reauthorized:refreshed(),...over})
 const wake=(run:string,key:string):WakeEvidence=>({version:1,resource:run,wait:'wait-r',evidence:'refreshed-1',kind:'event',event:{kind:'reauthorized',subject:'kit-conn'},key})
 const keys={r:'kit.reauth-r.Q7z2Xm9LpV4cR8tNb3Yh6Df1',s:'kit.reauth-s.Hs8Pq3Lz6Wd1Rm9Tc4Vb7Nx2',t:'kit.reauth-t.Ka5Ue2Jy8Gt3Wo6Bf9Mn1Sv4',a:'kit.reauth-a.Zx4Cv7Bn1Mq8Wp3Lk6Jh9Gf2'}

 // Binding the evidence to the invocation: the disposition parks only under the lease it was minted under, over a head
 // that holds its operation as the never-admitted dispatch. A refused park changes nothing and leaves the lease held.
 const r=await open('reauth-r',head('reauth-r'))
 const proof=disposition('reauth-r',r.fence)
 await refuses('reauth.park.stale',()=>store.parkWait(parkOf('reauth-r',r,{...proof,fence:{...r.fence,epoch:'9'}},keys.r),r.fence),'stale')
 check('reauth.park.stale-unchanged',await store.readWait('reauth-r','wait-r')===undefined&&(await store.readLease('reauth-r'))?.state==='held')
 const s=await open('reauth-s',head('reauth-s','result'))
 await refuses('reauth.park.unbound',()=>store.parkWait(parkOf('reauth-s',s,disposition('reauth-s',s.fence),keys.s),s.fence),'unbound')
 check('reauth.park.unbound-unchanged',await store.readWait('reauth-s','wait-r')===undefined&&(await store.readLease('reauth-s'))?.state==='held')
 const parked=await store.parkWait(parkOf('reauth-r',r,proof,keys.r),r.fence)
 check('reauth.park.committed',parked.wait.reason==='reauthorization'&&canonicalJSON(parked.wait.reauthorization)===canonicalJSON(proof)&&parked.wait.approval===undefined&&parked.attention.needs==='reauthorization'&&parked.attention.subject==='kit-conn'&&(await store.readLease('reauth-r'))?.state==='released')
 await refuses('reauth.closure.lease',()=>store.acquireLease({resource:'reauth-r',owner:'worker-x',expectedRevision:r.revision,durationMs:HOUR}),'wait-pending')
 await restart('reauth.restart.parked')
 check('reauth.restart.parked',canonicalJSON(await store.readWait('reauth-r','wait-r'))===canonicalJSON(parked.wait))

 // The claim: a wake is not the refresh; another principal is a changed identity; narrower permissions after the refresh
 // that no longer cover the operation make it inadmissible; a refresh before the park or after the store's time is stale.
 at=T0+MINUTE
 await store.wakeWait(wake('reauth-r',keys.r))
 const of=(over:Partial<WaitClaim>)=>claim('reauth-r',{lease:{resource:'reauth-r',owner:'worker-b',expectedRevision:r.revision,durationMs:24*HOUR},...over})
 const {reauthorized:_,...bare}=of({})
 await refuses('reauth.claim.required',()=>store.resumeWait(bare),'decision-required')
 await refuses('reauth.claim.identity',()=>store.resumeWait(of({reauthorized:refreshed({principal:'sha256:'+'9'.repeat(64)})})),'identity-changed')
 await refuses('reauth.claim.narrower',()=>store.resumeWait(of({reauthorized:refreshed({permissions:['issues:read']})})),'permission-insufficient')
 await refuses('reauth.claim.before-park',()=>store.resumeWait(of({reauthorized:refreshed({at:iso(T0-1)})})),'stale')
 await refuses('reauth.claim.after-now',()=>store.resumeWait(of({reauthorized:refreshed({at:iso(at+1)})})),'stale')
 check('reauth.claim.refused-unchanged',(await store.readWait('reauth-r','wait-r'))?.state==='woken'&&(await store.readLease('reauth-r'))?.state==='released')
 const resumed=await store.resumeWait(of({}))
 check('reauth.claim.committed',resumed.wait.state==='resumed'&&canonicalJSON(resumed.wait.reauthorized)===canonicalJSON(refreshed())&&resumed.lease.state==='held'&&resumed.lease.epoch==='2')
 await refuses('reauth.claim.once',()=>store.resumeWait(of({})),'already-resumed')
 await restart('reauth.restart.claimed')
 check('reauth.restart.claimed',canonicalJSON((await store.readWait('reauth-r','wait-r'))?.reauthorized)===canonicalJSON(refreshed())&&(await store.readLease('reauth-r'))?.epoch==='2')

 // The approval: a reauthorization wait parked while the run's approved call is unsettled carries that approval's binding
 // and expiry, the store's own, and its claim is refused from the expiry.
 at=T0+2*MINUTE
 const asked=checkpointWith(APPROVAL),binding=approvalBinding(APPROVAL as never)
 const opened=await store.commit({runId:'reauth-a',expectedRevision:null,commitId:'reauth-a-open',checkpoint:asked,events:[]})
 const a=fenceOf(await store.acquireLease({resource:'reauth-a',owner:'worker-a',expectedRevision:opened.revision,durationMs:HOUR}))
 await store.parkWait({version:1,resource:'reauth-a',wait:'wait-1',reason:'approval',pending:{kind:'approval',callId:'call-1',name:APPROVAL.name,tool:APPROVAL.tool},condition:{event:{kind:'approval-decision',subject:'call-1'}},limits:{approvalTtlMs:HOUR},approval:binding,continuation:'reauth-a-approve',checkpoint:{revision:opened.revision,digest:checkpointBytesDigest(asked)},pins:PINS,wakeKeyDigest:wakeKeyDigest(keys.t),release:[]},a)
 await store.wakeWait({version:1,resource:'reauth-a',wait:'wait-1',evidence:'decision-1',kind:'event',event:{kind:'approval-decision',subject:'call-1'},key:keys.t})
 const approved=await store.resumeWait({resource:'reauth-a',wait:'wait-1',evidence:'decision-1',lease:{resource:'reauth-a',owner:'worker-b',expectedRevision:opened.revision,durationMs:24*HOUR},reconciled:[],revalidation:{version:1,resource:'reauth-a',wait:'wait-1',pins:pinsDigest(PINS),evidence:'sha256:'+'e'.repeat(64),changes:[]}})
 const expiry=iso(T0+2*MINUTE+HOUR),refusedHead=head('reauth-a','unknown',{...APPROVAL,state:'approved'})
 const committed=await store.commitFenced({runId:'reauth-a',expectedRevision:opened.revision,commitId:'approved-refused',checkpoint:refusedHead,events:[]},fenceOf(approved.lease))
 const bound=await store.parkWait(parkOf('reauth-a',{revision:committed.revision,checkpoint:refusedHead},disposition('reauth-a',fenceOf(approved.lease)),keys.a),fenceOf(approved.lease))
 check('reauth.approval.bound',canonicalJSON(bound.wait.approval)===canonicalJSON(binding)&&bound.wait.deadline?.approval===expiry&&bound.wait.deadline.at===expiry)
 at=T0+3*MINUTE
 await store.wakeWait(wake('reauth-a',keys.a))
 await restart('reauth.restart.bound')
 at=Date.parse(expiry)
 await refuses('reauth.approval.expiry',()=>store.resumeWait(claim('reauth-a',{lease:{resource:'reauth-a',owner:'worker-c',expectedRevision:committed.revision,durationMs:HOUR},reauthorized:refreshed({at:iso(T0+3*MINUTE)})})),'deadline-passed')
}

/** #265 M6: certify a host store kind's wait port. Properties: the key check (a key belongs to one wait; a wake presents
 * its own wait's key), the single winner, the lease release inside a committed park (a refused one changes nothing),
 * claim-once, the run closure (lease and unfenced commit, wait-pending then closed), the deadline and approval ceilings
 * (acquire, renew, the claim, a later run limit reaching a parked wait, the deadline wake), the commit-time approval
 * binding and its settlement (#266 N3), and every one of them across restarts of the store. #226 adds the
 * re-authorization properties (reauth.*): the disposition bound to the lease that parks it and to the head's
 * never-admitted dispatch (a stale or unbound one refused, nothing changed), the claim's refresh (required; the parked
 * principal; permissions after the refresh that cover the operation; taken after the park and not after the store's
 * time), the run's unsettled approval binding and expiry the store attaches, and each across restarts. Throws
 * StorageConformanceFailure naming the first property broken. */
export async function runStoreWaitConformance(create:WaitStoreFactory):Promise<void>{
 try{await waitProperties(create);await reauthProperties(create)}catch(error){if(error instanceof StorageConformanceFailure)throw error;throw new StorageConformanceFailure('wait.operation')}
}
