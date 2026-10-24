import {ChatNotFound,ChatRevisionConflict,StorageCommitMismatch,StorageConflict,StorageCursorInvalid} from './common'
import type {RunStore,RunCommit} from './run'
import type {ChatStore} from './chat'
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

async function conformance(property:string,probe:()=>Promise<void>):Promise<void>{
 try{await probe()}catch(error){if(error instanceof StorageConformanceFailure)throw error;throw new StorageConformanceFailure(property)}
}
export const runStoreConformance=(create:()=>RunStore|Promise<RunStore>)=>conformance('run.operation',()=>runProperties(create))
export const chatStoreConformance=(create:()=>ChatStore|Promise<ChatStore>)=>conformance('chat.operation',()=>chatProperties(create))
export const runStoreFaultConformance=(create:(phase:'before'|'after')=>RunStore|Promise<RunStore>)=>conformance('run.fault.operation',()=>runFaultProperties(create))
export const chatStoreFaultConformance=(create:(phase:'before'|'after')=>ChatStore|Promise<ChatStore>)=>conformance('chat.fault.operation',()=>chatFaultProperties(create))
