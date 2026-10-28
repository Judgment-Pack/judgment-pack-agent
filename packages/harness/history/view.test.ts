import {it,expect,vi} from 'vitest'
import {MemoryRunStore} from '../storage/run'
import {managedContext} from '../context/managed'
import {HistoryChanged,HistoryInputInvalid,historyJSONBytes,copyHistoryBytes} from './codec'
import {copyHistoryEnvelope,readRetainedRunHistory,RetainedRunHistory} from './view'
import type {AssistantSession} from '../engine'
const session=(store:MemoryRunStore):AssistantSession=>({prompt:'Work',testPrompt:'',tools:[],hostTools:[],callTool:vi.fn(async()=>({content:[]})),model:{family:'openai-compatible',model:'fixture',call:vi.fn(async()=>{throw Error('No provider in this history fixture')})},signal:new AbortController().signal,context:{currentRequest:{message:'Work',input:'Work'},runStore:{store,id:'r'}}})
async function completed(){const store=new MemoryRunStore();for await(const _ of managedContext(session(store),async function*(){yield {type:'message',text:'Done'};yield {type:'end'}})){}return store}
const feedback={version:1 as const,id:'f',runId:'r',sequence:1,previous:null,target:{kind:'run' as const},rating:'bad' as const,actor:'user',recordedAt:'2026-10-25T01:02:03.000Z'}
it('collects actual writer bytes and checkpoint-only commits as a detached consistent view',async()=>{
 const store=await completed(),head=(await store.readHistoryHead('r'))!,view=await readRetainedRunHistory(store,'r')
 expect(view.head).toEqual(head);expect(view.commits).toHaveLength(head.commitCount);expect(view.commits.some(row=>row.events.length===0)).toBe(true);expect(view.commits.reduce((sum,row)=>sum+row.events.length,0)).toBe(head.eventCount)
 const checkpoint=view.readObject(head.checkpoint!);expect(checkpoint).toEqual((await store.load('r'))!.checkpoint);checkpoint[0]=0;expect(view.readObject(head.checkpoint!)[0]).not.toBe(0)
 const rows=view.commits;rows[0]!.commitId='changed';expect(view.commits[0]!.commitId).not.toBe('changed')
})
it('detects metadata-only publication during collection even though execution revision stays unchanged',async()=>{
 const store=await completed(),execution=(await store.load('r'))!,read=store.readHistoryPage.bind(store);let injected=false
 store.readHistoryPage=async(...args)=>{const page=await read(...args);if(!injected){injected=true;const head=(await store.readHistoryHead('r'))!;await store.commitHistoryMetadata({runId:'r',expectedHistoryRevision:head.revision,expectedMetadataRevision:null,commitId:'feedback-race',record:{version:1,kind:'feedback',feedback}})}return page}
 await expect(readRetainedRunHistory(store,'r')).rejects.toThrow(HistoryChanged);expect((await store.load('r'))!.revision).toBe(execution.revision)
 const view=await readRetainedRunHistory(store,'r');expect(view.metadata).toHaveLength(1);expect(view.metadata[0]!.executionRevision).toBe(execution.revision);expect(view.metadata[0]!.commitSequence).toBe(view.head.commitCount)
})
it('refuses truncated/out-of-order/mismatched pages and altered object bytes rather than returning partial history',async()=>{
 for(const mutate of [(page:any)=>({...page,items:[],hasMore:true}),(page:any)=>({...page,nextSequence:page.nextSequence+1}),(page:any)=>({...page,total:page.total+1}),(page:any)=>({...page,items:[...page.items].reverse()})]){const store=await completed(),read=store.readHistoryPage.bind(store);store.readHistoryPage=async(...args)=>mutate(await read(...args));await expect(readRetainedRunHistory(store,'r')).rejects.toThrow(HistoryInputInvalid)}
 const store=await completed(),read=store.readHistoryObject.bind(store);store.readHistoryObject=async(...args)=>{const value=await read(...args);value.value[0]^=1;return value};await expect(readRetainedRunHistory(store,'r')).rejects.toThrow(HistoryInputInvalid)
})
it('rejects getters, executable/sparse/cyclic envelope members without reading them',()=>{
 let executed=false;const value={};Object.defineProperty(value,'runId',{enumerable:true,get(){executed=true;throw Error('getter')}})
 expect(()=>copyHistoryEnvelope(value)).toThrow(HistoryInputInvalid);expect(executed).toBe(false);expect(()=>copyHistoryEnvelope({fn(){}})).toThrow(HistoryInputInvalid);const cycle:any={};cycle.next=cycle;expect(()=>copyHistoryEnvelope(cycle)).toThrow(HistoryInputInvalid);expect(()=>copyHistoryEnvelope(Array(3))).toThrow(HistoryInputInvalid)
 expect(()=>copyHistoryEnvelope({deep:historyJSONBytes({value:1})})).toThrow(HistoryInputInvalid)
})

it('does not execute object-page accessors or byte-array overrides while copying untrusted port data',async()=>{
 let executed=false;const store=await completed(),read=store.readHistoryObject.bind(store)
 store.readHistoryObject=async(...args)=>{const page=await read(...args);Object.defineProperty(page,'value',{enumerable:true,get(){executed=true;throw Error('accessor')}});return page}
 await expect(readRetainedRunHistory(store,'r')).rejects.toThrow(HistoryInputInvalid);expect(executed).toBe(false)
 const value=new Uint8Array([1,2,3]);Object.defineProperty(value,'byteLength',{get(){executed=true;throw Error('length')}});Object.defineProperty(value,Symbol.iterator,{get(){executed=true;throw Error('iterator')}})
 expect(copyHistoryBytes(value)).toEqual(new Uint8Array([1,2,3]));expect(executed).toBe(false)
})

it('does not allow a caller to mint a validated history view',()=>{expect(()=>new RetainedRunHistory(Symbol(),{} as never,[],[],new Map())).toThrow(HistoryInputInvalid)})
