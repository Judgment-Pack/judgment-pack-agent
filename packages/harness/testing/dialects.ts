import fixture from '../../../contract/testdata/dialect-authoring/authoring.json'
import {DIALECT_AUTHORING_LIMITS,DIALECT_RETAINED_COLUMN_DRAFT,dialectFieldIndex,dialectResearchIndex,dialectTools,type DialectAuthoringPorts,type DialectSnapshot,type DialectReviewProposal,type DialectCandidate,type DialectDraftCheckpoint,type DialectDeclarationPageRequest,type DialectFieldIndexPage,type DialectFieldIndexRequest,type DialectResearchRevision} from '../dialects'
import {contextDigest} from '../context/ledger'
import {canonicalJSON} from '../context/chat'
import {utf8Bytes} from '../context/policy'
import type {McpToolResult} from '../engine'
const signal=()=>new AbortController().signal
const sha=async(s:string)=>'sha256:'+await contextDigest(s)
const body=(r:McpToolResult)=>JSON.parse(r.content![0]!.text!) as Record<string,unknown>
/** The test host's handwritten pager: the flattened inventory from the cursor, at most size per page. */
export function servePage(inventory:readonly DialectResearchRevision[]|null|undefined,request:DialectDeclarationPageRequest,size:number){
 const flat=(inventory??[]).flatMap(r=>(r.fields??[]).map(f=>({revision:r.revision,name:f.name,fieldJson:f.fieldJson})))
 const page={id:request.id,packKey:request.packKey,revision:request.revision,inventoryDigest:request.inventoryDigest,cursor:request.cursor,declarations:[] as typeof flat}
 // The host fills a page up to the declaration limit and the page byte bound.
 let total=utf8Bytes(canonicalJSON(page))
 for(let at=request.cursor;at<flat.length&&page.declarations.length<Math.min(size,request.limit);at++){
  const one=utf8Bytes(canonicalJSON(flat[at]))
  if(total+one+1>DIALECT_AUTHORING_LIMITS.pageBytes)break
  total+=one+1;page.declarations.push(flat[at]!)
 }
 return page
}
/** The test host's revision field index page: the field index of the top entry the request names. */
export async function serveFieldIndex(inventory:readonly DialectResearchRevision[]|null|undefined,request:DialectFieldIndexRequest):Promise<DialectFieldIndexPage>{
 const revision=(inventory??[])[request.entry]
 const fieldIndex=revision&&await dialectFieldIndex(revision)
 if(!fieldIndex)throw Error('no revision field index at entry '+request.entry)
 return {id:request.id,packKey:request.packKey,revision:request.revision,inventoryDigest:request.inventoryDigest,entry:request.entry,fieldIndex}
}
export function dialectFixture(){
 const {researchDeclarations,...snapshot}=structuredClone(fixture.snapshot)
 /** declarations is the host collection's complete original inventory (null: unavailable; absent: not stated). */
 const state={snapshot:snapshot as DialectSnapshot,declarations:researchDeclarations as DialectResearchRevision[]|null|undefined,pageSize:DIALECT_AUTHORING_LIMITS.pageDeclarations as number,pages:[] as DialectDeclarationPageRequest[],fieldIndexes:[] as DialectFieldIndexRequest[],drafts:[] as DialectDraftCheckpoint[],retained:[] as DialectReviewProposal[],calls:[] as string[],mode:'',previewCandidate:undefined as DialectCandidate|undefined}
 const ports:DialectAuthoringPorts={
  conversionContract:DIALECT_RETAINED_COLUMN_DRAFT,
  read:async()=>{state.calls.push('read');return {...structuredClone(state.snapshot),...(state.declarations===undefined?{}:{researchIndex:await dialectResearchIndex(state.declarations)})}},
  readFieldIndex:async(request)=>{state.calls.push('field-index');state.fieldIndexes.push(structuredClone(request));return serveFieldIndex(state.declarations,request)},
  readDeclarations:async(request)=>{state.calls.push('page');state.pages.push(structuredClone(request));return servePage(state.declarations,request,state.pageSize)},
  runtime:async(name,args)=>{
   state.calls.push(name)
   const selection=DIALECT_RETAINED_COLUMN_DRAFT,limits={documentBytes:1048576,frameBytes:2097152}
   // Fake transport with the published companion report shape; real Runtime is a separate gate.
   const common={contractVersion:'1',specification:`https://github.com/Judgment-Pack/judgment-pack-spec/blob/${selection.spec.revision}/spec/retained-column.md`,specRevision:selection.spec.revision,diagnostics:[],...selection,role:'document-validation',maturity:'draft',limits}
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
