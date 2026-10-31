import {it,expect,vi} from 'vitest'
import vectors from '../../../contract/vectors/chat/context-snapshot.json'
import {ChatContext,canonicalJSON} from './chat'
import {CHAT_SNAPSHOT_VERSION,readChatContextSnapshot,ContextSnapshotInvalid,ContextTurnBindingInvalid,UnsupportedContextSnapshotVersion} from './chatSnapshot'
import {StoredChatScope,restoreStoredChatScope,readStoredChatSnapshot} from './storedChat'
import {MemoryRunStore} from '../storage/run'
import {HistoryChanged,HistoryInputInvalid,historyObject,HISTORY_OBJECT_BYTES,chunkHistoryValue} from '../history/codec'
import {StorageConflict} from '../storage/common'
import {contextualSession} from './session'
import {managedContext,contextOf} from './managed'
import {durableOwner,durableSession,scopeFor} from '../storage/writer'
import {extendExecution,readExecution} from '../execution/state'
import type {AssistantSession} from '../engine'
const bytes=(v:unknown)=>new TextEncoder().encode(canonicalJSON(v))
it('reads both handwritten complete snapshots and preserves aliases/order/empty revision',()=>{
 // The items are version 1; the same state at the version written now is what round-trips (#271).
 for(const vector of vectors.items){const raw=bytes({...vector,version:CHAT_SNAPSHOT_VERSION}),chat=ChatContext.restore(raw);expect(chat.snapshot()).toEqual(raw);expect(chat.entries.get('shared:entry')!.value).toEqual({z:false,a:null,number:0.1});expect(JSON.stringify(chat.entries.get('shared:entry')!.value)).toBe(vector.entries[0]!.valueJSON);expect(chat.entries.get('shared:entry')!.revision).toBe('')
 expect(chat.task('one')).toBe(chat.task('two'));chat.task('one')!.visible.add('shared:later');expect(chat.task('two')!.visible.has('shared:later')).toBe(true)
 const scope=chat.resumeTurn(2),turn=scope.openTurn();expect(turn.use('shared:later')).toBeUndefined();expect(()=>scope.openTurn()).toThrow(ContextTurnBindingInvalid);expect(chat.turnCount).toBe(3)
 // Both offered rows alias the same object even when the pending list no longer contains it.
 const a=chat.resumeTurn(1).openTurn();const pending=chat.pendingEvicted();if(vector.evicted.length){pending[0]!.bytes=99;expect(JSON.parse(new TextDecoder().decode(chat.snapshot())).evictions[0].bytes).toBe(99);a.sent();expect(chat.pendingEvicted()).toHaveLength(0)}else{const saved=readChatContextSnapshot(chat.snapshot());expect(saved.evictions).toHaveLength(1);expect(saved.open[0]!.offered).toEqual([0]);expect(saved.open[1]!.offered).toEqual([0])}
 }
})
it('refuses unknown/conflicting references, lossy source, future version and excess bounds before restore',()=>{
 const base=vectors.items[0]!
 for(const mutate of [(v:any)=>v.unknown=1,(v:any)=>v.tasks[0].thread=4,(v:any)=>v.open.push({...v.open[0]}),(v:any)=>v.size++,(v:any)=>v.entries[0].valueJSON='{"duplicate":1,"duplicate":2}',(v:any)=>v.visibility.push([]),(v:any)=>v.open[0].visible=null,(v:any)=>v.disposed=true]){const v=structuredClone(base);mutate(v);expect(()=>ChatContext.restore(bytes(v))).toThrow(ContextSnapshotInvalid)}
 expect(()=>ChatContext.restore(bytes({...base,version:CHAT_SNAPSHOT_VERSION+1}))).toThrow(UnsupportedContextSnapshotVersion)
 expect(()=>ChatContext.restore(new TextEncoder().encode('{"version":1,"version":1}'))).toThrow(ContextSnapshotInvalid)
 expect(()=>ChatContext.restore(bytes({...base,turns:9007199254740992}))).toThrow(ContextSnapshotInvalid)
})
it('journals every commit with storage sequence/time and immutable pinned views',async()=>{
 const now=vi.fn(()=>new Date('2026-10-25T01:02:03.000Z')),store=new MemoryRunStore({now}),first=await store.commitHistory({run:{runId:'run',expectedRevision:null,commitId:'one',checkpoint:bytes({test:1}),events:[bytes({event:1})]},observations:[],genesis:true})
 expect(await store.commitHistory({run:{runId:'run',expectedRevision:null,commitId:'one',checkpoint:bytes({test:1}),events:[bytes({event:1})]},observations:[],genesis:true})).toEqual(first);expect(now).toHaveBeenCalledOnce()
 await store.commit({runId:'run',expectedRevision:first.revision,commitId:'two',checkpoint:bytes({test:2}),events:[]})
 const old=await store.readHistoryPage('run',first.history.revision,0,100);expect(old.items).toHaveLength(1);expect(old.total).toBe(1)
 const head=(await store.readHistoryHead('run'))!,current=await store.readHistoryPage('run',head.revision,0,100);expect(current.items.map(e=>[e.sequence,e.eventStart,e.eventCount])).toEqual([[1,1,1],[2,2,0]]);expect(current.items[1]!.previousDigest).toBe(current.items[0]!.digest);expect(current.items[0]!.recordedAt).toBe('2026-10-25T01:02:03.000Z')
 current.items[0]!.events[0]!.digest='changed';expect((await store.readHistoryPage('run',head.revision,0,100)).items[0]!.events[0]!.digest).not.toBe('changed')
 await expect(store.readHistoryPage('other',head.revision,0,100)).rejects.toThrow(HistoryChanged)
 const staged=await historyObject(bytes({staged:true}));await store.stageHistoryObjects({kind:'run',id:'run'},[staged]);await expect(store.readHistoryObject({kind:'run',id:'run'},head.revision,staged.digest,0,100)).rejects.toThrow(HistoryInputInvalid)
})
it('validates every chunk before atomic chat/run publication and refuses stale chat CAS',async()=>{
 const store=new MemoryRunStore(),chat=ChatContext.restore(bytes(vectors.items[0])),source=new StoredChatScope(store,'chat',chat);source.openTurn()
 const context=await source.prepare(),input={run:{runId:'run',expectedRevision:null,commitId:'one',checkpoint:bytes({test:1}),events:[]},observations:[],context}
 const first=await store.commitHistory(input),before=(await store.load('run'))!;const broken={...input,run:{...input.run,expectedRevision:first.revision,commitId:'two'}}
 await expect(store.commitHistory(broken)).rejects.toThrow(StorageConflict);expect(await store.load('run')).toEqual(before)
 const restored=await restoreStoredChatScope(store,first.history.context!);expect(restored.context.snapshot()).toEqual(chat.snapshot());expect(restored.openTurn().number).toBe(context.turn);expect(()=>restored.openTurn()).toThrow(ContextTurnBindingInvalid)
 await expect(store.commitHistory({...input,run:{...input.run,runId:'another',commitId:'another'},context:{...context,expectedRevision:first.history.context!.revision}})).rejects.toThrow(HistoryInputInvalid)
 const chunks=await chunkHistoryValue(new Uint8Array(HISTORY_OBJECT_BYTES+1).fill(1));await store.stageHistoryObjects({kind:'chat',id:'incomplete'},[chunks.manifest,chunks.objects[0]!]);await expect(store.commitContextSnapshot({chatId:'incomplete',expectedRevision:null,manifest:{digest:chunks.manifest.digest,bytes:chunks.manifest.bytes}},'missing')).rejects.toThrow(HistoryInputInvalid);expect(await store.loadContextSnapshot('incomplete')).toBeUndefined()
})
it('restores the exact active session turn from its real writer CAS, rejecting a competing attachment before dispatch',async()=>{
 const store=new MemoryRunStore(),source=new StoredChatScope(store,'chat',new ChatContext({scope:'shared'})),execute=vi.fn(async()=>({content:[]}))
 const session=(scope:StoredChatScope,resume=false):AssistantSession=>({prompt:'Work',testPrompt:'',tools:[],hostTools:[{name:'save_object',description:'Save',inputSchema:{},effect:'workflow',taxonomy:{operation:'own-state',effect:'pure'},execute}],callTool:vi.fn(async()=>({content:[]})),model:{family:'openai-compatible',model:'fixture',call:vi.fn(async()=>new Response('done'))},signal:new AbortController().signal,context:{chat:scope,currentRequest:{message:'Work',input:'Work'},runStore:{store,id:'active',resume,context:scope}}})
 const opened=await contextualSession(session(source)),head=(await store.readHistoryHead('active'))!;expect(head.context?.turn).toBe(1)
 // Snapshot is an actual writer commit; don't close the original turn before simulating restart.
 const a=await restoreStoredChatScope(store,head.context!),b=await restoreStoredChatScope(store,head.context!),resumed=await contextualSession(session(a,true));expect(a.context.turnCount).toBe(1)
 let refused=false;try{await contextualSession(session(b,true))}catch{refused=true};expect(refused).toBe(true);expect(execute).not.toHaveBeenCalled();resumed.context.close();opened.context.close()
})
it('recognizes Go unrestricted thread wire state but refuses unsupported TS live state',()=>{
 for(const vector of vectors.goOnly){const raw=bytes(vector);expect(readChatContextSnapshot(raw)).toEqual(vector);expect(()=>ChatContext.restore(raw)).toThrow(ContextSnapshotInvalid)}
})
it('restores latest overlapping chat state while preserving original run/turn identity',async()=>{
 const store=new MemoryRunStore(),chat=new ChatContext({scope:'shared'}),a=new StoredChatScope(store,'c',chat);a.openTurn()
 const first=await store.commitHistory({run:{runId:'a',expectedRevision:null,commitId:'a',checkpoint:bytes({test:1}),events:[]},observations:[],context:await a.prepare()})
 const b=new StoredChatScope(store,'c',chat,first.history.context);b.openTurn();const second=await store.commitHistory({run:{runId:'b',expectedRevision:null,commitId:'b',checkpoint:bytes({test:2}),events:[]},observations:[],context:await b.prepare()})
 const restored=await restoreStoredChatScope(store,first.history.context!);expect(restored.context.turnCount).toBe(2);expect(restored.matches(first.history.context!)).toBe(true);expect(restored.openTurn().number).toBe(1)
 const update=await restored.prepare();expect(update.expectedRevision).toBe(second.history.context!.revision)
 await store.commitHistory({run:{runId:'a',expectedRevision:first.revision,commitId:'resume-a',checkpoint:bytes({test:1}),events:[]},observations:[],context:update})
 const head=(await store.loadContextSnapshot('c'))!,fresh=await restoreStoredChatScope(store,{...first.history.context!,revision:head.revision,manifest:head.manifest});expect(readChatContextSnapshot(fresh.context.snapshot()).open.map(row=>row.number)).toEqual([1,2])
})
it('preserves faulted acknowledgement idempotence and refuses precommit publication',async()=>{
 let fault='before';const store=new MemoryRunStore({beforeCommit:()=>{if(fault==='before')throw Error('injected before')},afterCommit:()=>{if(fault==='after')throw Error('injected after')}}),source=new StoredChatScope(store,'c',new ChatContext({scope:'shared'}));source.openTurn();const context=await source.prepare(),input={run:{runId:'r',expectedRevision:null,commitId:'one',checkpoint:bytes({x:1}),events:[]},observations:[],context}
 await expect(store.commitHistory(input)).rejects.toThrow('injected before');expect(await store.load('r')).toBeUndefined();expect(await store.readHistoryHead('r')).toBeUndefined();expect(await store.loadContextSnapshot('c')).toBeUndefined()
 fault='after';await expect(store.commitHistory(input)).rejects.toThrow('injected after');const head=(await store.load('r'))!,chat=(await store.loadContextSnapshot('c'))!;fault='none'
 const ack=await store.commitHistory({context:input.context,observations:input.observations,run:input.run});expect(ack.revision).toBe(head.revision);expect(ack.history.context!.revision).toBe(chat.revision);expect(ack.history.commitCount).toBe(1)
})
it('exports complete snapshot/storage APIs through the public contract entry',async()=>{
 const api=await import('../entries/contract');expect(api.ChatContext).toBe(ChatContext);expect(api.StoredChatScope).toBe(StoredChatScope);expect(api.MemoryRunStore).toBe(MemoryRunStore);expect(api.readChatContextSnapshot).toBe(readChatContextSnapshot)
})

const storedSession=(store:MemoryRunStore,scope:StoredChatScope,id:string,resume=false):AssistantSession=>({prompt:'Work',testPrompt:'',tools:[],hostTools:[],callTool:vi.fn(async()=>({content:[]})),model:{family:'openai-compatible',model:'fixture',call:vi.fn(async()=>new Response('done'))},signal:new AbortController().signal,context:{chat:scope,currentRequest:{message:'Work',input:'Work'},runStore:{store,id,resume,context:scope}}})
it('acknowledges actual terminal close before delivery and preserves every other active turn',async()=>{
 const store=new MemoryRunStore(),chat=ChatContext.restore(bytes(vectors.items[0])),scope=new StoredChatScope(store,'c',chat),events:string[]=[]
 for await(const event of managedContext(storedSession(store,scope,'terminal'),async function*(){yield {type:'message',text:'Done'};yield {type:'end'}})){
  if(event.type==='execution_checkpoint'&&['complete','stopped'].includes(event.checkpoint.status)||event.type==='end'){
   const head=(await store.readHistoryHead('terminal'))!;expect(head.context!.closed).toBe(true);const saved=readChatContextSnapshot(await readStoredChatSnapshot(store,head.context!));expect(saved.open.map(t=>t.number)).toEqual([1,2,3]);expect(saved.entries[0]!.valueJSON).toBe(vectors.items[0]!.entries[0]!.valueJSON)
  }events.push(event.type)
 }
 expect(events).toContain('end');expect(events).not.toContain('error');expect(readChatContextSnapshot(chat.snapshot()).open.map(t=>t.number)).toEqual([1,2,3]);await expect(restoreStoredChatScope(store,(await store.readHistoryHead('terminal'))!.context!)).rejects.toThrow(ContextTurnBindingInvalid)
})
it('preserves paused logical turn through actual runner cleanup and completes on exact restored turn',async()=>{
 const store=new MemoryRunStore(),scope=new StoredChatScope(store,'c',new ChatContext({scope:'shared'}))
 for await(const _ of managedContext(storedSession(store,scope,'paused'),async function*(prepared){yield {type:'budget_paused',checkpoint:contextOf(prepared).execution.snapshot('paused')};yield {type:'end'}})){}
 expect(readChatContextSnapshot(scope.context.snapshot()).open.map(t=>t.number)).toEqual([1])
 const paused=(await store.readHistoryHead('paused'))!;expect(paused.context!.closed).toBeUndefined();expect(readChatContextSnapshot(await readStoredChatSnapshot(store,paused.context!)).open.map(t=>t.number)).toEqual([1])
 const current=(await store.load('paused'))!,grant=extendExecution(readExecution(JSON.parse(new TextDecoder().decode(current.checkpoint!)))!);await store.commit({runId:'paused',expectedRevision:current.revision,commitId:'explicit-host-allowance',checkpoint:bytes(grant),events:[]})
 const resumed=await restoreStoredChatScope(store,paused.context!);const events:string[]=[];for await(const event of managedContext(storedSession(store,resumed,'paused',true),async function*(){yield {type:'message',text:'Done'};yield {type:'end'}}))events.push(event.type)
 expect(events).not.toContain('error');expect(resumed.context.turnCount).toBe(1);const terminal=(await store.readHistoryHead('paused'))!;expect(terminal.context!.closed).toBe(true);expect(readChatContextSnapshot(await readStoredChatSnapshot(store,terminal.context!)).open).toEqual([])
})
it('never delivers a successful terminal checkpoint/end after failed or unknown closed-chat CAS',async()=>{
 for(const lost of [false,true]){
  const store=new MemoryRunStore(),scope=new StoredChatScope(store,'c',new ChatContext({scope:'shared'})),original=store.commitHistory.bind(store);store.commitHistory=async(input,signal)=>{if(input.context?.closed){if(lost)await original(input,signal);throw Error('injected closed CAS failure')}return original(input,signal)}
  const events:string[]=[];let terminal=false;for await(const event of managedContext(storedSession(store,scope,'failed-'+lost),async function*(){yield {type:'end'}})){if(event.type==='execution_checkpoint'&&['complete','stopped'].includes(event.checkpoint.status))terminal=true;events.push(event.type)}
  expect(terminal).toBe(false);expect(events).toContain('error');expect(events.indexOf('error')).toBeLessThan(events.indexOf('end'));expect(readChatContextSnapshot(scope.context.snapshot()).open).toEqual([])
 }
})
it('preserves exact recorded request bytes across an interrupted invocation and explicit acknowledged host close',async()=>{
 const store=new MemoryRunStore(),scope=new StoredChatScope(store,'c',new ChatContext({scope:'shared'})),admitted=await durableSession(storedSession(store,scope,'interrupted')),first=await contextualSession(admitted),binding=admitted.context![durableOwner]!,exact=String.raw`{ "body":"{\"z\":false,\"a\":null}", "method":"POST", "suffix":"/call" }`
 await binding.writer.startModel(binding,'source','fixture',exact);first.context.close();expect(readChatContextSnapshot(scope.context.snapshot()).open.map(t=>t.number)).toEqual([1])
 const restored=await restoreStoredChatScope(store,(await store.readHistoryHead('interrupted'))!.context!),nextInput=await durableSession(storedSession(store,restored,'interrupted',true)),ready=await contextualSession(nextInput),next=nextInput.context![durableOwner]!,handle=await next.writer.startModel(next,'source','fixture','{"changed":true}')
 expect(handle.request).toBe(exact);await next.writer.finishModel(next,handle,{ok:true});ready.context.close();const closed=await restored.close();expect(readChatContextSnapshot(await readStoredChatSnapshot(store,closed)).open).toEqual([]);expect(restored.context.turnCount).toBe(1)
})
it('consumes independent handwritten chunk and row digests',async()=>{
 const {default:chunks}=await import('../../../contract/vectors/storage/history-chunks.json'),{default:rows}=await import('../../../contract/vectors/storage/history-entries.json'),{readHistoryChunkManifest,historyBytesDigest,historyJSONDigest}=await import('../history/codec')
 for(const item of chunks.items){expect(readHistoryChunkManifest(bytes(item))).toEqual(item);expect(historyBytesDigest(new TextEncoder().encode(chunks.sourceText))).toBe(item.digest);expect(()=>readHistoryChunkManifest(bytes({...item,unknown:true}))).toThrow(HistoryInputInvalid)}
 for(const {digest,...body} of rows.items)expect(historyJSONDigest(body)).toBe(digest)
})

const criticReply=()=>new Response(JSON.stringify({id:'completion-critic',object:'chat.completion',created:0,model:'fixture',choices:[{index:0,message:{role:'assistant',content:'Reviewed.'},finish_reason:'stop'}],usage:{prompt_tokens:1,completion_tokens:1,total_tokens:2}}),{headers:{'content-type':'application/json'}})
it('restarts an actual persisted critic call with exact private scope/request and fresh later isolation',async()=>{
 const store=new MemoryRunStore(),chat=new ChatContext({scope:'shared'}),scope=new StoredChatScope(store,'c',chat),physical:{suffix:string;method:string;body:string}[]=[]
 const input=storedSession(store,scope,'critic');input.model.call=async(suffix,request)=>{physical.push({suffix,method:request.method??'POST',body:request.body});throw Error('interrupted transport')}
 const first=await contextualSession(input),author=await first.context.ledger.retain({id:'author-only',kind:'artifact',label:'Author private work',value:'SECRET-AUTHOR'}),other=chat.openTurn()
 first.context.beginCriticPhase();const privateScope=first.context.ledger.scope;expect(privateScope).not.toBe(chat.scope);expect(first.context.execution.snapshot().context.entries.some(row=>row.id===author.id)).toBe(false)
 chat.room(0,0);expect(chat.entries.has(author.id)).toBe(false);expect(chat.hasActiveTurn(other.number)).toBe(true)
 const request={method:'POST' as const,body:'{"z":false,"a":null}',signal:input.signal}
 await expect(first.session.values.forRole('critic').model.call('/chat/completions',request)).rejects.toThrow('interrupted transport')
 first.context.close();const held=(await store.readHistoryHead('critic'))!;expect(held.context).toMatchObject({closed:true,privateScope,turn:1});expect(readChatContextSnapshot(await readStoredChatSnapshot(store,held.context!)).open.map(t=>t.number)).toEqual([2])
 const restored=await restoreStoredChatScope(store,held.context!),nextInput=storedSession(store,restored,'critic',true);nextInput.model.call=async(suffix,request)=>{physical.push({suffix,method:request.method??'POST',body:request.body});return criticReply()}
 const next=await contextualSession(nextInput);expect(next.context.ledger.scope).toBe(privateScope);expect(restored.context.turnCount).toBe(2);next.context.beginCriticPhase();expect(next.context.ledger.scope).toBe(privateScope)
 await next.session.values.forRole('critic').model.call('/changed',{...request,body:'{"different":true}'})
 expect(physical).toHaveLength(2);expect(physical[1]).toEqual(physical[0]);expect(readChatContextSnapshot(restored.context.snapshot()).open.map(t=>t.number)).toEqual([2]);expect(next.context.execution.snapshot().context.entries.some(row=>row.id===author.id)).toBe(false)
 next.context.endCriticPhase();next.context.beginCriticPhase();expect(next.context.ledger.scope).not.toBe(privateScope);expect(restored.context.turnCount).toBe(2);next.context.close()
})
it('refuses a private-phase CAS loss before any physical critic dispatch',async()=>{
 for(const lost of [false,true]){const store=new MemoryRunStore(),scope=new StoredChatScope(store,'c',new ChatContext({scope:'shared'})),input=storedSession(store,scope,'critic-loss-'+lost),physical=vi.fn(async()=>criticReply());input.model.call=physical
 const ready=await contextualSession(input),commit=store.commitHistory.bind(store);ready.context.beginCriticPhase();store.commitHistory=async(value,signal)=>{if(value.context?.privateScope){if(lost)await commit(value,signal);throw Error('private phase CAS failed')}return commit(value,signal)}
 await expect(ready.session.values.forRole('critic').model.call('/chat/completions',{method:'POST',body:'{}',signal:input.signal})).rejects.toThrow();expect(physical).not.toHaveBeenCalled();expect(scope.context.hasActiveTurn(1)).toBe(false);ready.context.close()}
})
it('refuses fabricated private bindings and a restored pending author request at critic entry',async()=>{
 const store=new MemoryRunStore(),scope=new StoredChatScope(store,'c',new ChatContext({scope:'shared'})),admitted=await durableSession(storedSession(store,scope,'bad-private')),binding=admitted.context![durableOwner]!,first=await contextualSession(admitted),head=(await store.readHistoryHead('bad-private'))!
 await expect(restoreStoredChatScope(store,{...head.context!,closed:true,privateScope:'invented'})).rejects.toThrow(ContextTurnBindingInvalid)
 first.context.beginCriticPhase();await binding.writer.startModel(binding,'source','fixture','{"wrongRole":true}');first.context.close()
 const held=(await store.readHistoryHead('bad-private'))!
 await expect(restoreStoredChatScope(store,{...held.context!,privateScope:undefined})).rejects.toThrow(ContextTurnBindingInvalid)
 const mismatched=await restoreStoredChatScope(store,{...held.context!,privateScope:'mismatched-scope'});await expect(contextualSession(storedSession(store,mismatched,'bad-private',true))).rejects.toThrow()
 const unrelated=await restoreStoredChatScope(store,held.context!);await expect(contextualSession(storedSession(store,unrelated,'unrelated',true))).rejects.toThrow()
 const restored=await restoreStoredChatScope(store,held.context!),next=await contextualSession(storedSession(store,restored,'bad-private',true));expect(()=>next.context.beginCriticPhase()).toThrow(ContextTurnBindingInvalid);next.context.close()
})

it('preserves paused critic checkpoint and typed pending request through acknowledged runner cleanup',async()=>{
 const store=new MemoryRunStore(),scope=new StoredChatScope(store,'c',new ChatContext({scope:'shared'})),admitted=await durableSession(storedSession(store,scope,'paused-critic')),binding=admitted.context![durableOwner]!,exact='{ "body":"critic request", "method":"POST", "suffix":"/call" }';let privateScope=''
 for await(const _ of managedContext(admitted,async function*(prepared){const context=contextOf(prepared);context.beginCriticPhase();privateScope=context.ledger.scope;await binding.writer.startModel(scopeFor(binding,'critic'),'source','fixture',exact);yield {type:'budget_paused',checkpoint:context.execution.snapshot('paused')};yield {type:'end'}})){}
 const held=(await store.readHistoryHead('paused-critic'))!,current=(await store.load('paused-critic'))!,saved=readExecution(JSON.parse(new TextDecoder().decode(current.checkpoint!)))!;expect(saved.status).toBe('paused');expect(saved.context.scope).toBe(privateScope);expect(held.context!.privateScope).toBe(privateScope);expect(readChatContextSnapshot(scope.context.snapshot()).open).toEqual([])
 await store.commit({runId:'paused-critic',expectedRevision:current.revision,commitId:'explicit-host-critic-allowance',checkpoint:bytes(extendExecution(saved)),events:[]})
 const restored=await restoreStoredChatScope(store,held.context!),nextInput=await durableSession(storedSession(store,restored,'paused-critic',true)),nextBinding=nextInput.context![durableOwner]!,next=await contextualSession(nextInput);next.context.beginCriticPhase();expect(next.context.ledger.scope).toBe(privateScope)
 const handle=await nextBinding.writer.startModel(scopeFor(nextBinding,'critic'),'source','fixture','{"changed":true}');expect(handle.request).toBe(exact);expect(restored.context.turnCount).toBe(1);await nextBinding.writer.finishModel(scopeFor(nextBinding,'critic'),handle,{ok:true});next.context.close()
})

it('preserves structurally typed extra host members in supported TS task/reuse rows',()=>{
 const value=structuredClone(vectors.items[0]!);value.threads[0]!.exchangesJSON='[{"objective":"Ask","result":"Old","hostExtra":{"z":false,"a":null}}]';value.reusable[0]!.value.questionsJSON='[{"kind":"question","text":"Typed","hostExtra":false}]'
 const raw=bytes({...value,version:CHAT_SNAPSHOT_VERSION}),restored=ChatContext.restore(raw);expect(restored.snapshot()).toEqual(raw);expect((restored.task('one')!.exchanges[0] as any).hostExtra).toEqual({z:false,a:null})
})
