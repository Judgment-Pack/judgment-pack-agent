import {expect,it} from 'vitest'
import fixture from '../../contract/testdata/dialect-authoring/authoring.json'
import retained from '../../contract/testdata/dialect-authoring/retained-policy.json'
import {dialectFixture} from './testing/dialects'
import {dialectResearchIndex,readDialectDeclarations} from './dialects'
import {canonicalJSON} from './context/chat'
import {contextDigest} from './context/ledger'

it('retains every original revision/type and emits executable declared leaves for all five kinds',async()=>{
 for(const conversion of fixture.conversions){
  const x=dialectFixture();x.state.declarations=structuredClone(retained.researchDeclarations)
  const read=await x.read();await x.propose(read,{...fixture.bindings,facts:[{target:'/amount',column:'typed',conversion}]})
  const candidate=x.state.retained[0]!,mapping=JSON.parse(candidate.documentJson)
  expect(mapping.case.facts).toEqual([{target:'/amount',source:'/retained',conversion:{contractVersion:'1',column:'typed',...conversion}}])
  expect(mapping.case.evidence).toEqual([{requirement:'receipt',source:'/retained',availability:{contractVersion:'1',column:'availability'}}])
  const index=await dialectResearchIndex(retained.researchDeclarations)
  expect(candidate.researchIndex).toEqual(index);expect(candidate.version).toBe(2);expect(Object.hasOwn(candidate,'researchDeclarations')).toBe(false)
  expect(x.state.drafts.every(d=>d.version===2&&canonicalJSON(d.candidate.researchIndex)===canonicalJSON(index))).toBe(true)
  expect(await readDialectDeclarations(x.ports.readDeclarations!,candidate,candidate.researchIndex,new AbortController().signal)).toEqual(retained.researchDeclarations)
  expect(candidate.columns).toEqual(fixture.snapshot.columns)
 }
})
it('keeps unavailable inventories explicit and never fabricates one from single-column metadata',async()=>{
 for(const inventory of [undefined,null,[],[{revision:'original:missing',fields:null}]]){
  const x=dialectFixture();x.state.declarations=inventory
  const read=await x.read();await x.propose(read,{...fixture.bindings,facts:[{target:'/amount',column:'typed',conversion:{kind:'research-value'}}]})
  expect(x.state.retained[0]!.researchIndex).toEqual(await dialectResearchIndex(inventory))
 }
})
for(const issue of retained.previewIssues)it('preserves review proposal versus actual-row preview issue '+issue.issue,async()=>{
 const x=dialectFixture();x.state.snapshot.columns.push({name:'name',samples:[]});x.state.declarations=null
 // Host preview seam, with a handwritten per-record expectation; actual Runner is a separate gate.
 x.ports.preview=async(_candidate,fingerprint)=>({fingerprint,recordCount:1,recordsWithIssues:1,issues:[{recordId:'actual-row:one',issues:[issue.issue]}]})
 await x.propose(await x.read(),{...fixture.bindings,facts:[{target:'/amount',column:issue.column,conversion:{kind:'research-value'}}]})
 expect(x.state.retained[0]!.preview.issues).toEqual([{recordId:'actual-row:one',issues:[issue.issue]}])
 expect(x.state.retained[0]!.researchIndex).toBeNull()
})
it('retains a planned research-value proposal with zero rows without inferring a declaration or readiness',async()=>{
 const x=dialectFixture();x.state.snapshot.recordCount=0;x.state.snapshot.columns.forEach(c=>{c.samples=[];delete c.research});delete x.state.declarations
 await x.propose(await x.read(),{...fixture.bindings,facts:[{target:'/amount',column:'missing',conversion:{kind:'research-value'}}]})
 expect(x.state.retained[0]!.preview).toMatchObject({recordCount:0,recordsWithIssues:0,issues:[]})
 expect(x.state.retained[0]!.researchIndex).toBeNull();expect(x.state.retained[0]!.status).toBe('review-required')
})
for(const drift of ['missing-opt-in','foreign-opt-in','old-alpha','discovery-spec','checked-spec','checked-role','checked-profile','checked-limits','missing-operation','malformed-limit'])it('refuses exact conversion capability drift '+drift,async()=>{
 const x=dialectFixture(),original=x.ports.runtime
 if(drift==='missing-opt-in')delete x.ports.conversionContract
 if(drift==='foreign-opt-in')x.ports.conversionContract={...fixture.conversionContract,spec:{...fixture.conversionContract.spec,revision:'a'.repeat(40)}}
 x.ports.runtime=async(name,args,signal)=>{
  const result=await original(name,args,signal),r=JSON.parse(result.content![0]!.text!)
  if(name==='get_dialect_contract'){
   if(drift==='old-alpha'){delete r.contract;delete r.spec;r.capabilities={validationVersions:[2],packCheckVersions:[2]}}
   if(drift==='discovery-spec')r.spec.schemaSha256='sha256:'+'0'.repeat(64)
   if(drift==='missing-operation')r.capabilities.operations=['check']
   if(drift==='malformed-limit'){r.limits.documentBytes='1048576';r.capabilities.conversionContracts[0].limits.documentBytes='1048576'}
  }else if(name==='dialect_validate'){
   if(drift==='checked-spec')r.spec.corpusSha256='sha256:'+'0'.repeat(64)
   if(drift==='checked-role')r.role='preparation'
   if(drift==='checked-profile')r.profile='extended'
   if(drift==='checked-limits')r.limits.frameBytes++
  }
  return {content:[{type:'text',text:JSON.stringify(r)}]}
 }
 await expect(x.propose(await x.read())).rejects.toThrow()
 expect(x.state.drafts.length).toBeGreaterThan(0);expect(x.state.retained).toHaveLength(0);expect(x.state.calls.includes('preview')).toBe(false)
})
for(const raw of ['{"name":"typed","type":"text","name":"typed"}','{"name":"wrong","type":"text"}','{"name":"typed","type":null}','{"name":"typed","type":"text","data":"\\ud800"}'])it('refuses malformed original declaration without replacing exact numeric text '+raw,async()=>{
 const x=dialectFixture();x.state.declarations=[{revision:'original',fields:[{name:'typed',fieldJson:raw}]}]
 await expect(x.read()).rejects.toThrow()
})
it('resumes retained original checkpoint data through a fresh factory and requires a fresh read',async()=>{
 const x=dialectFixture();x.state.declarations=structuredClone(retained.researchDeclarations)
 await x.propose(await x.read());const checkpoint=structuredClone(x.state.drafts[0]!)
 const {digest,...content}=checkpoint;expect(digest).toBe('sha256:'+await contextDigest(canonicalJSON(content)))
 const y=dialectFixture();y.state.declarations=structuredClone(retained.researchDeclarations)
 await expect(y.propose({...fixture.request,...checkpoint.candidate})).rejects.toThrow('read the exact')
 await y.propose(await y.read());expect(y.state.retained[0]!.researchIndex).toEqual(checkpoint.candidate.researchIndex)
})
it('edits only the trusted selected generated occurrence and preserves source order, profile, arguments and unwrap',async()=>{
 const {default:template}=await import('../../contract/testdata/dialect-authoring/generated-read.json')
 const x=dialectFixture();x.state.snapshot.retainedRead={...template,occurrence:{kind:'source',name:'retained-generated'}}
 await x.propose(await x.read())
 const candidate=x.state.retained[0]!,actual=JSON.parse(candidate.documentJson),before=JSON.parse(template.documentJson)
 expect(candidate.retainedRead).toEqual(template)
 expect(actual.case).toEqual({facts:[],evidence:[]})
 expect(actual.sources.map((s:{name:string})=>s.name)).toEqual(['before','retained-generated','after'])
 const selected=actual.sources[1];expect(selected.read.copy).toEqual(JSON.parse(fixture.expectedDocument).case)
 before.sources[1].read.copy=selected.read.copy
 expect(actual.sources).toEqual(before.sources)
 expect(candidate.columns[2]!.samples.every(s=>s.origin==='generated')).toBe(true)
})
it('preserves default proposal policy for imported, generated and mixed samples without asserting preparation readiness',async()=>{
 for(const origins of [['asserted'],['generated'],['asserted','generated']]){
  const x=dialectFixture();x.state.snapshot.columns[0]!.samples=origins.map(origin=>({origin,valueJson:'1.2300'}))
  await x.propose(await x.read());const candidate=x.state.retained[0]!
  expect(candidate.columns[0]!.samples.map(s=>s.origin)).toEqual(origins)
  expect(candidate.retainedRead).toBeNull();expect(candidate.validation.role).toBe('document-validation')
  expect((candidate.coverage.compatibility as Record<string,unknown>).executionChecked).toBe(false)
 }
})
it('applies a discovered byte ceiling before sending a document to validation',async()=>{
 const x=dialectFixture();x.state.mode='small-runtime-bound';await expect(x.propose(await x.read())).rejects.toThrow('byte ceiling')
 expect(x.state.calls.includes('dialect_validate')).toBe(false)
})
it('refuses ambiguous trusted source occurrences before falling back to a case mapping',async()=>{
 const {default:template}=await import('../../contract/testdata/dialect-authoring/generated-read.json')
 const x=dialectFixture(),mapping=JSON.parse(template.documentJson);mapping.sources.push(structuredClone(mapping.sources[1]))
 x.state.snapshot.retainedRead={...template,documentJson:JSON.stringify(mapping),occurrence:{kind:'source',name:'retained-generated'}}
 await expect(x.propose(await x.read())).rejects.toThrow('one existing copy read')
 expect(x.state.retained).toHaveLength(0)
})
