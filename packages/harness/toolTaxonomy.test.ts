import {describe,it,expect} from 'vitest'
import vectors from '../../contract/testdata/taxonomy/tools.json'
import profiles from '../../contract/testdata/taxonomy/profiles.json'
import declarations from '../../contract/vectors/taxonomy/declarations.json'
import {taxonomyProblem,runtimeTaxonomyProblem,taxonomyFits,presetLimit,capabilityProfile,profileFits,defaultApproval,taxonomyAnnotations,type ToolTaxonomy,type TaxonomyEffect,type TaxonomyLimit} from './toolTaxonomy'
import {declareSources,toolListDigest,SourceGate} from './toolSources'
import {declareSkills,skillDigest,catalogueSkills,type SkillDeclaration} from './skills'
import {CONTEXT_DEFAULTS} from './context/policy'
import {contextualSession} from './context/session'
import {failureEvent,AIFailureError} from './failures'
import {readRunEvent} from './recordReader'
import type {EngineSession} from './engine'
import {GateViolation} from './toolGate'
import type {SourceFigures} from './execution/state'
import type {HostTool} from './engine'

const taxonomyMap=new Map(Object.entries(profiles.tools) as [string,ToolTaxonomy][])
const skill=(tools:string[]=[],kind?:SkillDeclaration['kind']):SkillDeclaration=>({id:'notes',name:'notes',description:'Review notes',instructions:'Read carefully.',source:{kind:'custom',id:'library',trust:'person',version:'1'},tools,kind,pinnedDigest:'sha256:'+'0'.repeat(64)})
const local=():HostTool=>({name:'read_notes',taxonomy:{operation:'read',effect:'read'},description:'Read notes',inputSchema:{type:'object'},execute:async()=>({content:[]})})

describe('ADR0027 taxonomy foundation',()=>{
 it('reads every shared operation, effect, subkind and experimental flag',()=>{
  for(const row of vectors.valid){const t=row.taxonomy as ToolTaxonomy;expect(taxonomyProblem(t,'runtime' in row&&row.runtime)).toBe('');expect(defaultApproval(t.effect)).toBe(row.approval);expect(taxonomyFits(t,presetLimit('review'))).toBe(row.review);expect(taxonomyFits(t,presetLimit('work'))).toBe(row.work)}
  for(const t of vectors.invalid)expect(taxonomyProblem(t)).not.toBe('')
  for(const row of vectors.limits)expect(taxonomyFits(row.taxonomy as ToolTaxonomy,row.limit as TaxonomyLimit)).toBe(row.allowed)
  for(const row of vectors.hints)expect(taxonomyAnnotations(row.effect as TaxonomyEffect)).toEqual(row.annotations)
 })
 it('refuses unsupported Runtime metadata before work and emits readable bounded events',async()=>{
  expect(runtimeTaxonomyProblem({operation:'act',effect:'act'},'get_schema')).toBe('runtime taxonomy exceeds its local computation contract')
  expect(runtimeTaxonomyProblem({operation:'evaluate',effect:'act',experimental:true},'experimental_evaluate')).toBe('')
  expect(runtimeTaxonomyProblem({operation:'read',effect:'read'},'experimental_evaluate')).not.toBe('')
  for(const metadata of [undefined,{}, {operation:'x'.repeat(10000),effect:'read'}]){
   try {await contextualSession({tools:[{name:'get_schema',_meta:metadata}],hostTools:[]} as unknown as EngineSession);throw Error('missing refusal')}
   catch(error){expect(error).toBeInstanceOf(AIFailureError);expect(()=>readRunEvent(failureEvent((error as AIFailureError).failure))).not.toThrow()}
  }
 })
 it('derives profiles and preset filtering from admitted dependencies',()=>{
  for(const row of profiles.profiles){const p=capabilityProfile(row.tools,taxonomyMap);expect(p).toEqual(row.profile);expect(profileFits(p,presetLimit('review'))).toBe(row.review);expect(profileFits(p,presetLimit('work'))).toBe(row.work)}
  expect(()=>capabilityProfile(profiles.unknown,taxonomyMap)).toThrow('unadmitted')
  expect(taxonomyFits({operation:'ask',effect:'pure'},{operations:[],effects:[]})).toBe(true)
 })
 it('profiles only offered Runtime references, withholding withdrawn dependencies',async()=>{
  const metadata:ToolTaxonomy={operation:'check',effect:'pure',checkKind:'validate'}
  expect(runtimeTaxonomyProblem(metadata,'validate')).toBe('')
  const tools=new Map([['validate',metadata]])
  const s=skill(['validate'],'method');s.pinnedDigest=await skillDigest(s,tools)
  expect((await declareSkills([s],['validate'],CONTEXT_DEFAULTS,[],tools)).skills[0]!.profile).toEqual({effect:'pure',operations:['check']})
  expect((await declareSkills([s],[],CONTEXT_DEFAULTS,[],tools)).withheld).toHaveLength(1)
  expect((await declareSkills([s],['validate'],CONTEXT_DEFAULTS,[],new Map())).withheld).toHaveLength(1)
 })
 it('refuses missing taxonomy, contradictory projections, owner spoofing and widening through admission',async()=>{
  await expect(declareSources([{...local(),taxonomy:undefined}],[])).rejects.toThrow('mandatory tool taxonomy')
  await expect(declareSources([{...local(),effectClass:'act'}],[])).rejects.toThrow('contradicts')
  await expect(declareSources([{...local(),taxonomy:{operation:'evaluate',effect:'pure'}}],[])).rejects.toThrow('only the gated runtime')
  const act:HostTool={...local(),taxonomy:{operation:'act',effect:'act'},approval:'none',source:{kind:'custom',id:'notes',trust:'first-party',version:'1'}}
  act.source!.toolListDigest=await toolListDigest([act])
  await expect(declareSources([act],[])).rejects.toThrow('cannot widen')
  const out=await declareSources([{...local(),taxonomyLimit:{operations:['check']}}],[]);expect(out.hostTools).toHaveLength(0);expect(out.withheld).toHaveLength(1)
 })
 it('binds kind, bounded untrusted tags and dependency taxonomy to approval',async()=>{
  for(const kind of profiles.kinds){const s=skill([],kind as SkillDeclaration['kind']);s.tags=profiles.tags;s.pinnedDigest=await skillDigest(s,taxonomyMap);const out=await declareSkills([s],[],CONTEXT_DEFAULTS,[],taxonomyMap);expect(out.skills[0]!.kind).toBe(kind);expect(out.skills[0]!.profile.effect).toBe('pure')}
  const tagged=skill([],'method');tagged.tags=['finance'];tagged.pinnedDigest=await skillDigest(tagged)
  for(const changed of [{...tagged,kind:'review'},{...tagged,tags:['legal']}])expect((await declareSkills([changed],[],CONTEXT_DEFAULTS)).withheld).toHaveLength(1)
  const s=skill(['read_notes'],'procedure');s.pinnedDigest=await skillDigest(s,taxonomyMap)
  const admitted=await declareSkills([s],['read_notes'],CONTEXT_DEFAULTS,[],taxonomyMap);expect(admitted.skills).toHaveLength(1)
  expect(catalogueSkills(admitted.skills,CONTEXT_DEFAULTS,'marker','review').section).toContain('reads external data')
  const changed=new Map(taxonomyMap);changed.set('read_notes',{operation:'act',effect:'act'})
  expect((await declareSkills([s],['read_notes'],CONTEXT_DEFAULTS,[],changed)).withheld).toHaveLength(1)
  const acting=skill(['send_notes'],'procedure');acting.pinnedDigest=await skillDigest(acting,taxonomyMap)
  const acts=await declareSkills([acting],['send_notes'],CONTEXT_DEFAULTS,[],taxonomyMap)
  expect(catalogueSkills(acts.skills,CONTEXT_DEFAULTS,'marker','review').catalogued).toHaveLength(0)
  expect(catalogueSkills(acts.skills,CONTEXT_DEFAULTS,'marker','work').section).toContain('acts; needs approval')
  expect((await declareSkills([s],[],CONTEXT_DEFAULTS,[],taxonomyMap)).withheld).toHaveLength(1)
  for(const invalid of [{kind:'model-choice'},{tags:Array(9).fill('x')},{tags:['x'.repeat(33)]},{profile:{effect:'pure',operations:[]}}])await expect(declareSkills([{...skill(),...invalid}],[],CONTEXT_DEFAULTS)).rejects.toThrow('declaration')
 })
 it('binds external receipts to the exact real call and withholds mismatched results',async()=>{
  const t:HostTool={...local(),taxonomy:{operation:'read',effect:'read-external'},source:{kind:'gateway',id:'notes',trust:'connected',version:'1',verifyReceipt:e=>e.receipt==='signed-call-1'&&e.tool==='read_notes'&&e.args==='{"id":"one"}'&&e.result==='{"content":[{"text":"one","type":"text"}]}'},execute:async args=>({receipt:'signed-call-1',content:[{type:'text',text:String(args.id)}]})}
  t.source!.toolListDigest=await toolListDigest([t])
  const admitted=await declareSources([t],[]);expect(admitted.hostTools).toHaveLength(1)
  const rows:SourceFigures[]=[]
  const gate=new SourceGate(admitted.entries,admitted.sources,CONTEXT_DEFAULTS,'marker',(name,detail)=>new GateViolation(name,detail),()=>rows)
  const offered=admitted.hostTools[0]!,signal=new AbortController().signal
  expect(await gate.call(offered,{id:'one'},signal,offered.execute)).toEqual({content:[{type:'text',text:'one'}]})
  const mismatch=gate.call(offered,{id:'two'},signal,offered.execute)
  await expect(mismatch).rejects.toThrow('receipt verification failed')
  await expect(mismatch).rejects.toMatchObject({failure:{code:'tool-failed',outcome:'unknown'}})
  await expect(gate.call(offered,{id:'one'},signal,async()=>({receipt:'signed-call-1',content:[{type:'text',text:'tampered'}]}))).rejects.toThrow('receipt verification failed')
  expect(rows.find(row=>row.source==='notes')).toMatchObject({calls:3,refused:2})
  await expect(declareSources([{...t,source:{...t.source!,verifyReceipt:undefined}}],[])).rejects.toThrow('requires a host receipt verifier')
  const changed=await declareSources([{...t,taxonomy:{operation:'answer',effect:'read-external'}}],[]);expect(changed.hostTools).toHaveLength(0)
 })
 it('admits rehearse only as a pure or read check whose description opens non-binding (#241)',async()=>{
  const refusal=async(tool:HostTool,problem:string)=>{
   try {await declareSources([tool],[]);throw Error('missing refusal')}
   catch(error){
    expect(error).toBeInstanceOf(AIFailureError)
    expect((error as AIFailureError).failure.code).toBe('configuration')
    expect((error as Error).message).toBe(`Tool taxonomy refused: ${problem}. Nothing was sent.`)
    expect(()=>readRunEvent(failureEvent((error as AIFailureError).failure))).not.toThrow()
   }
  }
  for(const t of declarations.items)expect(taxonomyProblem(t,true)).toBe('')
  expect(declarations.items.filter(t=>'checkKind' in t&&t.checkKind==='rehearse')).toHaveLength(2)
  expect(declarations.refused).toHaveLength(4)
  for(const row of declarations.refused){
   expect(taxonomyProblem(row.taxonomy)).toBe(row.problem)
   await refusal({...local(),description:'Rehearsal, non-binding: read notes',taxonomy:row.taxonomy as ToolTaxonomy},row.problem)
  }
  for(const row of vectors.rehearsal.admitted){
   const out=await declareSources([{...row.declaration,execute:async()=>({content:[]})} as HostTool],[])
   expect(out.hostTools).toHaveLength(1)
   const admitted=out.hostTools[0]!
   expect(admitted.annotations).toEqual(row.annotations)
   expect(defaultApproval(admitted.taxonomy!.effect)).toBe(row.approval)
   expect(taxonomyFits(admitted.taxonomy!,presetLimit('review'))).toBe(row.review)
   expect(taxonomyFits(admitted.taxonomy!,presetLimit('work'))).toBe(row.work)
  }
  expect(vectors.rehearsal.refused).toHaveLength(4)
  for(const row of vectors.rehearsal.refused)await refusal({...row.declaration,execute:async()=>({content:[]})} as HostTool,row.problem)
 })
})
