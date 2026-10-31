import {expect,it} from 'vitest'
import checkpoints from '../../contract/vectors/record-versions/checkpoint.json'
import drafts from '../../contract/vectors/dialect-authoring/retention.json'
import current from '../../contract/testdata/dialect-authoring/authoring.json'
import historical from '../../contract/testdata/dialect-authoring/superseded-private-draft-identity.json'
import rc2 from '../../contract/testdata/dialect-authoring/superseded-rc2-identity.json'
import shapes from './contract/record-shapes.json'
import {checkRecord,InvalidRecord,UnknownRecordMember} from './recordReader'

// No production record currently references dialect-draft.json. This test-only edge
// drives its generated inventory through the actual current native record walker.
function nativeDraft(value:unknown){
 const properties=shapes.current['checkpoint.json'].properties as Record<string,unknown>
 properties.dialectDraftProbe={$ref:'dialect-draft.json'}
 try{checkRecord('checkpoint',{...checkpoints.items[1],version:3,dialectDraftProbe:value})}
 finally{delete properties.dialectDraftProbe}
}
const witness=()=>structuredClone(drafts.items.at(-1)!) as unknown as {candidate:{conversionContract:{spec:Record<string,unknown>}}}

it('native generated draft inventory accepts both complete tuples and rejects every hybrid',()=>{
 const before=JSON.stringify(drafts)
 for(let mask=0;mask<16;mask++){
  const value=witness(),spec=value.candidate.conversionContract.spec
  for(const [i,key] of Object.keys(current.conversionContract.spec).entries())spec[key]=(mask&(1<<i)?historical.spec:current.conversionContract.spec)[key as keyof typeof historical.spec]
  if(mask===0||mask===15)expect(()=>nativeDraft(value),'native acceptance of exact tuple '+mask).not.toThrow()
  else expect(()=>nativeDraft(value),'native refusal of hybrid tuple '+mask).toThrow(InvalidRecord)
 }
 expect(JSON.stringify(drafts)).toBe(before)
})
it('native generated draft inventory keeps the RC2 tuple as history and never mixes it with the final (#269)',()=>{
 // The two tuples share their digests: only version and revision can mix.
 for(let mask=0;mask<4;mask++){
  const value=witness(),spec=value.candidate.conversionContract.spec
  Object.assign(spec,current.conversionContract.spec)
  if(mask&1)spec.version=rc2.spec.version
  if(mask&2)spec.revision=rc2.spec.revision
  if(mask===0||mask===3)expect(()=>nativeDraft(value),'native acceptance of exact tuple '+mask).not.toThrow()
  else expect(()=>nativeDraft(value),'native refusal of hybrid tuple '+mask).toThrow(InvalidRecord)
 }
})
it('native generated draft inventory refuses missing and unknown tuple fields',()=>{
 for(const key of Object.keys(current.conversionContract.spec)){const value=witness();delete value.candidate.conversionContract.spec[key];expect(()=>nativeDraft(value)).toThrow(InvalidRecord)}
 const value=witness();value.candidate.conversionContract.spec.extra='unsupported'
 expect(()=>nativeDraft(value)).toThrow(UnknownRecordMember)
})
