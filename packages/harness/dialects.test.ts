import {describe,expect,it} from 'vitest'
import fixture from '../../contract/testdata/dialect-authoring/authoring.json'
import inputVectors from '../../contract/vectors/dialect-authoring/inputs.json'
import {dialectFixture} from './testing/dialects'
import {dialectTools} from './dialects'
import {contextDigest} from './context/ledger'
import type {McpToolResult} from './engine'
const signal=()=>new AbortController().signal
const sha=async(s:string)=>'sha256:'+await contextDigest(s)
const body=(r:McpToolResult)=>JSON.parse(r.content![0]!.text!) as Record<string,unknown>
describe('dialect authoring shared vectors through exported tools',()=>{
 it('reads exact identities and retains one complete replacement after checks, preview and exact reread',async()=>{
  const x=dialectFixture(),read=await x.read(),result=await x.propose(read)
  expect(result.status).toBe('review-required');expect(x.state.retained).toHaveLength(1)
  expect(x.state.calls).toEqual(['read','field-index','page','read','field-index','page','draft:candidate','get_dialect_contract','dialect_validate','dialect_check','draft:checked','preview','read','retain'])
  expect(x.state.retained[0]!.documentJson).toBe(fixture.expectedDocument)
  expect(x.state.retained[0]!.fingerprint).toBe(fixture.expectedFingerprint);expect(x.state.retained[0]!.mappingDigest).toBe(fixture.expectedMappingDigest)
  expect(x.state.retained[0]!.packJson).toBe(fixture.snapshot.packJson)
  expect(x.state.previewCandidate!.columns).toEqual(fixture.snapshot.columns)
  expect(read.dialectDigest).toBe(await sha(fixture.snapshot.dialectJson))
  await x.propose(read);expect(x.state.retained).toHaveLength(2)
 })
 it('reads every public input-schema witness through the exported tools',async()=>{
  for(const row of inputVectors.items){const x=dialectFixture()
   // The read port's revision field index request and page, page request and page for the fixture inventory, and the paged read itself.
   if('fieldIndex'in row){x.ports.readFieldIndex=async()=>structuredClone(row) as never;await dialectTools(x.ports)[0]!.execute(fixture.request,signal());continue}
   if('entry'in row){await x.read();expect(x.state.fieldIndexes[0]).toEqual(row);continue}
   if('limit'in row){await x.read();expect(x.state.pages[0]).toEqual(row);continue}
   if('declarations'in row){x.ports.readDeclarations=async()=>structuredClone(row) as never;await dialectTools(x.ports)[0]!.execute(fixture.request,signal());continue}
   if('cursor'in row){await x.read();expect(body(await x.tools[0]!.execute(row,signal())).cursor).toBe(0);continue}
   if('revision'in row){if(row.dialectDigest===null)delete x.state.snapshot.dialectJson;await x.read();const result=await x.tools[1]!.execute(row as unknown as Record<string,unknown>,signal());expect(body(result).status).toBe('review-required');expect(x.state.retained).toHaveLength(1)}else await x.tools[0]!.execute(row,signal())}
 })
 it('keeps explicit omissions review-required',async()=>{const x=dialectFixture(),read=await x.read();const result=await x.propose(read,{...fixture.bindings,facts:[],evidence:[]});expect(result.coverageStatus).toBe('review-required');expect(JSON.parse(x.state.retained[0]!.documentJson)).toMatchObject({unmapped:['/amount'],unmappedEvidence:['receipt']})})
 it('requires an actual prior read even for otherwise exact identities',async()=>{const x=dialectFixture();await expect(x.propose({...fixture.request,revision:fixture.snapshot.revision,packDigest:await sha(fixture.snapshot.packJson),dialectDigest:await sha(fixture.snapshot.dialectJson)})).rejects.toThrow('read the exact');expect(x.state.retained).toHaveLength(0)})
 for(const conversion of fixture.conversions)it('carries explicit '+conversion.kind+' conversion',async()=>{
  const x=dialectFixture(),read=await x.read();await x.propose(read,{...fixture.bindings,facts:[{target:'/amount',column:conversion.kind==='research-value'?'typed':'amount/raw~value',conversion}]});expect(x.state.retained[0]!.conversions['/amount']).toEqual(conversion)
 })
 for(const refusal of fixture.refusals)it('refuses '+refusal.name,async()=>{
  const x=dialectFixture(),read=await x.read();await expect(x.propose(read,{...fixture.bindings,facts:[refusal.fact]})).rejects.toThrow();expect(x.state.retained).toHaveLength(0)
 })
 for(const mode of ['small-runtime-bound','unsupported','encoded','incomplete','wrong-document','wrong-digest','wrong-pack','wrong-inventory','malformed','wrong-preview','changed-preview'])it('fails closed on '+mode,async()=>{
  const x=dialectFixture(),read=await x.read();x.state.mode=mode;await expect(x.propose(read)).rejects.toThrow();expect(x.state.retained).toHaveLength(0)
 })
 for(const member of ['packJson','dialectJson'] as const)it('binds exact raw '+member+' even with repeated revision',async()=>{
  const x=dialectFixture(),read=await x.read();x.state.snapshot[member]+=' ';const error=await x.propose(read).then(()=>null,error=>error);expect(x.state.calls).toEqual(['read','field-index','page','read']);expect(error?.message).toContain('changed');expect(x.state.retained).toHaveLength(0)
 })
 for(const mode of ['lost-ack','wrong-ack'])it('does not retry unknown retention '+mode,async()=>{
  const x=dialectFixture(),read=await x.read();x.state.mode=mode;await expect(x.propose(read)).rejects.toMatchObject({failure:{outcome:'unknown'}});await expect(x.propose(read)).rejects.toMatchObject({failure:{outcome:'unknown'}});expect(x.state.retained).toHaveLength(1)
 })
 it('refuses no read, malformed/empty/oversized args, duplicate targets and invented original declarations',async()=>{
  const x=dialectFixture();await expect(x.propose({id:'x'})).rejects.toThrow()
  for(const args of [{},{id:'x'.repeat(513),packKey:'p'},{...fixture.request,extra:true}])await expect(x.tools[0]!.execute(args,signal())).rejects.toThrow()
  const read=await x.read();await expect(x.propose(read,{...fixture.bindings,facts:[fixture.bindings.facts[0],fixture.bindings.facts[0]]})).rejects.toThrow()
  x.state.snapshot.columns[2]!.research!.fieldJson='{}';await expect(x.read()).rejects.toThrow('actual original')
 })
 it('bounds samples visibly while retaining exact JSON, and refuses oversized snapshots',async()=>{
  const x=dialectFixture();x.state.snapshot.columns[0]!.samples[0]!.valueJson=JSON.stringify('界'.repeat(100));const read=await x.read();expect((read.columns as {samples:{truncated:boolean}[]}[])[0]!.samples[0]!.truncated).toBe(true);await x.propose(read);expect(x.state.previewCandidate!.columns[0]!.samples[0]!.valueJson).toBe(x.state.snapshot.columns[0]!.samples[0]!.valueJson)
  x.state.snapshot.columns[0]!.samples.push({valueJson:'0',origin:'asserted'});await expect(x.read()).rejects.toThrow()
 })
 for(const stage of ['get_dialect_contract','dialect_validate','dialect_check','preview','current-read','retain'])it('checks cancellation at '+stage+' acknowledgment',async()=>{
  const x=dialectFixture(),read=await x.read(),stop=new AbortController(),originalRuntime=x.ports.runtime,originalRead=x.ports.read,originalPreview=x.ports.preview,originalRetain=x.ports.retain;let reads=0
  x.ports.runtime=async(...args)=>{const r=await originalRuntime(...args);if(args[0]===stage)stop.abort();return r}
  x.ports.read=async(...args)=>{const r=await originalRead(...args);if(stage==='current-read'&&++reads===2)stop.abort();return r}
  x.ports.preview=async(...args)=>{const r=await originalPreview(...args);if(stage==='preview')stop.abort();return r}
  x.ports.retain=async(...args)=>{const r=await originalRetain(...args);if(stage==='retain')stop.abort();return r}
  const args={...Object.fromEntries(['id','revision','packKey','packDigest','dialectDigest'].map(k=>[k,read[k]])),...fixture.bindings}
  const promise=x.tools[1]!.execute(args,stop.signal)
  if(stage==='retain')await expect(promise).rejects.toMatchObject({failure:{outcome:'unknown'}});else await expect(promise).rejects.toThrow()
  expect(x.state.retained).toHaveLength(stage==='retain'?1:0)
 })
 it('cancels before each port and after preview without retention',async()=>{
  const x=dialectFixture(),read=await x.read(),abort=new AbortController();abort.abort();await expect(x.tools[1]!.execute({...read,...fixture.bindings},abort.signal)).rejects.toThrow();expect(x.state.retained).toHaveLength(0)
  const y=dialectFixture(),bound=await y.read(),stop=new AbortController(),original=y.ports.preview;y.ports.preview=async(...args)=>{const r=await original(...args);stop.abort();return r};await expect(y.tools[1]!.execute({...Object.fromEntries(['id','revision','packKey','packDigest','dialectDigest'].map(k=>[k,bound[k]])),...fixture.bindings},stop.signal)).rejects.toThrow();expect(y.state.retained).toHaveLength(0)
 })
})
