import fixture from '../../../contract/testdata/dialect-authoring/authoring.json'
import {DIALECT_RETAINED_COLUMN_DRAFT,dialectTools,type DialectAuthoringPorts,type DialectSnapshot,type DialectReviewProposal,type DialectCandidate,type DialectDraftCheckpoint} from '../dialects'
import {contextDigest} from '../context/ledger'
import type {McpToolResult} from '../engine'
const signal=()=>new AbortController().signal
const sha=async(s:string)=>'sha256:'+await contextDigest(s)
const body=(r:McpToolResult)=>JSON.parse(r.content![0]!.text!) as Record<string,unknown>
export function dialectFixture(){
 const state={snapshot:structuredClone(fixture.snapshot) as DialectSnapshot,drafts:[] as DialectDraftCheckpoint[],retained:[] as DialectReviewProposal[],calls:[] as string[],mode:'',previewCandidate:undefined as DialectCandidate|undefined}
 const ports:DialectAuthoringPorts={
  conversionContract:DIALECT_RETAINED_COLUMN_DRAFT,
  read:async()=>{state.calls.push('read');return structuredClone(state.snapshot)},
  runtime:async(name,args)=>{
   state.calls.push(name)
   const selection=DIALECT_RETAINED_COLUMN_DRAFT,limits={documentBytes:131072,frameBytes:2097152}
   // Fake transport with the actual private Draft report shape; real Runtime is a separate gate.
   const common={contractVersion:'1',specification:`https://github.com/Judgment-Pack/judgment-pack-spec/blob/${selection.spec.revision}/rfcs/0029-retained-column-conversions.md`,specRevision:selection.spec.revision,diagnostics:[],...selection,role:'document-validation',maturity:'experimental-draft',limits}
   const mapping=JSON.parse(args.document??'{}') as {unmapped?:unknown[];unmappedEvidence?:unknown[]}
   const coverageStatus=state.mode==='incomplete'?'incomplete':(mapping.unmapped?.length||mapping.unmappedEvidence?.length)?'review-required':'complete'
   let r:Record<string,unknown>
   if(name==='get_dialect_contract')r={...common,status:'available',capabilities:{conversionContracts:[{...selection,role:'document-validation',limits}],packCheckScope:'declared-inventory-coverage',operations:['validate','check'],maxPackBytes:10485760}}
   else r={...common,status:name==='dialect_validate'?'document-valid':coverageStatus,dialectVersion:2,validationScope:name==='dialect_validate'?'experimental-retained-column-base-document':'experimental-retained-column-base-document-and-declared-pack-inventory',document:args.document,digest:await sha(args.document!),...(name==='dialect_check'?{compatibility:{status:coverageStatus,scope:'declared-inventory-coverage',pack:{sha256:(await sha(args.pack!)).slice(7)},valueTypesChecked:false,executionChecked:false,facts:[{target:'/amount'}],evidence:[{target:'receipt'}]}}:{})}
   if(state.mode==='small-runtime-bound'&&name==='get_dialect_contract')limits.documentBytes=1
   if(state.mode==='unsupported'&&name==='get_dialect_contract')(r.capabilities as Record<string,unknown>).conversionContracts=[]
   if(state.mode==='encoded'&&name==='dialect_validate')r.status='encoded'
   if(state.mode==='wrong-document'&&name!=='get_dialect_contract'){r.document='{}';r.digest=await sha('{}')}
   if(state.mode==='wrong-digest'&&name==='dialect_validate')r.digest='sha256:'+'0'.repeat(64)
   if(state.mode==='wrong-pack'&&name==='dialect_check')((r.compatibility as Record<string,unknown>).pack as Record<string,unknown>).sha256='0'.repeat(64)
   if(state.mode==='wrong-inventory'&&name==='dialect_check')(r.compatibility as Record<string,unknown>).facts=[]
   if(state.mode==='transport-failed'&&name==='dialect_check')throw Error('Runtime unavailable')
   if(state.mode==='oversized-report'&&name==='dialect_check')return {content:[{type:'text',text:'x'.repeat(4194305)}]}
   if(state.mode==='failed-validation'&&name==='dialect_validate')r.status='refused'
   if(state.mode==='malformed-check'&&name==='dialect_check')return {content:[{type:'text',text:'{'}]}
   if(state.mode==='malformed')return {content:[{type:'text',text:'{'}]}
   return {content:[{type:'text',text:JSON.stringify(r)}]}
  },
  preview:async(candidate,fingerprint)=>{state.calls.push('preview');state.previewCandidate=structuredClone(candidate);if(state.mode==='preview-failed')throw Error('Preview failed');if(state.mode==='changed-preview')state.snapshot.packJson+=' ';return {fingerprint:state.mode==='wrong-preview'?'wrong':fingerprint,recordCount:state.snapshot.recordCount,recordsWithIssues:0,issues:[]}},
  retainDraft:async(checkpoint)=>{state.calls.push('draft:'+checkpoint.stage);state.drafts.push(structuredClone(checkpoint));return {fingerprint:checkpoint.fingerprint,stage:checkpoint.stage,digest:checkpoint.digest,previousReceipt:checkpoint.previousReceipt,receipt:'draft:'+checkpoint.stage,status:'draft'}},
  retain:async(proposal)=>{state.calls.push('retain');state.retained.push(structuredClone(proposal));if(state.mode==='lost-ack')throw Error('unavailable');return {fingerprint:state.mode==='wrong-ack'?'wrong':proposal.fingerprint,receipt:'review:one',status:'review-required',draft:structuredClone(proposal.draft)}}
 }
 const tools=dialectTools(ports)
 const read=async()=>body(await tools[0]!.execute(fixture.request,signal()))
 const propose=async(identity:Record<string,unknown>,bindings:unknown=fixture.bindings)=>body(await tools[1]!.execute({...Object.fromEntries(['id','revision','packKey','packDigest','dialectDigest'].map(k=>[k,identity[k]])),...(bindings as Record<string,unknown>)},signal()))
 return {state,ports,tools,read,propose}
}
