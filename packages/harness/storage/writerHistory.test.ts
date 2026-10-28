import {it,expect,vi} from 'vitest'
import {MemoryRunStore} from './run'
import {RunWriter,ContinuationIdentityChanged,ContinuationNotRecoverable,type DurableBinding} from './writer'
import {RECORD_DEFAULTS,writeRecord,CHECKPOINT_SCHEMA} from '../records'
import {readExecution,type ExecutionCheckpoint} from '../execution/state'
import {canonicalJSON,ChatContext} from '../context/chat'
import {StoredChatScope} from '../context/storedChat'
import {readRetainedRunHistory} from '../history/view'
import {readHistoryObservation} from '../history/metadata'
import {historyBytesDigest,historyJSONDigest,HISTORY_OBJECT_BYTES} from '../history/codec'
import {prepareHistoryOriginal,stageHistoryObservation} from '../history/recording'
const checkpoint=(id:string):ExecutionCheckpoint=>({version:2,id,requestKey:'a'.repeat(64),phase:'work',status:'running',unit:'model-requests',used:0,limit:20,grants:[],plan:[],explanation:'',operations:[],context:{scope:'fixture',entries:[]}})
async function setup(store:MemoryRunStore,id='history-writer',resume=false){const writer=new RunWriter(store,id,resume?await store.load(id):undefined),binding:DurableBinding={writer,root:true,scope:{execution:id,role:'author'}};writer.bind(()=>writer.checkpoint??checkpoint(id),RECORD_DEFAULTS,()=>undefined);return {writer,binding}}
async function rows(store:MemoryRunStore,id='history-writer'){const view=await readRetainedRunHistory(store,id);return {view,observations:view.commits.flatMap(commit=>commit.observations.map(ref=>readHistoryObservation(view.readObject(ref))))}}
it('publishes one fresh genesis and exact original/request rows through the original MemoryRunStore writer',async()=>{
 const store=new MemoryRunStore(),{writer,binding}=await setup(store),send=vi.fn(async()=>({value:'saved'}))
 await writer.invoke(binding,'host','source','write','{"value":1}',false,send)
 const {view,observations}=await rows(store),scope={execution:writer.id,role:'author'}
 expect(view.head.coverage).toBe('recording-incomplete');expect(observations.filter(row=>row.kind==='genesis')).toEqual([{version:1,kind:'genesis',scope,profile:{version:1,profile:'full-history',genesis:true,originals:true,compactions:true,requests:true}}])
 expect(observations.filter(row=>row.kind==='original')).toHaveLength(1)
 const request=observations.find(row=>row.kind==='request')!;expect(request).toMatchObject({version:1,kind:'request',scope,source:'source',tool:'write',compactions:[]});expect(request.kind==='request'&&request.originals).toHaveLength(1)
 expect(send).toHaveBeenCalledOnce();expect(view.commits[0]!.observations).toHaveLength(3)
 const next=await setup(store,writer.id,true);await next.writer.event(next.binding,{type:'message',text:'after resume'})
 const resumed=await rows(store);expect(resumed.observations.filter(row=>row.kind==='genesis')).toHaveLength(1);expect(resumed.observations.filter(row=>row.kind==='request')).toHaveLength(1);expect(resumed.observations.filter(row=>row.kind==='original')).toHaveLength(1)
 await next.writer.invoke(next.binding,'host','source','write','{"value":1}',false,send);expect(send).toHaveBeenCalledOnce()
})
it('keeps pre-recording legacy history disabled without fabricating originals or genesis',async()=>{
 const store=new MemoryRunStore();await store.commit({runId:'legacy',expectedRevision:null,commitId:'before-recording',checkpoint:new TextEncoder().encode(canonicalJSON(checkpoint('legacy'))),events:[]})
 const {writer,binding}=await setup(store,'legacy',true);await writer.save(binding,writer.checkpoint!)
 const {view,observations}=await rows(store,'legacy');expect(view.head.coverage).toBe('legacy-incomplete');expect(observations).toEqual([])
 await expect(writer.historyOriginal(binding,'boundary','[]','messages',0)).rejects.toBeInstanceOf(ContinuationNotRecoverable)
})
it('publishes checkpoint, events, chat and observations atomically through the same original commit',async()=>{
 const store=new MemoryRunStore(),source=new StoredChatScope(store,'chat',new ChatContext({scope:'fixture'}));source.openTurn()
 const {writer,binding}=await setup(store);writer.attachContext(source)
 const original=store.commitHistory.bind(store);let commits=0
 store.commitHistory=async(input,signal)=>{commits++;expect(input.context).toBeDefined();expect(input.observations.length).toBeGreaterThan(0);expect(await store.load(writer.id)).toBeUndefined();expect(await store.readHistoryHead(writer.id)).toBeUndefined();expect(await store.loadContextSnapshot('chat')).toBeUndefined();const receipt=await original(input,signal);expect((await store.load(writer.id))!.revision).toBe(receipt.revision);expect((await store.readHistoryHead(writer.id))!.root).toBe(receipt.history.root);expect((await store.loadContextSnapshot('chat'))!.revision).toBe(receipt.history.context!.revision);return receipt}
 await writer.event(binding,{type:'message',text:'exact event'});expect(commits).toBe(1)
 const {view}=await rows(store);expect(view.commits[0]!.events).toEqual([{digest:historyBytesDigest(new TextEncoder().encode(canonicalJSON({version:1,scope:binding.scope,event:{type:'message',text:'exact event',version:1}}))),bytes:new TextEncoder().encode(canonicalJSON({version:1,scope:binding.scope,event:{type:'message',text:'exact event',version:1}})).length}])
 expect(view.head.context?.chatId).toBe('chat')
})
it('keeps orphan staging private and publishes exact-scope helper refs only after acknowledgement',async()=>{
 const store=new MemoryRunStore(),{writer,binding}=await setup(store);await writer.save(binding,checkpoint(writer.id))
 const before=(await store.readHistoryHead(writer.id))!,orphan=await stageHistoryObservation(store,writer.id,await prepareHistoryOriginal(binding.scope,'orphan','[]','messages',0))
 await expect(store.readHistoryObject({kind:'run',id:writer.id},before.revision,orphan.digest,0,HISTORY_OBJECT_BYTES)).rejects.toThrow()
 const original=store.commitHistory.bind(store);let release!:()=>void,entered!:()=>void
 const blocked=new Promise<void>(resolve=>release=resolve),waiting=new Promise<void>(resolve=>entered=resolve)
 const exactCheckpoint=(await store.load(writer.id))!.checkpoint!,latest=writer.checkpoint
 store.commitHistory=async(input,signal)=>{expect(input.run.checkpoint).toEqual(exactCheckpoint);expect(input.run.events).toEqual([]);entered();await blocked;return original(input,signal)}
 let returned=false;const pending=writer.historyOriginal(binding,'messages','[{"role":"user","content":"exact"}]','messages',0).then(ref=>{returned=true;return ref})
 await waiting;expect(returned,'helper leaked an unacknowledged reference').toBe(false);release();const ref=await pending;expect(returned).toBe(true)
 const {view}=await rows(store);expect(view.head.commitCount).toBe(before.commitCount+1);expect(view.head.eventCount).toBe(before.eventCount);expect(view.head.root).not.toBe(before.root);expect(view.head.executionRevision).not.toBe(before.executionRevision);expect(view.head.checkpoint).toEqual(before.checkpoint);expect(writer.checkpoint).toEqual(latest);expect(view.commits.at(-1)!.eventStart).toBe(before.eventCount+1);expect(()=>view.readObject(ref)).not.toThrow();expect(()=>view.readObject(orphan)).toThrow()
 store.commitHistory=original
 const compact=await writer.historyCompaction(binding,'compact',[ref],'"replacement"','summary',[],'legacy')
 expect(readHistoryObservation((await rows(store)).view.readObject(compact))).toMatchObject({kind:'compaction',originals:[ref]})
})
it('isolates child originals and refuses cross-scope compaction and foreign writer helpers',async()=>{
 const store=new MemoryRunStore(),{writer,binding}=await setup(store);await writer.save(binding,checkpoint(writer.id))
 const child:DurableBinding={writer,root:false,scope:{execution:'child',role:'author',task:'child'}};await writer.save(child,readExecution(writeRecord(CHECKPOINT_SCHEMA,{...checkpoint('child')},RECORD_DEFAULTS))!)
 const original=await writer.historyOriginal(child,'child-messages','[]','messages',0),{observations}=await rows(store)
 expect(observations.filter(row=>row.kind==='original').map(row=>row.scope)).toContainEqual(child.scope)
 await expect(writer.historyCompaction(binding,'cross-scope',[original],'"replacement"','summary',[],'legacy')).rejects.toThrow()
 const next=await setup(store,writer.id,true),foreign={...next.binding,writer};await expect(next.writer.historyOriginal(foreign,'wrong','[]','messages',0)).rejects.toBeInstanceOf(ContinuationIdentityChanged)
})
for(const mutation of ['missing-head','head-checkpoint','row-commit','row-previous','row-sequence','row-revision','row-checkpoint','row-events','row-observations','row-event-start','lost-ack'])it(`refuses ${mutation} before dispatch and permanently closes the original writer`,async()=>{
 const store=new MemoryRunStore(),{writer,binding}=await setup(store);if(mutation!=='missing-head')await writer.save(binding,checkpoint(writer.id));const before=writer.checkpoint,send=vi.fn(async()=>1),commit=store.commitHistory.bind(store),page=store.readHistoryPage.bind(store);let writes=0
 let altered:Awaited<ReturnType<typeof page>>|undefined
 store.commitHistory=async(input,signal)=>{writes++;const receipt=await commit(input,signal);if(mutation==='lost-ack')throw Error('lost acknowledgement');if(mutation==='missing-head')delete (receipt as Partial<typeof receipt>).history;if(mutation==='head-checkpoint')receipt.history.checkpoint!.bytes++
  if(mutation.startsWith('row-')){altered=await page(writer.id,receipt.history.revision,receipt.history.commitCount-1,1);const row=altered.items[0]!;if(mutation==='row-commit')row.commitId='different';if(mutation==='row-previous')row.previousDigest='sha256:'+'a'.repeat(64);if(mutation==='row-sequence')row.sequence++;if(mutation==='row-revision')row.executionRevision='different';if(mutation==='row-checkpoint')row.checkpoint!.bytes++;if(mutation==='row-events')row.events=[{digest:'sha256:'+'b'.repeat(64),bytes:1}];if(mutation==='row-observations')row.observations=[];if(mutation==='row-event-start')row.eventStart++;row.eventCount=row.events.length;const {digest:_,...body}=row;row.digest=historyJSONDigest(body);receipt.history.root=row.digest}
  return receipt}
 store.readHistoryPage=async(...args)=>altered??page(...args)
 await expect(writer.invoke(binding,'host','source','write','{}',false,send)).rejects.toThrow();expect(send,'dispatch escaped a refused history acknowledgement').not.toHaveBeenCalled();expect(writer.checkpoint,'local checkpoint advanced on refused history acknowledgement').toEqual(before)
 await expect(writer.invoke(binding,'host','source','write','{}',false,send)).rejects.toThrow();expect(writes,'writer repeated an uncertain publication').toBe(1);expect(send).not.toHaveBeenCalled()
})
it('does not repeat an original send when the result commit loses acknowledgement',async()=>{
 const store=new MemoryRunStore(),{writer,binding}=await setup(store),commit=store.commitHistory.bind(store),send=vi.fn(async()=>1)
 store.commitHistory=async(input,signal)=>{const receipt=await commit(input,signal);if(JSON.parse(new TextDecoder().decode(input.run.checkpoint!)).durable.dispatches.some((row:{state:string})=>row.state==='result'))throw Error('lost result acknowledgement');return receipt}
 await expect(writer.invoke(binding,'host','source','write','{}',false,send)).rejects.toThrow();expect(send).toHaveBeenCalledOnce();store.commitHistory=commit
 const next=await setup(store,writer.id,true);await next.writer.invoke(next.binding,'host','source','write','{}',false,send);expect(send).toHaveBeenCalledOnce()
})
it('strictly refuses a malformed retained head before staging or dispatch',async()=>{
 const store=new MemoryRunStore(),{writer,binding}=await setup(store);await writer.save(binding,checkpoint(writer.id))
 const next=await setup(store,writer.id,true),read=store.readHistoryHead.bind(store);store.readHistoryHead=async(...args)=>{const head=(await read(...args))!;return {...head,extra:true}}
 const stage=vi.spyOn(store,'stageHistoryObjects'),send=vi.fn(async()=>1)
 await expect(next.writer.invoke(next.binding,'host','source','write','{}',false,send)).rejects.toThrow();expect(stage).not.toHaveBeenCalled();expect(send).not.toHaveBeenCalled()
})
it('preserves lexical request JSON and large values while refusing ambiguous or malformed requests',async()=>{
 const exact=String.raw` { "n":9007199254740993, "negative":-0, "exponent":1e+3, "escaped":"\u0061\/", "fraction":0.10000000000000001 } `
 const store=new MemoryRunStore(),{writer,binding}=await setup(store),send=vi.fn(async()=>1)
 await writer.invoke(binding,'host','source','write',exact,false,send)
 const {view,observations}=await rows(store),request=observations.find(row=>row.kind==='request')!;if(request.kind!=='request')throw Error('request expected')
 const {readHistoryChunkManifest,historyText,readHistoryRequestJSON,readCanonicalHistoryJSON,HistoryInputInvalid}=await import('../history/codec'),manifest=readHistoryChunkManifest(view.readObject(request.request))
 expect(historyText(view.readObject(manifest.parts[0]!))).toBe(exact)
 for(const bad of ['{"x":1,"x":2}','{"x":1, "\\u0078":2}','{"x":01}','{"x":1e9999}','{"x":"\\ud800"}','{"x":1} trailing','[1,]'])expect(()=>readHistoryRequestJSON(bad),bad).toThrow(HistoryInputInvalid)
 expect(()=>readCanonicalHistoryJSON(new TextEncoder().encode('{"n":9007199254740993}'),100,'finite')).toThrow(HistoryInputInvalid)
})
it('keeps helper checkpoint bytes and state coherent with an attached chat on resume',async()=>{
 const store=new MemoryRunStore(),source=new StoredChatScope(store,'chat',new ChatContext({scope:'fixture'}));source.openTurn()
 const {writer,binding}=await setup(store);writer.attachContext(source);await writer.save(binding,checkpoint(writer.id))
 const before=(await store.readHistoryHead(writer.id))!,bytes=(await store.load(writer.id))!.checkpoint!,state=writer.checkpoint
 await writer.historyOriginal(binding,'messages','[]','messages',0)
 const after=(await store.readHistoryHead(writer.id))!;expect(after.checkpoint).toEqual(before.checkpoint);expect((await store.load(writer.id))!.checkpoint).toEqual(bytes);expect(writer.checkpoint).toEqual(state);expect(after.context!.revision).not.toBe(before.context!.revision);expect(after.eventCount).toBe(before.eventCount)
 const resumed=await setup(store,writer.id,true);const ref=await resumed.writer.historyOriginal(resumed.binding,'resumed','[]','messages',0),retained=await rows(store);expect(()=>retained.view.readObject(ref)).not.toThrow()
})
it('never returns helper refs or advances local state after a lost publication acknowledgement',async()=>{
 const store=new MemoryRunStore(),{writer,binding}=await setup(store);await writer.save(binding,checkpoint(writer.id));const original=store.commitHistory.bind(store),state=writer.checkpoint;let writes=0
 store.commitHistory=async(input,signal)=>{writes++;await original(input,signal);throw Error('lost helper acknowledgement')}
 await expect(writer.historyOriginal(binding,'messages','[]','messages',0)).rejects.toThrow();expect(writer.checkpoint).toEqual(state)
 await expect(writer.historyOriginal(binding,'messages','[]','messages',0)).rejects.toThrow();expect(writes).toBe(1)
 store.commitHistory=original;const resumed=await setup(store,writer.id,true);await resumed.writer.historyOriginal(resumed.binding,'after-resume','[]','messages',0)
})
it('does not acknowledge chat locally until the exact history row has also been verified',async()=>{
 const store=new MemoryRunStore(),source=new StoredChatScope(store,'chat',new ChatContext({scope:'fixture'}));source.openTurn()
 const {writer,binding}=await setup(store);writer.attachContext(source);await writer.save(binding,checkpoint(writer.id));const before=(await source.prepare()).expectedRevision,read=store.readHistoryPage.bind(store)
 store.readHistoryPage=async(...args)=>{const page=await read(...args);page.items=[];return page}
 await expect(writer.invoke(binding,'host','source','write','{}',false,async()=>1)).rejects.toThrow()
 expect((await source.prepare()).expectedRevision,'chat acknowledgement advanced before history validation').toBe(before)
})
it('republishes exact acknowledged checkpoint bytes after resume despite a changed live snapshot',async()=>{
 const store=new MemoryRunStore(),first=await setup(store);await first.writer.save(first.binding,checkpoint(first.writer.id));const exact=(await store.load(first.writer.id))!.checkpoint!
 const {writer,binding}=await setup(store,first.writer.id,true),before=writer.checkpoint,changed=checkpoint(writer.id);changed.used=7;writer.bind(()=>changed,RECORD_DEFAULTS,()=>undefined)
 await writer.historyOriginal(binding,'messages','[]','messages',0)
 expect((await store.load(writer.id))!.checkpoint,'helper regenerated acknowledged checkpoint bytes').toEqual(exact);expect(writer.checkpoint).toEqual(before)
})

for(const kind of ['legacy-malformed','stale-resume'])it(`refuses ${kind} initialization before staging or dispatch`,async()=>{
 const store=new MemoryRunStore(),id='history-init'
 if(kind==='legacy-malformed')await store.commit({runId:id,expectedRevision:null,commitId:'before-recording',checkpoint:new TextEncoder().encode(canonicalJSON(checkpoint(id))),events:[]})
 else {const first=await setup(store,id);await first.writer.save(first.binding,checkpoint(id))}
 const {writer,binding}=await setup(store,id,true)
 if(kind==='legacy-malformed'){const read=store.readHistoryHead.bind(store);store.readHistoryHead=async(...args)=>{const head=(await read(...args))!;return {...head,extra:true}}}
 else {const other=await setup(store,id,true);await other.writer.save(other.binding,{...checkpoint(id),used:1})}
 const stage=vi.spyOn(store,'stageHistoryObjects'),send=vi.fn(async()=>1)
 await expect(writer.invoke(binding,'host','source','write','{}',false,send)).rejects.toThrow()
 expect(stage,'initialization allowed staging').not.toHaveBeenCalled();expect(send,'initialization allowed dispatch').not.toHaveBeenCalled()
})
