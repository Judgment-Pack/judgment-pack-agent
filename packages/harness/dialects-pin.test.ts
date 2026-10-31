import {expect,it} from 'vitest'
import fixture from '../../contract/testdata/dialect-authoring/authoring.json'
import privateDraft from '../../contract/testdata/dialect-authoring/superseded-private-draft-identity.json'
import superseded from '../../contract/testdata/dialect-authoring/superseded-prototype-identity.json'
import rc2 from '../../contract/testdata/dialect-authoring/superseded-rc2-identity.json'
import runner from '../../contract/testdata/dialect-authoring/runner-v0.27.0-installed-conversion-spec.json'
import {DIALECT_RETAINED_COLUMN_DRAFT} from './dialects'
import {dialectFixture} from './testing/dialects'

it('selects the final Spec 0.3.0 tuple Runner v0.27.0 installs, byte for byte (#269)',()=>{
 expect(runner.source).toMatchObject({repository:'Judgment-Pack/judgment-pack-runner',tag:'v0.27.0',commit:'1164d78cb401f169e63623700853db5c6e635848',path:'internal/runner/column_role_v1.go',symbol:'installedConversionSpec'})
 expect(JSON.stringify(DIALECT_RETAINED_COLUMN_DRAFT.spec)).toBe(runner.json)
 expect(DIALECT_RETAINED_COLUMN_DRAFT.contract).toBe('retained-column/1');expect(DIALECT_RETAINED_COLUMN_DRAFT.profile).toBe('base')
 // The RC2 tuple is the same leaf under its candidate identity, and no longer the selector.
 expect(rc2.spec.schemaSha256).toBe(DIALECT_RETAINED_COLUMN_DRAFT.spec.schemaSha256);expect(rc2.spec.corpusSha256).toBe(DIALECT_RETAINED_COLUMN_DRAFT.spec.corpusSha256)
 expect(JSON.stringify(rc2.spec)).not.toBe(runner.json)
})

it('uses the published final tuple and refuses the superseded prototype before Runtime',async()=>{
 expect(DIALECT_RETAINED_COLUMN_DRAFT).toEqual(fixture.conversionContract)
 const x=dialectFixture();x.ports.conversionContract=superseded
 await expect(x.propose(await x.read())).rejects.toThrow('exact package-supported retained-column opt-in')
 expect(x.state.calls.includes('get_dialect_contract')).toBe(false)
 expect(x.state.calls.includes('preview')).toBe(false)
 expect(x.state.drafts.length).toBeGreaterThan(0);expect(x.state.retained).toHaveLength(0)
})

for(const stale of [superseded,privateDraft,rc2])it('requires current trusted opt-in even for an inspectable historical identity '+stale.spec.revision,async()=>{
 const x=dialectFixture();x.ports.conversionContract=stale
 const identity=await dialectFixture().read()
 await expect(x.propose(identity)).rejects.toThrow('read the exact')
 await expect(x.propose(await x.read())).rejects.toThrow('exact package-supported retained-column opt-in')
 expect(x.state.calls.includes('get_dialect_contract')).toBe(false)
 expect(x.state.calls.includes('preview')).toBe(false);expect(x.state.retained).toHaveLength(0)
 x.ports.conversionContract=DIALECT_RETAINED_COLUMN_DRAFT
 await x.propose(await x.read())
 expect(x.state.retained[0]!.conversionContract).toEqual(fixture.conversionContract)
 expect(stale).not.toEqual(fixture.conversionContract)
})
for(const operation of ['get_dialect_contract','dialect_validate','dialect_check'] as const)for(const member of ['version','revision','schemaSha256','corpusSha256','specRevision','specification','maturity','role','profile'])it('refuses published identity drift at '+operation+': '+member,async()=>{
 const x=dialectFixture(),original=x.ports.runtime
 x.ports.runtime=async(name,args,signal)=>{
  expect(args.draftContract).toBe('retained-column/1@27cb29bf13e4a2d58d69fe19c7ebfeb762b014fc');expect(args.draftProfile).toBe('base')
  const result=await original(name,args,signal)
  if(name!==operation)return result
  const report=JSON.parse(result.content![0]!.text!)
  if(Object.hasOwn(fixture.conversionContract.spec,member))report.spec[member]=privateDraft.spec[member as keyof typeof privateDraft.spec]
  else if(member==='specRevision')report.specRevision=privateDraft.spec.revision
  else if(member==='specification')report.specification='https://github.com/Judgment-Pack/judgment-pack-spec/blob/'+fixture.conversionContract.spec.revision+'/rfcs/0029-retained-column-conversions.md'
  else report[member]=member==='maturity'?'experimental-draft':member==='role'?'preparation':'extended'
  return {content:[{type:'text',text:JSON.stringify(report)}]}
 }
 await expect(x.propose(await x.read())).rejects.toThrow()
 expect(x.state.calls.includes('preview')).toBe(false);expect(x.state.retained).toHaveLength(0)
 expect(x.state.drafts[0]!.candidate.conversionContract).toEqual(fixture.conversionContract)
 if(operation!=='get_dialect_contract'){expect(x.state.drafts.at(-1)!.check!.failure!.code).toBe('check-refused');expect(x.state.drafts.at(-1)!.check!.results).toHaveLength(operation==='dialect_check'?2:1)}
})
