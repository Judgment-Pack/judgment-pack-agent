import {it,expect,vi} from 'vitest'
import type {ModelMessage} from 'ai'
import type {AssistantSession} from '../engine'
import {contextualSession} from './session'
import {normalize} from '../thinking'
import {MemoryRunStore,type RunStore} from '../storage/run'
import {durableSession,durableOwner} from '../storage/writer'
import {readRetainedRunHistory} from '../history/view'
import {readHistoryObservation} from '../history/metadata'
import {readHistoryChunkManifest,historyText,type HistoryObjectRef} from '../history/codec'

const exact=String.raw`{ "z":false, "zero":0, "escaped":"\u0061" }`
function fixture(id='one'){
 const call={type:'tool-call' as const,toolCallId:id,toolName:'check',input:{flag:false,zero:0,exact}},result={type:'tool-result' as const,toolCallId:id,toolName:'check',output:{type:'json' as const,value:{ok:false,count:0}}}
 const publicMessages:ModelMessage[]=[{role:'assistant',content:[call]},{role:'tool',content:[result]}]
 const messages:ModelMessage[]=[{role:'assistant',content:[{type:'reasoning',text:'PRIVATE-REASONING'},{...call,providerOptions:{google:{thoughtSignature:'TRANSPORT'.repeat(9000)}}}]},{role:'tool',content:[{...result,providerOptions:{google:{thoughtSignature:'OTHER-SIGNATURE'}}}]}]
 return {messages,publicMessages}
}
function input(store:RunStore,mode:'index'|'summary',resume=false,compactor=vi.fn(async()=> 'Public summary')):AssistantSession{
 return {prompt:'Current request',testPrompt:'',tools:[],hostTools:[],callTool:async()=>({content:[]}),model:{family:'gemini',model:'small',call:async()=>{throw Error('physical author transport')}},thinking:normalize('off','gemini'),signal:new AbortController().signal,policy:{compaction:mode},context:{runStore:{store,id:'compaction',resume},configuration:{version:1,models:{small:{contextWindowTokens:8192,outputTokens:1024}}},compactor}}
}
async function open(store:RunStore,mode:'index'|'summary',resume=false,compactor=vi.fn(async()=> 'Public summary')){const session=input(store,mode,resume,compactor),physical=vi.fn(session.model.call);session.model.call=physical;const admitted=await durableSession(session);return {...await contextualSession(admitted),binding:admitted.context![durableOwner]!,physical}}
async function retained(store:MemoryRunStore){const view=await readRetainedRunHistory(store,'compaction');const observations=view.commits.flatMap(commit=>commit.observations.map(ref=>readHistoryObservation(view.readObject(ref))));const source=(ref:HistoryObjectRef)=>{const manifest=readHistoryChunkManifest(view.readObject(ref));return historyText(new Uint8Array(manifest.parts.flatMap(part=>Array.from(view.readObject(part)))))};return {view,observations,source}}
for(const mode of ['index','summary'] as const)it(`retains exact public source and replacement through actual ${mode} session compaction`,async()=>{
 const store=new MemoryRunStore(),compactor=vi.fn(async()=> 'Public summary'),run=await open(store,mode,false,compactor),block=fixture(),history:ModelMessage[]=[{role:'user',content:'Task'},...block.messages]
 for(let i=0;i<16;i++)history.push({role:'assistant',content:'earlier public discussion '.repeat(35)},{role:'user',content:'continue'})
 const projected=await run.context.messages(history,'host',[]),{view,observations,source}=await retained(store),originals=observations.filter(row=>row.kind==='original'&&row.format==='messages'),compactions=observations.filter(row=>row.kind==='compaction')
 expect(originals).toHaveLength(1);expect(compactions).toHaveLength(1)
 const original=originals[0]!,compaction=compactions[0]!;if(original.kind!=='original'||compaction.kind!=='compaction')throw Error('typed rows missing')
 const consumed=JSON.parse(source(original.value)) as ModelMessage[]
 expect([...consumed,...projected.slice(2)]).toEqual([...block.publicMessages,...history.slice(3)])
 expect(source(original.value)).toContain(exact.replaceAll('\\','\\\\').replaceAll('"','\\"'))
 expect(source(original.value)).not.toMatch(/PRIVATE-REASONING|thoughtSignature|TRANSPORT|OTHER-SIGNATURE/)
 expect(JSON.parse(source(compaction.replacement))).toEqual(projected[1]);expect(compaction.mode).toBe(mode);expect(compaction.prior).toEqual([]);expect(compaction.boundary).toBe(run.context.ledger.scope);expect(compaction.scope).toEqual(run.binding.scope)
 expect(view.head.coverage).toBe('recording-incomplete');expect(compactor).toHaveBeenCalledTimes(mode==='summary'?1:0)
})
it('preserves older originals and replacements through repeat and explicit resume without invented occurrence edges',async()=>{
 const store=new MemoryRunStore();let run=await open(store,'index'),history:ModelMessage[]=[{role:'user',content:'Task'}];const replacements:ModelMessage[]=[]
 for(let i=0;i<3;i++){
  const previous=history[1];history=[...history,...fixture('call-'+i).messages];history=await run.context.messages(history,'host',[]);replacements.push(history[1]!)
  await run.binding.writer.save(run.binding,run.context.execution.snapshot())
  const {observations,source}=await retained(store),originals=observations.filter(row=>row.kind==='original'&&row.format==='messages'),compactions=observations.filter(row=>row.kind==='compaction')
  expect(originals).toHaveLength(i+1);expect(compactions).toHaveLength(i+1)
  for(const row of compactions)if(row.kind==='compaction')expect(row.prior,'text equality must not invent an occurrence link').toEqual([])
  if(i>0){const last=originals.at(-1)!;if(last.kind==='original')expect(JSON.parse(source(last.value))).toContainEqual(previous)}
  for(let j=0;j<=i;j++){const row=compactions[j]!;if(row.kind==='compaction')expect(JSON.parse(source(row.replacement))).toEqual(replacements[j])}
  if(i===0){run.context.close();history=JSON.parse(JSON.stringify(history));run=await open(store,'index',true)}
 }
})
for(const phase of ['original','replacement'])for(const fault of ['missing','lost'])it(`refuses ${fault} ${phase} acknowledgement before replacement use or author dispatch`,async()=>{
 const store=new MemoryRunStore(),compactor=vi.fn(async()=> 'Public summary'),run=await open(store,'summary',false,compactor),commit=store.commitHistory.bind(store),physical=run.physical
 const staged=new Map<string,Record<string,unknown>>(),stage=store.stageHistoryObjects.bind(store);store.stageHistoryObjects=async(resource,objects,signal)=>{for(const object of objects){try{staged.set(object.digest,JSON.parse(historyText(object.value)))}catch{/* chunk source is not an envelope */}}return stage(resource,objects,signal)}
 let faults=0;store.commitHistory=async(value,signal)=>{const receipt=await commit(value,signal);if(value.observations.some(ref=>{const row=staged.get(ref.digest);return phase==='original'?row?.kind==='original'&&row.format==='messages':row?.kind==='compaction'})){faults++;if(fault==='lost')throw Error('lost publication acknowledgement');delete (receipt as Partial<typeof receipt>).history}return receipt}
 const history:ModelMessage[]=[{role:'user',content:'Task'},...fixture().messages];for(let i=0;i<16;i++)history.push({role:'assistant',content:'public discussion '.repeat(40)},{role:'user',content:'continue'})
 let used=false;const attempt=async()=>{const projected=await run.context.messages(history,'host',[]);used=true;await run.session.values.model.call('/generateContent',{method:'POST',body:JSON.stringify({contents:projected}),signal:new AbortController().signal})}
 await expect(attempt()).rejects.toThrow();expect(used,'unacknowledged replacement was exposed').toBe(false);expect(physical).not.toHaveBeenCalled();expect(compactor).toHaveBeenCalledTimes(phase==='original'?0:1)
 await expect(attempt()).rejects.toThrow();expect(faults).toBe(1);expect(compactor).toHaveBeenCalledTimes(phase==='original'?0:1)
})
it('keeps optional-history absence compatible and ordinary summary failures use the index',async()=>{
 const memory=new MemoryRunStore(),commit=vi.fn(memory.commit.bind(memory)),store:RunStore={durability:memory.durability,load:memory.load.bind(memory),readEvents:memory.readEvents.bind(memory),commit},run=await open(store,'summary',false,vi.fn(async()=>{throw Error('ordinary compactor failure')}))
 const history:ModelMessage[]=[{role:'user',content:'Task'},...fixture().messages];for(let i=0;i<16;i++)history.push({role:'assistant',content:'public discussion '.repeat(40)},{role:'user',content:'continue'})
 commit.mockClear() // Admission already acknowledged its ordinary startup checkpoint.
 expect(JSON.stringify(await run.context.messages(history,'host',[]))).toContain('Completed tool calls must not be repeated');expect(commit).not.toHaveBeenCalled()
})
