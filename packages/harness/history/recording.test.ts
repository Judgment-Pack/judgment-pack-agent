import {it,expect,vi} from 'vitest'
import {MemoryRunStore} from '../storage/run'
import {managedContext} from '../context/managed'
import type {AssistantSession} from '../engine'
import {HistoryInputInvalid,HISTORY_OBJECT_BYTES,historyText,readHistoryChunkManifest,historyJSONBytes} from './codec'
import {prepareHistoryOriginal,prepareHistoryCompaction,prepareHistoryGenesis,prepareCaptureExpectation,stageHistoryObservation} from './recording'
import {readRetainedRunHistory} from './view'
import {readHistoryObservation} from './metadata'
it('stages chunked ordered public originals privately and publishes only through the real owner CAS',async()=>{
 const store=new MemoryRunStore(),session:AssistantSession={prompt:'Work',testPrompt:'',tools:[],hostTools:[],callTool:vi.fn(async()=>({content:[]})),model:{family:'openai-compatible',model:'fixture',call:vi.fn()},signal:new AbortController().signal,context:{currentRequest:{message:'Work',input:'Work'},runStore:{store,id:'r'}}}
 for await(const _ of managedContext(session,async function*(){yield {type:'end'}})){}
 const source=JSON.stringify([{role:'assistant',content:'x'.repeat(HISTORY_OBJECT_BYTES),extra:{z:0.1,a:false,none:null}}]),prepared=await prepareHistoryOriginal({execution:'r',role:'author'},'boundary',source,'messages',0),head=(await store.readHistoryHead('r'))!,ref=await stageHistoryObservation(store,'r',prepared)
 await expect(store.readHistoryObject({kind:'run',id:'r'},head.revision,ref.digest,0,HISTORY_OBJECT_BYTES)).rejects.toThrow()
 if(prepared.observation.kind!=='original')throw Error('original expected')
 const original=prepared.observation,manifest=readHistoryChunkManifest(prepared.objects.find(row=>row.digest===original.value.digest)!.value);expect(manifest.parts).toHaveLength(2)
 prepared.objects[0]!.value[0]^=1
 const actual=(await store.load('r'))!;await store.commitHistory({run:{runId:'r',expectedRevision:actual.revision,commitId:'original-evidence',checkpoint:actual.checkpoint,events:[]},observations:[ref]})
 const view=await readRetainedRunHistory(store,'r'),full=new Uint8Array(manifest.bytes);let offset=0
 for(const part of manifest.parts){const value=view.readObject(part);full.set(value,offset);offset+=value.length}
 expect(historyText(full)).toBe(source);await expect(stageHistoryObservation(store,'r',prepared)).rejects.toThrow(HistoryInputInvalid)
})
it('keeps genesis and capture expectations closed and distinguishes actual recorder surfaces',async()=>{
 const scope={execution:'r',role:'author'},genesis=await prepareHistoryGenesis(scope);expect(genesis.observation.kind).toBe('genesis')
 const identity={scope,dispatchId:'d',kind:'model' as const,source:'provider',tool:'model',requestDigest:'sha256:'+'a'.repeat(64),surface:'session-model' as const},expectation=await prepareCaptureExpectation(identity)
 expect(expectation.observation).toMatchObject({kind:'capture-request',captureKind:'model',surface:'session-model'})
 await expect(prepareCaptureExpectation({...identity,surface:'native-aggregate'})).rejects.toThrow(HistoryInputInvalid)
 expect(()=>readHistoryObservation(historyJSONBytes({...genesis.observation,profile:{version:1,profile:'full-history',genesis:true,originals:true,compactions:false,requests:true}}))).toThrow(HistoryInputInvalid)
})
it('refuses unacknowledged originals, wrong prior kinds and cross-scope compaction before publication',async()=>{
 const store=new MemoryRunStore(),session:AssistantSession={prompt:'Work',testPrompt:'',tools:[],hostTools:[],callTool:vi.fn(async()=>({content:[]})),model:{family:'openai-compatible',model:'fixture',call:vi.fn()},signal:new AbortController().signal,context:{currentRequest:{message:'Work',input:'Work'},runStore:{store,id:'r'}}}
 for await(const _ of managedContext(session,async function*(){yield {type:'end'}})){}
 const scope={execution:'r',role:'author'},original=await prepareHistoryOriginal(scope,'boundary','[{"role":"assistant","content":"original"}]','messages',0),ref=await stageHistoryObservation(store,'r',original)
 const publish=async(observations:typeof ref[])=>{const held=(await store.load('r'))!;return store.commitHistory({run:{runId:'r',expectedRevision:held.revision,commitId:crypto.randomUUID(),checkpoint:held.checkpoint,events:[]},observations})}
 const compact=await prepareHistoryCompaction(scope,'boundary',[ref],'"replacement"','index',[],'legacy-digest'),compacted=await stageHistoryObservation(store,'r',compact),before=(await store.readHistoryHead('r'))!
 await expect(publish([compacted])).rejects.toThrow(HistoryInputInvalid);await expect(publish([ref,compacted])).rejects.toThrow(HistoryInputInvalid);expect((await store.readHistoryHead('r'))!.revision).toBe(before.revision)
 await publish([ref]);await publish([compacted]);const valid=(await store.readHistoryHead('r'))!
 for(const bad of [await prepareHistoryCompaction(scope,'next',[ref],'"replacement"','summary',[ref],'legacy-digest'),await prepareHistoryCompaction({...scope,role:'critic'},'next',[ref],'"replacement"','summary',[],'legacy-digest')]){const invalid=await stageHistoryObservation(store,'r',bad);await expect(publish([invalid])).rejects.toThrow(HistoryInputInvalid);await expect(store.readHistoryObject({kind:'run',id:'r'},valid.revision,invalid.digest,0,HISTORY_OBJECT_BYTES)).rejects.toThrow()}
 expect((await readRetainedRunHistory(store,'r')).head).toEqual(valid)
})
