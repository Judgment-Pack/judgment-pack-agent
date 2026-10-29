import {ChatNotFound,ChatRevisionConflict,StorageCommitMismatch,StorageConflict,StorageCursorInvalid} from './common'
import type {RunStore,RunCommit} from './run'
import type {ChatStore} from './chat'
import type {HistoryRunStore} from './history'
import {fenceOf,LeaseRefused,type LeaseFence,type LeaseRefusalReason,type LeaseRunStore} from './lease'
export class StorageConformanceFailure extends Error {
 constructor(readonly property:string){super(`Storage conformance failed: ${property}`);this.name='StorageConformanceFailure'}
}
const bytes=(text:string)=>new TextEncoder().encode(text)
const text=(raw:Uint8Array|null|undefined)=>raw===null||raw===undefined?null:new TextDecoder().decode(raw)
function check(property:string,value:unknown):asserts value {if(!value)throw new StorageConformanceFailure(property)}
async function refuses(property:string,run:()=>Promise<unknown>,type:new(...args:never[])=>Error){try{await run()}catch(error){check(property,error instanceof type);return}throw new StorageConformanceFailure(property)}
/** Hosts supply a fresh, authorized, isolated store per invocation. Fixtures are hand authored.
 * Fault/lease/attachment/backup tests belong to the host capabilities; this certifies this port.
 */
async function runProperties(create:()=>RunStore|Promise<RunStore>):Promise<void>{
 const store=await create();check('run.durability.declared',store.durability!=='single-writer-callback')
 const raw=bytes('{"id":"run-alpha","payload":{"nested":[null,0,"kept"]}}'),event=bytes('{"type":"opened"}')
 const input:RunCommit={runId:'run-alpha',expectedRevision:null,commitId:'open-alpha',checkpoint:raw,events:[event]}
 const committed=await store.commit(input)
 check('run.receipt.scoped',committed.commitId==='open-alpha'&&committed.revision.length>0&&committed.cursor!==null)
 raw[0]=32;event[0]=32
 const head=await store.load('run-alpha');check('run.bytes.preserved',text(head?.checkpoint)==='{"id":"run-alpha","payload":{"nested":[null,0,"kept"]}}')
 check('run.head.atomic',head?.revision===committed.revision&&head.cursor===committed.cursor)
 head!.checkpoint![0]=32
 check('run.bytes.detached',text((await store.load('run-alpha'))?.checkpoint)?.startsWith('{'))
 const again=await store.commit({...input,checkpoint:bytes('{"id":"run-alpha","payload":{"nested":[null,0,"kept"]}}'),events:[bytes('{"type":"opened"}')]})
 check('run.commit.idempotent',JSON.stringify(again)===JSON.stringify(committed))
 await refuses('run.commit.content-mismatch',()=>store.commit({...input,checkpoint:bytes('{}')}),StorageCommitMismatch)
 await refuses('run.create.no-overwrite',()=>store.commit({...input,commitId:'different-open'}),StorageConflict)
 const contenders=await Promise.allSettled(['left','right'].map(commitId=>store.commit({runId:'run-alpha',expectedRevision:committed.revision,commitId,checkpoint:bytes(`{"winner":"${commitId}"}`),events:[bytes(`{"winner":"${commitId}"}`)]})))
 check('run.cas.atomic',contenders.filter(r=>r.status==='fulfilled').length===1&&contenders.filter(r=>r.status==='rejected'&&r.reason instanceof StorageConflict).length===1)
 const winner=contenders.find(r=>r.status==='fulfilled') as PromiseFulfilledResult<typeof committed>
 const winnerHead=await store.load('run-alpha'),all=await store.readEvents('run-alpha',null,100)
 const won=contenders[0]!.status==='fulfilled'?'left':'right'
 check('run.cas.checkpoint-and-events',text(winnerHead?.checkpoint)===`{"winner":"${won}"}`&&all.items.length===2&&text(all.items[1]?.bytes)===`{"winner":"${won}"}`)
 const appended=await store.commit({runId:'run-alpha',expectedRevision:winner.value.revision,commitId:'event-only',checkpoint:null,events:[bytes('{"type":"third"}')]})
 check('run.event-only.revision',appended.revision!==winner.value.revision&&text((await store.load('run-alpha'))?.checkpoint)===text(winnerHead?.checkpoint))
 const first=await store.readEvents('run-alpha',null,1),second=await store.readEvents('run-alpha',first.nextCursor,1),third=await store.readEvents('run-alpha',second.nextCursor,1),end=await store.readEvents('run-alpha',third.nextCursor,1)
 check('run.events.order',first.items[0]?.cursor===committed.cursor&&second.items[0]?.cursor===winner.value.cursor&&third.items[0]?.cursor===appended.cursor&&first.hasMore&&second.hasMore&&!third.hasMore&&end.items.length===0&&end.nextCursor===third.nextCursor)
 first.items[0]!.bytes[0]=32
 check('run.events.detached',text((await store.readEvents('run-alpha',null,1)).items[0]?.bytes)==='{"type":"opened"}')
 await refuses('run.cursor.resource-scope',()=>store.readEvents('run-beta',committed.cursor,1),StorageCursorInvalid)
 const other=await create();await refuses('run.cursor.store-scope',()=>other.readEvents('run-alpha',committed.cursor,1),StorageCursorInvalid)
 await refuses('run.cursor.invalid',()=>store.readEvents('run-alpha','unknown-cursor',1),StorageCursorInvalid)
 check('run.read.missing-observational',await store.load('run-beta')===undefined&&(await store.readEvents('run-beta',null,1)).items.length===0)
}
/** Factory seeds alpha and beta with host-only fields, a plan JSON payload and distinct kinds. */
async function chatProperties(create:()=>ChatStore|Promise<ChatStore>):Promise<void>{
 const store=await create();check('chat.consistency.declared',store.consistency!=='single-writer-legacy');const alpha=await store.get('chat-alpha');check('chat.get.opaque-id',alpha?.chat.id==='chat-alpha')
 check('chat.get.missing',await store.get('missing')===undefined)
 const before=JSON.stringify(alpha!.chat),contenders=await Promise.allSettled(['plan','work'].map(interaction=>store.update('chat-alpha',{interaction:interaction as 'plan'|'work'},alpha!.revision)))
 check('chat.cas.no-lost-update',contenders.filter(r=>r.status==='fulfilled').length===1&&contenders.filter(r=>r.status==='rejected'&&r.reason instanceof ChatRevisionConflict).length===1)
 const updated=await store.get('chat-alpha');check('chat.cas.new-revision',updated?.revision!==alpha!.revision)
 const original=JSON.parse(before);delete original.interaction
 const retained={...updated!.chat};delete retained.interaction
 check('chat.unrelated-fields.preserved',JSON.stringify(retained)===JSON.stringify(original))
 updated!.chat.hostOnly='mutated';check('chat.get.detached',(await store.get('chat-alpha'))?.chat.hostOnly!=='mutated')
 await refuses('chat.update.missing',()=>store.update('missing',{interaction:'work'},alpha!.revision),ChatNotFound)
 const page=await store.list({limit:1});check('chat.list.bounded',page.items.length===1&&page.hasMore&&page.nextCursor!==undefined)
 const next=await store.list({limit:1,cursor:page.nextCursor});check('chat.list.ordered',next.items.length===1&&!next.hasMore&&page.items[0]!.chat.id<next.items[0]!.chat.id)
 await refuses('chat.cursor.kind-scope',()=>store.list({limit:1,cursor:page.nextCursor,kind:'draft'}),StorageCursorInvalid)
 const other=await create();await refuses('chat.cursor.store-scope',()=>other.list({limit:1,cursor:page.nextCursor}),StorageCursorInvalid)
}

/** The fixture injects exactly one failure at its physical commit seam, then permits normal calls.
 * A postcommit fault represents lost acknowledgement, not permission to repeat an external effect.
 */
async function runFaultProperties(create:(phase:'before'|'after')=>RunStore|Promise<RunStore>):Promise<void>{
 for(const phase of ['before','after'] as const){
  const store=await create(phase),input:RunCommit={runId:'fault-run',expectedRevision:null,commitId:'fault-commit',checkpoint:bytes('{"payload":"kept"}'),events:[bytes('{"type":"kept"}')]}
  let rejected=false;try{await store.commit(input)}catch{rejected=true}
  check(`run.fault.${phase}.injected`,rejected)
  const head=await store.load('fault-run'),page=await store.readEvents('fault-run',null,100)
  if(phase==='before')check('run.fault.before.atomic',head===undefined&&page.items.length===0)
  else check('run.fault.after.complete',text(head?.checkpoint)==='{"payload":"kept"}'&&page.items.length===1&&head?.cursor===page.items[0]?.cursor)
  const receipt=await store.commit(input),after=await store.load('fault-run'),events=await store.readEvents('fault-run',null,100)
  check(`run.fault.${phase}.recovered`,after?.revision===receipt.revision&&text(after.checkpoint)==='{"payload":"kept"}'&&events.items.length===1)
  if(phase==='after')check('run.fault.after.idempotent',receipt.revision===head?.revision)
 }
}
/** Factory seeds chat-alpha and injects exactly one failure in update, before or after publication. */
async function chatFaultProperties(create:(phase:'before'|'after')=>ChatStore|Promise<ChatStore>):Promise<void>{
 for(const phase of ['before','after'] as const){
  const store=await create(phase),head=await store.get('chat-alpha');check(`chat.fault.${phase}.fixture`,head!==undefined)
  let rejected=false;try{await store.update('chat-alpha',{interaction:'work'},head.revision)}catch{rejected=true}
  check(`chat.fault.${phase}.injected`,rejected)
  const after=await store.get('chat-alpha');check(`chat.fault.${phase}.present`,after!==undefined)
  if(phase==='before')check('chat.fault.before.atomic',JSON.stringify(after)===JSON.stringify(head))
  else{
   check('chat.fault.after.complete',after.revision!==head.revision&&after.chat.interaction==='work')
   await refuses('chat.fault.after.stale-refused',()=>store.update('chat-alpha',{interaction:'plan'},head.revision),ChatRevisionConflict)
  }
 }
}

/** #223, for a host store kind's lease port (desk #609). The factory builds a fresh store whose authoritative lease
 * clock is the `now` it is given, so the probe can move time; every other property is the store's own. */
async function leaseProperties(create:(now:()=>Date)=>LeaseRunStore|Promise<LeaseRunStore>):Promise<void>{
 let at=Date.parse('2026-01-01T00:00:00.000Z')
 const store=await create(()=>new Date(at)),run='lease-run'
 check('lease.declared',store.leaseVersion===1&&(store.leaseLifetime==='durable'||store.leaseLifetime==='process')&&store.durability!=='single-writer-callback')
 const refusesLease=async(property:string,attempt:()=>Promise<unknown>,reason:LeaseRefusalReason)=>{try{await attempt()}catch(error){check(property,error instanceof LeaseRefused&&error.reason===reason);return}throw new StorageConformanceFailure(property)}
 const commit=(commitId:string,expectedRevision:string|null,fence:LeaseFence)=>store.commitFenced({runId:run,expectedRevision,commitId,checkpoint:bytes(`{"commit":"${commitId}"}`),events:[]},fence)
 const opened=await store.commit({runId:run,expectedRevision:null,commitId:'lease-open',checkpoint:bytes('{"commit":"lease-open"}'),events:[]})
 await refusesLease('lease.acquire.revision-checked',()=>store.acquireLease({resource:run,owner:'worker-a',expectedRevision:null,durationMs:1000}),'revision-changed')
 const first=await store.acquireLease({resource:run,owner:'worker-a',expectedRevision:opened.revision,durationMs:1000})
 check('lease.acquire.first',first.version===1&&first.resource===run&&first.owner==='worker-a'&&first.epoch==='1'&&first.state==='held'&&first.expiry==='2026-01-01T00:00:01.000Z')
 await refusesLease('lease.acquire.held',()=>store.acquireLease({resource:run,owner:'worker-b',expectedRevision:opened.revision,durationMs:1000}),'held')
 const a=fenceOf(first),current=await commit('lease-a-1',opened.revision,a)
 check('lease.commit.current',current.commitId==='lease-a-1'&&(await store.load(run))?.revision===current.revision)
 await refusesLease('lease.commit.other-owner',()=>commit('lease-forged',current.revision,{...a,owner:'worker-b'}),'other-owner')
 await refusesLease('lease.commit.future-epoch',()=>commit('lease-forged',current.revision,{...a,epoch:'2'}),'stale-epoch')
 await refusesLease('lease.commit.resource',()=>store.commitFenced({runId:'lease-other',expectedRevision:null,commitId:'lease-other',checkpoint:bytes('{}'),events:[]},a),'resource-mismatch')
 await refusesLease('lease.fence.exact-epoch',()=>store.checkFence({...a,epoch:'01'}),'invalid')
 at+=500
 const renewed=await store.renewLease(a,1000)
 check('lease.renew.store-clock',renewed.epoch==='1'&&renewed.owner==='worker-a'&&renewed.expiry==='2026-01-01T00:00:01.500Z')
 await refusesLease('lease.renew.other-owner',()=>store.renewLease({...a,owner:'worker-b'},1000),'other-owner')
 at=Date.parse(renewed.expiry)
 await refusesLease('lease.expiry.equal-time',()=>store.checkFence(a),'expired')
 await refusesLease('lease.renew.no-revival',()=>store.renewLease(a,1000),'expired')
 await refusesLease('lease.commit.expired',()=>commit('lease-a-late',current.revision,a),'expired')
 check('lease.commit.expired-unapplied',(await store.load(run))?.revision===current.revision)
 const second=await store.acquireLease({resource:run,owner:'worker-b',expectedRevision:current.revision,durationMs:1000})
 check('lease.takeover.greater-epoch',second.epoch==='2'&&second.owner==='worker-b')
 await refusesLease('lease.commit.stale-epoch',()=>commit('lease-a-stale',current.revision,a),'stale-epoch')
 const b=fenceOf(second)
 let taken=await commit('lease-b-1',current.revision,b)
 // #223 review: every way a fence is not current, through every port the writer and the effect host rely on.
 const stale:[string,LeaseFence,LeaseRefusalReason][]=[['stale-epoch',a,'stale-epoch'],['other-owner',{...b,owner:'worker-x'},'other-owner'],['resource',{...b,resource:'never-leased'},'no-lease']]
 for(const [name,fence,reason] of stale){
  await refusesLease(`lease.check.${name}`,()=>store.checkFence(fence),reason)
  await refusesLease(`lease.renew.${name}`,()=>store.renewLease(fence,1000),reason)
  await refusesLease(`lease.release.${name}`,()=>store.releaseLease(fence),reason)
 }
 const history=store as Partial<HistoryRunStore>&LeaseRunStore
 const historyCommit=(commitId:string,expectedRevision:string|null,fence:LeaseFence,runId=run)=>history.commitHistoryFenced!({run:{runId,expectedRevision,commitId,checkpoint:bytes(`{"commit":"${commitId}"}`),events:[]},observations:[]},fence)
 const snapshot=(fence:LeaseFence)=>history.commitContextSnapshotFenced!({chatId:'lease-chat',expectedRevision:null,manifest:{digest:'sha256:'+'a'.repeat(64),bytes:2}},'lease-snapshot',fence)
 if(history.historyVersion===1){
  check('lease.history.declared',typeof history.commitHistoryFenced==='function'&&typeof history.commitContextSnapshotFenced==='function')
  for(const [name,fence,reason] of [...stale.slice(0,2),['resource',{...b,resource:'lease-other'},'resource-mismatch']] as [string,LeaseFence,LeaseRefusalReason][]){
   await refusesLease(`lease.history.${name}`,()=>historyCommit(`lease-history-${name}`,taken.revision,fence),reason)
   if(name!=='resource')await refusesLease(`lease.snapshot.${name}`,()=>snapshot(fence),reason)
  }
  await refusesLease('lease.history.resource',()=>historyCommit('lease-history-other',null,b,'lease-other'),'resource-mismatch')
  check('lease.history.unapplied',(await store.load(run))?.revision===taken.revision)
  taken=await historyCommit('lease-history-b',taken.revision,b)
  check('lease.history.current',(await store.load(run))?.revision===taken.revision)
 }
 const released=await store.releaseLease(b)
 check('lease.release.retains-epoch',released.state==='released'&&released.epoch==='2')
 await refusesLease('lease.release.fence-ends',()=>commit('lease-b-2',taken.revision,b),'released')
 await refusesLease('lease.check.released',()=>store.checkFence(b),'released')
 if(history.historyVersion===1){
  await refusesLease('lease.history.released',()=>historyCommit('lease-history-released',taken.revision,b),'released')
  await refusesLease('lease.snapshot.released',()=>snapshot(b),'released')
 }
 const third=await store.acquireLease({resource:run,owner:'worker-a',expectedRevision:taken.revision,durationMs:1000})
 check('lease.release.next-greater',third.epoch==='3'&&(await store.readLease(run))?.epoch==='3')
 // The same owner's earlier epoch is stale too: no port may match a fence by owner alone.
 for(const [name,attempt] of [['check',()=>store.checkFence(a)],['renew',()=>store.renewLease(a,1000)],['release',()=>store.releaseLease(a)],['commit',()=>commit('lease-a-old',taken.revision,a)]] as [string,()=>Promise<unknown>][])await refusesLease(`lease.same-owner.${name}`,attempt,'stale-epoch')
 if(history.historyVersion===1)await refusesLease('lease.same-owner.history',()=>historyCommit('lease-a-old-history',taken.revision,a),'stale-epoch')
 check('lease.read.missing',await store.readLease('never-leased')===undefined)
 const contenders=await Promise.allSettled(['worker-x','worker-y'].map(owner=>store.acquireLease({resource:'lease-race',owner,expectedRevision:null,durationMs:1000})))
 check('lease.acquire.atomic',contenders.filter(r=>r.status==='fulfilled').length===1&&contenders.some(r=>r.status==='rejected'&&r.reason instanceof LeaseRefused&&r.reason.reason==='held'))
}

async function conformance(property:string,probe:()=>Promise<void>):Promise<void>{
 try{await probe()}catch(error){if(error instanceof StorageConformanceFailure)throw error;throw new StorageConformanceFailure(property)}
}
export const runStoreConformance=(create:()=>RunStore|Promise<RunStore>)=>conformance('run.operation',()=>runProperties(create))
export const chatStoreConformance=(create:()=>ChatStore|Promise<ChatStore>)=>conformance('chat.operation',()=>chatProperties(create))
export const runStoreFaultConformance=(create:(phase:'before'|'after')=>RunStore|Promise<RunStore>)=>conformance('run.fault.operation',()=>runFaultProperties(create))
export const chatStoreFaultConformance=(create:(phase:'before'|'after')=>ChatStore|Promise<ChatStore>)=>conformance('chat.fault.operation',()=>chatFaultProperties(create))
export const runStoreLeaseConformance=(create:(now:()=>Date)=>LeaseRunStore|Promise<LeaseRunStore>)=>conformance('lease.operation',()=>leaseProperties(create))
