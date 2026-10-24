import {it,expect,vi} from 'vitest'
import vectors from '../../../contract/vectors/storage/retained-value.json'
import {ContextLedger,contextDigest} from '../context/ledger'
import {CONTEXT_DEFAULTS} from '../context/policy'
import {canonicalJSON} from '../context/chat'
import {contextualSession} from '../context/session'
import {readExecution,type ExecutionCheckpoint} from '../execution/state'
import {checkRecord,UnknownRecordMember} from '../recordReader'
import {recordsPolicy,writeRecord,recordDigest,validRecord,CHECKPOINT_SCHEMA} from '../records'
import type {AssistantSession} from '../engine'
import {MemoryRunStore} from './run'
import {RunWriter,ContinuationNotRecoverable} from './writer'
const fixture=()=>structuredClone(vectors.items[0]) as ExecutionCheckpoint
const original=()=>{const cp=fixture(),entry=cp.context.entries[0]!;entry.value=JSON.parse(entry.valueJSON!);delete entry.valueJSON;return cp}
const ledger=()=>new ContextLedger({...CONTEXT_DEFAULTS})
it('reads the shared v2 pair, restores exact source order and keeps v1 frozen',async()=>{
 const cp=readExecution(fixture())!,held=ledger();await held.restore(cp.context)
 expect(JSON.stringify((held.read({id:cp.context.entries[0]!.id}) as {value:unknown}).value)).toBe(cp.context.entries[0]!.valueJSON)
 expect(held.snapshot().entries[0]!.digest).toBe(cp.context.entries[0]!.digest)
 expect(()=>checkRecord('checkpoint',{...cp,version:1})).toThrow(UnknownRecordMember)
 const legacy=original();legacy.version=1;checkRecord('checkpoint',legacy);await ledger().restore(legacy.context)
 const corrupt=structuredClone(legacy);corrupt.context.entries[0]!.value={changed:true};await expect(ledger().restore(corrupt.context)).rejects.toThrow(/recorded digest/)
})
for(const mode of ['raw','semantic','digest','bytes','missing-value','malformed','oversized','alternate-spelling'])it(`refuses forged retained representation: ${mode}`,async()=>{
 const cp=fixture(),e=cp.context.entries[0]!
 if(mode==='raw')e.valueJSON='{"z":3,"a":{"last":1,"first":0}}'
 if(mode==='semantic')e.value={different:true}
 if(mode==='digest')e.digest='0'.repeat(64)
 if(mode==='bytes')e.bytes++
 if(mode==='missing-value')delete (e as Partial<typeof e>).value
 if(mode==='malformed')e.valueJSON='{'
 if(mode==='oversized')e.valueJSON=' '.repeat(4*1024*1024+1)
 if(mode==='alternate-spelling')e.valueJSON=' '+e.valueJSON
 await expect(ledger().restore(cp.context)).rejects.toThrow(/recorded digest/)
})
it('preserves actual first/resumed model request bodies through canonical storage',async()=>{
 const store=new MemoryRunStore(),sent:string[]=[],cp=original(),source=cp.context.entries[0]!.value
 const raw:AssistantSession={prompt:'Read retained data',testPrompt:'',tools:[],hostTools:[],callTool:vi.fn(),critic:'off',signal:new AbortController().signal,model:{family:'openai-compatible',model:'fixture',call:async(_suffix,request)=>{sent.push(request.body);return new Response('{"choices":[{"index":0,"message":{"role":"assistant","content":"Done"},"finish_reason":"stop"}]}',{headers:{'content-type':'application/json'}})}},context:{currentRequest:{message:'Read',input:'Read'},documents:[{id:'opaque',label:'Ordered source',kind:'artifact',value:source}],runStore:{store,id:'retained-request'}}}
 for(const resume of [false,true]){
  const ready=await contextualSession({...raw,context:{...raw.context,runStore:{store,id:'retained-request',resume}}})
  const entry=ready.context.ledger.list().items.find(e=>e.label==='Ordered source')!
  const value=(ready.context.ledger.read({id:entry.contextId}) as {value:unknown}).value
  expect(JSON.stringify(value)).toBe(JSON.stringify(source))
  await ready.session.values.model.call('chat/completions',{body:JSON.stringify({retained:value})})
  ready.context.close()
 }
 expect(sent).toHaveLength(2);expect(sent[0]).toBe(sent[1]);expect(sent[0]).toContain('"z":2,"a":{"last":1,"first":0}')
 const bytes=(await store.load('retained-request'))!.checkpoint!,stored=JSON.parse(new TextDecoder().decode(bytes))
 expect(new TextDecoder().decode(bytes)).toBe(canonicalJSON(stored));expect(stored.context.entries.some((e:any)=>e.valueJSON===JSON.stringify(source))).toBe(true)
})
for(const mode of ['unchanged','value','remove-value','raw'])it(`guards nested child pairs before committing: ${mode}`,async()=>{
 const store=new MemoryRunStore(),commit=vi.spyOn(store,'commit'),root=original();root.id='root-nested';root.context.entries=[]
 const writer=new RunWriter(store,root.id),binding={writer,scope:{execution:'child-one',role:'author',task:'task-one'},root:false},child=original();child.id='child-one'
 const childRecord=writeRecord(CHECKPOINT_SCHEMA,{...child},recordsPolicy({}),undefined,new Date('2026-10-24T00:00:00Z'),'2030-01-01T00:00:00.000Z')
 writer.bind(()=>root,recordsPolicy({}),()=>({value<T>(input:T):T{if(input&&typeof input==='object'&&'context'in input){const changed=structuredClone(input) as unknown as ExecutionCheckpoint,e=changed.context.entries[0]!
  if(mode==='value')e.value={redacted:true};if(mode==='remove-value')delete (e as Partial<typeof e>).value;if(mode==='raw')e.valueJSON='{"private":"forged"}'
  return changed as T}return input}}))
 if(mode==='unchanged'){
  await writer.save(binding,childRecord);const saved=JSON.parse(new TextDecoder().decode((await store.load(root.id))!.checkpoint!))
  const state=saved.durable.children[0].checkpoint as ExecutionCheckpoint,held=ledger();expect(validRecord(state)).toBe(true);expect(state.retainUntil).toBe(childRecord.retainUntil);await held.restore(state.context)
  expect(JSON.stringify((held.read({id:state.context.entries[0]!.id}) as {value:unknown}).value)).toBe(fixture().context.entries[0]!.valueJSON)
 }else{await expect(writer.save(binding,childRecord)).rejects.toBeInstanceOf(ContinuationNotRecoverable);expect(commit).not.toHaveBeenCalled()}
})
it('keeps the 4MiB checkpoint bound when companion text duplicates retained content',async()=>{
 const root=original(),text='x'.repeat(2200000),value=JSON.stringify(text);root.context.entries=[{id:root.context.scope+':large',kind:'artifact',label:'Large',value:text,bytes:new TextEncoder().encode(value).length,digest:await contextDigest(value)}]
 const store=new MemoryRunStore(),commit=vi.spyOn(store,'commit'),writer=new RunWriter(store,root.id);writer.bind(()=>root,recordsPolicy({}),()=>undefined)
 await expect(writer.save({writer,scope:{execution:root.id,role:'author'},root:true},root)).rejects.toBeInstanceOf(ContinuationNotRecoverable);expect(commit).not.toHaveBeenCalled()
})

it('refuses a root guard redaction rather than preserving pre-guard companion text',async()=>{
 const root=original(),value=[{role:'assistant',content:'before guard'}],text=JSON.stringify(value)
 root.context.entries=[{id:root.context.scope+':guarded',kind:'history',label:'Guarded',value,bytes:new TextEncoder().encode(text).length,digest:await contextDigest(text)}]
 const store=new MemoryRunStore(),commit=vi.spyOn(store,'commit'),writer=new RunWriter(store,root.id)
 writer.bind(()=>root,recordsPolicy({}),()=>({value<T>(value:T):T{return (value==='before guard'?'after guard':value) as T}}))
 await expect(writer.save({writer,scope:{execution:root.id,role:'author'},root:true},root)).rejects.toBeInstanceOf(ContinuationNotRecoverable)
 expect(commit).not.toHaveBeenCalled()
})
for(const source of [{z:2,a:{last:1,first:0}},{z:'é\n\\"\u0001',a:{last:1,first:0}}])it('preserves limited callback first/resumed request bytes without adding a journal companion',async()=>{
 let saved:ExecutionCheckpoint|undefined;const sent:string[]=[],snapshots:ExecutionCheckpoint[]=[]
 const raw:AssistantSession={prompt:'Read retained data',testPrompt:'',tools:[],hostTools:[],callTool:vi.fn(),critic:'off',signal:new AbortController().signal,model:{family:'openai-compatible',model:'fixture',call:async(_suffix,request)=>{sent.push(request.body);return new Response('{}')}} ,context:{currentRequest:{message:'Read',input:'Read'},documents:[{id:'opaque',label:'Ordered source',kind:'artifact',value:source}],checkpoint:async cp=>{saved=structuredClone(cp);snapshots.push(saved)}}}
 for(const resume of [false,true]){
  const ready=await contextualSession({...raw,context:{...raw.context,...(resume?{execution:saved}: {})}})
  const entry=ready.context.ledger.list().items.find(e=>e.label==='Ordered source')!
  const value=(ready.context.ledger.read({id:entry.contextId}) as {value:unknown}).value
  expect(JSON.stringify(value)).toBe(JSON.stringify(source))
  await ready.session.values.model.call('chat/completions',{body:JSON.stringify({retained:value})});ready.context.close()
 }
 expect(sent).toHaveLength(2);expect(sent[0]).toBe(sent[1]);expect(snapshots.every(cp=>cp.durable===undefined&&cp.context.entries.every(e=>e.valueJSON===undefined))).toBe(true)
})
it('refuses corrupted legacy evidence before creating a strict companion or committing',async()=>{
 const root=original();root.context.entries[0]!.value={changed:true}
 const store=new MemoryRunStore(),commit=vi.spyOn(store,'commit'),writer=new RunWriter(store,root.id)
 writer.bind(()=>root,recordsPolicy({}),()=>undefined)
 await expect(writer.save({writer,scope:{execution:root.id,role:'author'},root:true},root)).rejects.toBeInstanceOf(ContinuationNotRecoverable)
 expect(commit).not.toHaveBeenCalled()
})

for(const version of [1,2] as const)it(`converts only a valid header-bearing child v${version} without changing retention or entry identity`,async()=>{
 const root=original();root.id='root-conversion';root.context.entries=[]
 const child=original();child.id='child-one'
 const record=writeRecord(CHECKPOINT_SCHEMA,{...child},recordsPolicy({}),undefined,new Date('2026-10-24T00:00:00Z'),'2030-01-01T00:00:00.000Z');record.version=version;record.digest=recordDigest(record)
 const before=structuredClone(record),store=new MemoryRunStore(),writer=new RunWriter(store,root.id);writer.bind(()=>root,recordsPolicy({}),()=>undefined)
 await writer.save({writer,scope:{execution:record.id,role:'author',task:'task-one'},root:false},record)
 const saved=JSON.parse(new TextDecoder().decode((await store.load(root.id))!.checkpoint!)).durable.children[0].checkpoint as ExecutionCheckpoint
 expect(saved.version).toBe(2);expect(validRecord(saved)).toBe(true);expect(saved.retainUntil).toBe(before.retainUntil);expect(saved.context.entries[0]).toMatchObject({digest:before.context.entries[0]!.digest,bytes:before.context.entries[0]!.bytes})
 expect(record).toEqual(before);await ledger().restore(saved.context)
})
for(const mode of ['digest','unsupported-version','missing-header','scope'])it(`refuses invalid original child conversion: ${mode}`,async()=>{
 const root=original();root.id='root-invalid';root.context.entries=[]
 const child=original();child.id='child-one'
 const record=writeRecord(CHECKPOINT_SCHEMA,{...child},recordsPolicy({}),undefined,new Date('2026-10-24T00:00:00Z'),'2030-01-01T00:00:00.000Z') as ExecutionCheckpoint
 if(mode==='digest')record.digest='sha256:'+'0'.repeat(64)
 if(mode==='unsupported-version'){(record as {version:number}).version=3;record.digest=recordDigest(record)}
 if(mode==='missing-header'){delete record.digest;delete record.schema;delete record.retainUntil}
 const store=new MemoryRunStore(),commit=vi.spyOn(store,'commit'),writer=new RunWriter(store,root.id);writer.bind(()=>root,recordsPolicy({}),()=>undefined)
 await expect(writer.save({writer,scope:{execution:mode==='scope'?'different-child':record.id,role:'author',task:'task-one'},root:false},record)).rejects.toBeInstanceOf(ContinuationNotRecoverable);expect(commit).not.toHaveBeenCalled()
})
