import type {HostTool, McpToolResult} from './engine'
import {AIFailureError, toolInputFailure} from './failures'
import {canonicalJSON} from './context/chat'
import {contextDigest} from './context/ledger'
import {prefix, utf8Bytes} from './context/policy'
import {readDescriptorJSON} from './descriptorSnapshot'
import {toolTimeout, withDeadline} from './engines/contract'

/** Package-owned draft/review tools. Ports cannot save a recipe, publish or execute it. */
export const DIALECT_AUTHORING_LIMITS = Object.freeze({documentBytes:1048576, packBytes:1048576, snapshotBytes:4194304, resultBytes:2097152, runtimeBytes:4194304, candidateBytes:4194304, draftBytes:8388608, runtimeResults:2, columns:1000, bindings:1000, samples:3, sampleBytes:16384, shownSampleBytes:160, rememberedReads:32, milliseconds:10000})
/** Private experimental Draft document role at one reviewed development identity; no normative adoption. */
export const DIALECT_RETAINED_COLUMN_DRAFT = Object.freeze({
 contract:'retained-column/1', profile:'base',
 spec:Object.freeze({version:'Draft RFC0029',revision:'ade3914b0b81ef299e5b9a8d6147f37ff3f0976c',schemaSha256:'sha256:996cec17473987eebf1aa553536df0ce5a740971532567f411282ee70036879f',corpusSha256:'sha256:e2ac53f56cd5ab779e032d2847b49fa04438ffb9df57d456f75f2ba058dc12d2'})
})
export interface DialectConversionContract {contract:string; profile:string; spec:{version:string; revision:string; schemaSha256:string; corpusSha256:string}}
export interface DialectRetainedRead {documentJson:string; occurrence:{kind:'case'}|{kind:'source';name:string}; framePointer:string}
export interface DialectResearchRevision {revision:string; fields:{name:string; fieldJson:string}[]|null}
export interface DialectIdentity {id:string; revision:string; packKey:string; packDigest:string; dialectDigest:string|null}
export interface DialectConversion {kind:'text'|'decimal'|'number'|'boolean'|'research-value'; trueValue?:string; falseValue?:string}
export interface DialectFactBinding {target:string; column:string; conversion:DialectConversion}
export interface DialectEvidenceBinding {requirement:string; column:string}
export interface DialectSample {valueJson:string; origin:string}
export interface DialectColumn {
 name:string
 samples:DialectSample[]
 /** Original retained field declaration, never a current recipe's requested type. */
 research?:{revision:string; fieldJson:string}
}
export interface DialectReadRequest {id:string; packKey:string}
export interface DialectSnapshot extends DialectReadRequest {
 revision:string; name:string; recordCount:number; packJson:string; dialectJson?:string
 columns:DialectColumn[]; factTargets:string[]; evidenceRequirements:string[]
 conversions:Record<string,DialectConversion>
 /** Complete retained original revision inventory; absent/null means unavailable, never inferred from a sample. */
 researchDeclarations?:DialectResearchRevision[]|null
 /** Trusted source template/selected occurrence; no acquisition credentials are constructed. */
 retainedRead?:DialectRetainedRead
}
export interface DialectPreview {
 fingerprint:string; recordCount:number; recordsWithIssues:number
 issues:{recordId:string; issues:string[]}[]
}
export interface DialectCandidate extends DialectIdentity {
 documentJson:string; packJson:string; conversions:Record<string,DialectConversion>
 facts:DialectFactBinding[]; evidence:DialectEvidenceBinding[]; summary:string
 /** Original declarations and exact bounded sample JSON; not acquired or type-verified data. */
 columns:DialectColumn[]
 researchDeclarations:DialectResearchRevision[]|null
 retainedRead:DialectRetainedRead|null
 conversionContract:DialectConversionContract
}
/** Retained drafts remain inspectable/editable even when checking fails; never save authority. */
export interface DialectDraftCheck {
 results:{operation:'dialect_validate'|'dialect_check'; resultJson:string}[]
 failure?:{code:'report-unavailable'|'check-refused'; message:string}
}
export interface DialectDraftCheckpoint {
 version:1; attemptId:string; fingerprint:string; stage:'candidate'|'checked'; digest:string; previousReceipt:string|null
 candidate:DialectCandidate; check?:DialectDraftCheck
}
export interface DialectDraftRetention {
 fingerprint:string; stage:'candidate'|'checked'; digest:string; previousReceipt:string|null; receipt:string; status:'draft'
}
export interface DialectDraftLineage {attemptId:string; fingerprint:string; candidateReceipt:string; checkedReceipt:string; checkedDigest:string}
export interface DialectReviewProposal extends DialectCandidate {
 draft:DialectDraftLineage
 version:1; fingerprint:string; mappingDigest:string; status:'review-required'
 validation:Record<string,unknown>; coverage:Record<string,unknown>; preview:DialectPreview
}
export interface DialectRetention {fingerprint:string; receipt:string; status:'review-required'; draft:DialectDraftLineage}
export interface DialectAuthoringPorts {
 /** Trusted host opt-in to the package's exact private Draft; never a model argument. */
 conversionContract?:DialectConversionContract
 read:(request:Readonly<DialectReadRequest>,signal:AbortSignal)=>Promise<DialectSnapshot>
 /** Actual installed Runtime transport. The package calls only discovery, validate and check. */
 runtime:(name:'get_dialect_contract'|'dialect_validate'|'dialect_check',args:Readonly<Record<string,string>>,signal:AbortSignal)=>Promise<McpToolResult>
 preview:(candidate:Readonly<DialectCandidate>,fingerprint:string,signal:AbortSignal)=>Promise<DialectPreview>
 /** Upsert this exact draft for inspection/review/editing; no validation, save or approval authority. */
 retainDraft:(checkpoint:Readonly<DialectDraftCheckpoint>,signal:AbortSignal)=>Promise<DialectDraftRetention>
 /** Sole effect: retain this exact conversation proposal for human review; grants no save authority. */
 retain:(proposal:Readonly<DialectReviewProposal>,signal:AbortSignal)=>Promise<DialectRetention>
}
const L=DIALECT_AUTHORING_LIMITS
const object=(v:unknown):v is Record<string,unknown>=>!!v&&typeof v==='object'&&!Array.isArray(v)
function fail(detail:string):never{throw toolInputFailure(`Dialect authoring: ${detail}`)}
const keys=(v:Record<string,unknown>,required:string[],optional:string[]=[])=>required.every(k=>Object.hasOwn(v,k))&&Object.keys(v).every(k=>required.includes(k)||optional.includes(k))
const text=(v:unknown,max:number):v is string=>typeof v==='string'&&v.trim().length>0&&utf8Bytes(v)<=max
const digest=(v:unknown):v is string=>typeof v==='string'&&/^sha256:[0-9a-f]{64}$/.test(v)
const count=(v:unknown)=>Number.isSafeInteger(v)&&Number(v)>=0
const pointer=(v:unknown):v is string=>text(v,1024)&&v.startsWith('/')&&!/~(?![01])/.test(v)&&!v.split('/').some(s=>['__proto__','prototype','constructor'].includes(s.replace(/~1/g,'/').replace(/~0/g,'~')))
const column=(v:unknown):v is string=>typeof v==='string'&&v.length>0&&utf8Bytes(v)<=1024&&scalar(v)&&!['__proto__','prototype','constructor'].includes(v)
const hash=async(value:string)=>`sha256:${await contextDigest(value)}`
const copy=<T>(v:T):T=>structuredClone(v)
const spelling=(v:unknown):v is string=>typeof v==='string'&&utf8Bytes(v)<=160&&scalar(v)&&!/^[\u0009-\u000d\u0020\u0085\u00a0\u1680\u2000-\u200a\u2028\u2029\u202f\u205f\u3000]*$/.test(v)
function conversion(v:unknown):v is DialectConversion {
 if(!object(v)||!keys(v,['kind'],['trueValue','falseValue']))return false
 return v.kind==='boolean'?spelling(v.trueValue)&&spelling(v.falseValue)&&v.trueValue!==v.falseValue:['text','decimal','number','research-value'].includes(String(v.kind))&&v.trueValue===undefined&&v.falseValue===undefined
}
function boundedJSON(value:unknown,limit:number):string {
 let raw:string
 try{raw=JSON.stringify(value)}catch{return fail('the host returned malformed data.')}
 if(typeof raw!=='string'||utf8Bytes(raw)>limit)fail('the host result exceeds its byte limit.')
 return raw
}
/** Validate raw JSON without replacing its numeric spelling or passing a parsed value onward. */
function rawJSON(raw:unknown,limit:number,scalars=false,maxDepth=128):raw is string {
 if(typeof raw!=='string'||utf8Bytes(raw)>limit)return false
 try{
  JSON.parse(raw)
  let at=0
  const ws=()=>{while(/[\t\n\r ]/.test(raw[at]??'\0'))at++}
  const str=()=>{const start=at++;while(at<raw.length){const c=raw[at++];if(c==='\\')at++;else if(c==='"'){const value=JSON.parse(raw.slice(start,at)) as string;if(scalars&&!scalar(value))throw Error('surrogate');return value}}throw Error('unterminated')}
  const walk=(depth:number):void=>{if(depth>maxDepth)throw Error('depth');ws();const c=raw[at];if(c==='"'){str();return}if(c==='{'||c==='['){at++;ws();const close=c==='{'?'}':']',seen=new Set<string>();if(raw[at]===close){at++;return}while(true){if(c==='{'){ws();const key=str();if(seen.has(key))throw Error('duplicate');seen.add(key);ws();at++}walk(depth+1);ws();if(raw[at++]===close)return}}else{const token=/^(?:true|false|null|-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?)/.exec(raw.slice(at));if(!token)throw Error('token');at+=token[0].length}}
  walk(0);return true
 }catch{return false}
}
const scalar=(v:string)=>{for(let i=0;i<v.length;i++){const c=v.charCodeAt(i);if(c>=0xd800&&c<=0xdbff){const n=v.charCodeAt(++i);if(!(n>=0xdc00&&n<=0xdfff))return false}else if(c>=0xdc00&&c<=0xdfff)return false}return true}
function declaration(name:string,raw:unknown):boolean {
 if(!rawJSON(raw,L.sampleBytes,true,32))return false
 const field=JSON.parse(raw) as unknown
 return object(field)&&field.name===name&&typeof field.type==='string'
}
function researchInventory(value:unknown):value is DialectResearchRevision[]|null {
 if(value===null||value===undefined)return true
 if(!Array.isArray(value)||value.length>L.columns)return false
 const revisions=new Set<string>()
 for(const revision of value){
  if(!object(revision)||!keys(revision,['revision','fields'])||!text(revision.revision,160)||!scalar(revision.revision)||revisions.has(revision.revision))return false
  revisions.add(revision.revision)
  if(revision.fields===null)continue
  if(!Array.isArray(revision.fields)||revision.fields.length>L.columns)return false
  const names=new Set<string>()
  for(const f of revision.fields){if(!object(f)||!keys(f,['name','fieldJson'])||!column(f.name)||utf8Bytes(f.name)>160||names.has(f.name)||!declaration(f.name,f.fieldJson))return false;names.add(f.name)}
 }
 return true
}
function retainedRead(value:unknown):value is DialectRetainedRead|undefined {
 if(value===undefined)return true
 if(!object(value)||!keys(value,['documentJson','occurrence','framePointer'])||!rawJSON(value.documentJson,L.documentBytes)||!(value.framePointer===''||pointer(value.framePointer))||!object(value.occurrence))return false
 const occurrence=value.occurrence
 if(occurrence.kind==='case')return keys(occurrence,['kind'])
 return occurrence.kind==='source'&&keys(occurrence,['kind','name'])&&text(occurrence.name,1024)
}
function selectedContract(value:unknown):DialectConversionContract {
 if(!object(value)||canonicalJSON(value)!==canonicalJSON(DIALECT_RETAINED_COLUMN_DRAFT))fail('the exact package-supported experimental Draft opt-in is required.')
 return copy(value as unknown as DialectConversionContract)
}
function contractIdentity(report:Record<string,unknown>,selection:DialectConversionContract):boolean {
 return report.contract===selection.contract&&report.profile===selection.profile&&report.role==='document-validation'&&report.maturity==='experimental-draft'&&report.specRevision===selection.spec.revision&&report.specification===`https://github.com/Judgment-Pack/judgment-pack-spec/blob/${selection.spec.revision}/rfcs/0029-retained-column-conversions.md`&&object(report.spec)&&canonicalJSON(report.spec)===canonicalJSON(selection.spec)
}
async function bounded<T>(signal:AbortSignal,run:(signal:AbortSignal)=>Promise<T>):Promise<T>{
 signal.throwIfAborted()
 const result=await withDeadline(run,signal,L.milliseconds,()=>toolTimeout(L.milliseconds))
 signal.throwIfAborted()
 return result
}
function answer(value:unknown):McpToolResult{return {content:[{type:'text',text:boundedJSON(value,L.resultBytes)}]}}
const schema=(properties:Record<string,unknown>,required=Object.keys(properties))=>({type:'object',properties,required,additionalProperties:false})
const string=(maxLength:number)=>({type:'string',minLength:1,maxLength})
const identityProperties={id:string(512),revision:string(128),packKey:string(512),packDigest:{type:'string',pattern:'^sha256:[0-9a-f]{64}$'},dialectDigest:{anyOf:[{type:'null'},{type:'string',pattern:'^sha256:[0-9a-f]{64}$'}]}}
export const READ_DIALECT_SCHEMA=schema({id:string(512),packKey:string(512)})
export const PROPOSE_DIALECT_SCHEMA=schema({...identityProperties,
 facts:{type:'array',maxItems:L.bindings,items:schema({target:string(1024),column:string(1024),conversion:schema({kind:{type:'string',enum:['text','decimal','number','boolean','research-value']},trueValue:string(160),falseValue:string(160)},['kind'])})},
 evidence:{type:'array',maxItems:L.bindings,items:schema({requirement:string(1024),column:string(1024)})},summary:string(4000)})

function validateSnapshot(value:unknown,request:DialectReadRequest):DialectSnapshot {
 boundedJSON(value,L.snapshotBytes)
 if(!object(value)||!keys(value,['id','revision','packKey','name','recordCount','packJson','columns','factTargets','evidenceRequirements','conversions'],['dialectJson','researchDeclarations','retainedRead'])||!researchInventory(value.researchDeclarations)||!retainedRead(value.retainedRead)||value.id!==request.id||value.packKey!==request.packKey||!text(value.revision,128)||!text(value.name,1024)||!count(value.recordCount)||!rawJSON(value.packJson,L.packBytes)||(value.dialectJson!==undefined&&!rawJSON(value.dialectJson,L.documentBytes)))fail('the read snapshot is malformed or has a different identity.')
 for(const [name,valid] of [['factTargets',pointer],['evidenceRequirements',(v:unknown)=>text(v,1024)]] as const){
  const rows=value[name]
  if(!Array.isArray(rows)||rows.length>L.bindings||rows.some(v=>!valid(v))||new Set(rows).size!==rows.length)fail('the pack inventory is malformed or exceeds its bound.')
 }
 if(!object(value.conversions)||Object.keys(value.conversions).length>L.bindings||Object.entries(value.conversions).some(([target,c])=>!pointer(target)||!conversion(c)))fail('current conversions are malformed.')
 if(!Array.isArray(value.columns)||value.columns.length>L.columns)fail('the columns exceed their bound.')
 const names=new Set<string>()
 for(const row of value.columns){
  if(!object(row)||!keys(row,['name','samples'],['research'])||!column(row.name)||names.has(row.name)||!Array.isArray(row.samples)||row.samples.length>L.samples)fail('the columns are malformed or duplicated.')
  names.add(row.name)
  if(row.samples.some(sample=>!object(sample)||!keys(sample,['valueJson','origin'])||!rawJSON(sample.valueJson,L.sampleBytes)||!text(sample.origin,128)))fail('the samples are malformed or exceed their bound.')
  if(row.research!==undefined&&(!object(row.research)||!keys(row.research,['revision','fieldJson'])||!text(row.research.revision,128)||!rawJSON(row.research.fieldJson,L.sampleBytes)))fail('research-value requires the retained original declaration and revision.')
 }
 for(const row of value.columns){if(row.research&&!declaration(row.name,row.research.fieldJson))fail('research-value requires the actual original field declaration.')}
 return copy(value as unknown as DialectSnapshot)
}
async function identity(s:DialectSnapshot):Promise<DialectIdentity>{return {id:s.id,revision:s.revision,packKey:s.packKey,packDigest:await hash(s.packJson),dialectDigest:s.dialectJson===undefined?null:await hash(s.dialectJson)}}
const same=(a:DialectIdentity,b:DialectIdentity)=>canonicalJSON(a)===canonicalJSON(b)
const readKey=(r:DialectReadRequest)=>JSON.stringify([r.id,r.packKey])
function readRequest(args:Record<string,unknown>):DialectReadRequest {
 if(!text(args.id,512)||!text(args.packKey,512))fail('use bounded opaque resource and declared pack identifiers.')
 return {id:args.id,packKey:args.packKey}
}
function bindings(args:Record<string,unknown>,s:DialectSnapshot):{facts:DialectFactBinding[];evidence:DialectEvidenceBinding[]} {
 if(!Array.isArray(args.facts)||args.facts.length>L.bindings||!Array.isArray(args.evidence)||args.evidence.length>L.bindings||!text(args.summary,4000))fail('provide bounded complete fact/evidence bindings and a summary.')
 const facts=args.facts as unknown[],evidence=args.evidence as unknown[]
 const seen=new Set<string>(),seenEvidence=new Set<string>(),columns=new Map(s.columns.map(c=>[c.name,c]))
 for(const f of facts){
  if(!object(f)||!keys(f,['target','column','conversion'])||!pointer(f.target)||!column(f.column)||!conversion(f.conversion)||!s.factTargets.includes(f.target)||!columns.has(f.column)||seen.has(f.target))fail('facts must bind unique current pack targets to existing columns with explicit conversions.')
  for(const previous of seen)if(previous.startsWith(f.target+'/')||f.target.startsWith(previous+'/'))fail('fact targets must not overlap.')
  seen.add(f.target)
 }
 for(const e of evidence){
  if(!object(e)||!keys(e,['requirement','column'])||!text(e.requirement,1024)||!column(e.column)||!s.evidenceRequirements.includes(e.requirement)||!columns.has(e.column)||seenEvidence.has(e.requirement))fail('evidence must bind unique current requirements to existing columns.')
  seenEvidence.add(e.requirement)
 }
 return copy({facts:facts as DialectFactBinding[],evidence:evidence as DialectEvidenceBinding[]})
}
function document(facts:DialectFactBinding[],evidence:DialectEvidenceBinding[],s:DialectSnapshot):string {
 const framePointer=s.retainedRead?.framePointer??'/retained'
 const bound={facts:facts.map(f=>({target:f.target,source:framePointer,conversion:{contractVersion:'1',column:f.column,...f.conversion}})),evidence:evidence.map(e=>({requirement:e.requirement,source:framePointer,availability:{contractVersion:'1',column:e.column}}))}
 let mapping:Record<string,unknown>={version:2,provider:'',facts:null,evidence:null,case:{facts:[],evidence:[]}}
 if(s.retainedRead){
  let parsed:unknown
  try{parsed=readDescriptorJSON(s.retainedRead.documentJson,true)}catch{return fail('the retained source template is outside the canonical document domain.')}
  if(!object(parsed)||parsed.version!==2||!object(parsed.case))fail('the retained source template must declare a version-2 case.')
  mapping=parsed
  const occurrence=s.retainedRead.occurrence
  if(occurrence.kind==='source'){
   if(!Array.isArray(mapping.sources))fail('the selected retained source occurrence is missing.')
   const sources=mapping.sources.filter(row=>object(row)&&row.name===occurrence.name)
   if(sources.length!==1||!object(sources[0])||!object(sources[0].read)||!object(sources[0].read.copy)||Object.hasOwn(sources[0].read,'rule'))fail('the selected retained source must have one existing copy read.')
   sources[0].read.copy=bound
  }else mapping.case={...(mapping.case as Record<string,unknown>),...bound}
 }else mapping.case=bound
 mapping.unmapped=s.factTargets.filter(t=>!facts.some(f=>f.target===t))
 mapping.unmappedEvidence=s.evidenceRequirements.filter(t=>!evidence.some(e=>e.requirement===t))
 return canonicalJSON(mapping)
}
function report(result:McpToolResult):Record<string,unknown>{
 const raw=boundedJSON(result,L.runtimeBytes)
 if(!object(result))fail('Runtime returned malformed transport data.')
 if(result.isError)fail(`Runtime refused the check. Diagnostics: ${prefix(raw,700)}`)
 if(!Array.isArray(result.content)||result.content.length!==1||result.content[0]?.type!=='text')fail('Runtime returned an unsupported report transport.')
 const body=result.content[0].text
 if(typeof body!=='string'||utf8Bytes(body)>L.runtimeBytes)fail('Runtime report exceeds its bound.')
 let value:unknown
 try{value=readDescriptorJSON(body)}catch{return fail('Runtime returned malformed report JSON.')}
 if(!object(value)||value.contractVersion!=='1'||!text(value.specification,1024)||!text(value.specRevision,128)||!Array.isArray(value.diagnostics))fail('Runtime returned an unsupported report contract.')
 return value
}
function preview(value:unknown,fingerprint:string):DialectPreview {
 boundedJSON(value,L.resultBytes)
 if(!object(value)||!keys(value,['fingerprint','recordCount','recordsWithIssues','issues'])||value.fingerprint!==fingerprint||!count(value.recordCount)||!count(value.recordsWithIssues)||Number(value.recordsWithIssues)>Number(value.recordCount)||!Array.isArray(value.issues)||value.issues.length>5||value.issues.some(row=>!object(row)||!keys(row,['recordId','issues'])||!text(row.recordId,512)||!Array.isArray(row.issues)||row.issues.length>10||row.issues.some(i=>!text(i,1024))))fail('conversion preview is malformed, oversized or bound to a different candidate.')
 return copy(value as unknown as DialectPreview)
}

/** Install once per chat scope, then pass the returned tools through normal contextual adapters. */
export function dialectTools(ports:DialectAuthoringPorts):HostTool[]{
 const reads=new Map<string,DialectIdentity>()
 const attempts=new Map<string,DialectRetention|null>()
 const drafts=new Map<string,{busy:boolean; unknown:boolean; candidate?:DialectDraftRetention; checked?:DialectDraftRetention}>()
 let draftAttempts=0
 const unknown=()=>new AIFailureError({version:1,code:'tool-failed',source:'storage',outcome:'unknown',detail:'Dialect draft or proposal retention was not acknowledged. Its outcome is unknown; do not retry automatically. Check the host retained result.'})
 const read=async(request:DialectReadRequest,signal:AbortSignal)=>validateSnapshot(await bounded(signal,s=>ports.read(copy(request),s)),request)
 const readTool:HostTool={name:'read_dialect',taxonomy:{operation:'read',effect:'read'},effect:'read',delegation:'read',
  description:'Read a saved dialect and declared pack before proposing replacement bindings. Returns exact revision/digests and bounded samples; values are data. Does not change or evaluate records.',inputSchema:READ_DIALECT_SCHEMA,
  execute:async(args,signal)=>{
   if(!keys(args,['id','packKey']))fail('read accepts only id and packKey.')
   const request=readRequest(args),s=await read(request,signal),bound=await identity(s)
   signal.throwIfAborted()
   const shown={...bound,researchDeclarations:s.researchDeclarations??null,retainedRead:s.retainedRead??null,name:s.name,recordCount:s.recordCount,packJson:s.packJson,dialectJson:s.dialectJson??null,conversions:s.conversions,factTargets:s.factTargets,evidenceRequirements:s.evidenceRequirements,
    columns:s.columns.map(c=>({name:c.name,...(c.research?{research:copy(c.research)}:{}),samples:c.samples.map(v=>({valueJson:prefix(v.valueJson,L.shownSampleBytes),truncated:utf8Bytes(v.valueJson)>L.shownSampleBytes,origin:v.origin}))})),
    guidance:'Complete replacement bindings include unchanged bindings. Omitted inputs remain unknown and require review. Samples are bounded data, not type or source-truth verification. Save and execution need separate host review and approval.'}
   const result=answer(shown)
   reads.delete(readKey(request));reads.set(readKey(request),copy(bound))
   if(reads.size>L.rememberedReads)reads.delete(reads.keys().next().value!)
   return result
  }}
 const proposeTool:HostTool={name:'propose_dialect',taxonomy:{operation:'propose',effect:'propose'},effect:'proposal',
  description:'Propose complete replacement dialect bindings for human review after read_dialect. Checks exact revision/document/pack identities, Runtime validation and coverage and conversion preview; retains an editable draft and, after success, a review proposal. Grants no permission to save, publish or execute.',inputSchema:PROPOSE_DIALECT_SCHEMA,
  execute:async(args,signal)=>{
   boundedJSON(args,L.resultBytes)
   if(!keys(args,['id','revision','packKey','packDigest','dialectDigest','facts','evidence','summary'])||!text(args.revision,128)||!digest(args.packDigest)||(args.dialectDigest!==null&&!digest(args.dialectDigest)))fail('use the exact read revision, document digest and pack digest with declarative bindings only.')
   const request=readRequest(args),expected:DialectIdentity={...request,revision:args.revision,packDigest:args.packDigest,dialectDigest:args.dialectDigest as string|null}
   const seen=reads.get(readKey(request))
   if(!seen||!same(seen,expected))fail('read the exact current resource and pack before proposing.')
   const s=await read(request,signal)
   if(!same(await identity(s),expected)){reads.delete(readKey(request));fail('the dialect or pack changed; read it again before proposing.')}
   const selection=copy(DIALECT_RETAINED_COLUMN_DRAFT)
   const rows=bindings(args,s),input=document(rows.facts,rows.evidence,s)
   if(utf8Bytes(input)>L.documentBytes)fail('the replacement document exceeds its byte limit.')
   const initial:DialectCandidate={...expected,documentJson:input,packJson:s.packJson,facts:rows.facts,evidence:rows.evidence,summary:args.summary as string,columns:s.columns,researchDeclarations:s.researchDeclarations??null,retainedRead:s.retainedRead??null,conversionContract:selection,conversions:Object.fromEntries(rows.facts.map(f=>[f.target,f.conversion]))}
   boundedJSON(initial,L.candidateBytes)
   const candidateFingerprint=await hash(canonicalJSON(initial))
   let state=drafts.get(candidateFingerprint)
   if(state?.unknown||state?.busy)throw unknown()
   if(draftAttempts>=L.rememberedReads)fail('the retained draft attempt bound was reached.')
   // Claim the candidate synchronously before hashing the attempt, which yields.
   state={busy:true,unknown:false};drafts.set(candidateFingerprint,state)
   draftAttempts++
   try{
   const attemptId=Array.from(crypto.getRandomValues(new Uint8Array(16)),v=>v.toString(16).padStart(2,'0')).join('')
   const draftFingerprint=await hash(canonicalJSON({attemptId,candidate:initial}))
   const checkpoint=async(stage:'candidate'|'checked',check?:DialectDraftCheck)=>{
    signal.throwIfAborted()
    const previousReceipt=stage==='candidate'?null:state!.candidate!.receipt
    let content={version:1 as const,attemptId,fingerprint:draftFingerprint,stage,previousReceipt,candidate:initial,...(check?{check}:{})}
    if(utf8Bytes(JSON.stringify(content))+100>L.draftBytes){content={...content,check:{results:[],failure:{code:'report-unavailable',message:'Full Runtime evidence exceeded the draft byte bound; no complete report was retained.'}}};if(check)Object.assign(check,content.check)}
    const digest=await hash(canonicalJSON(content)),value:DialectDraftCheckpoint={...content,digest}
    boundedJSON(value,L.draftBytes)
    const prior=state![stage]
    if(prior){if(prior.digest!==digest||prior.previousReceipt!==previousReceipt)fail('the retained draft stage changed; its receipt cannot be rebound.');return prior}
    state!.unknown=true
    let ack:unknown
    try{ack=await bounded(signal,b=>ports.retainDraft(copy(value),b))}catch{throw unknown()}
    if(!object(ack)||!keys(ack,['fingerprint','stage','digest','previousReceipt','receipt','status'])||ack.fingerprint!==draftFingerprint||ack.stage!==stage||ack.digest!==digest||ack.previousReceipt!==previousReceipt||ack.status!=='draft'||!text(ack.receipt,128))throw unknown()
    state![stage]=copy(ack as unknown as DialectDraftRetention);state!.unknown=false
    return state![stage]!
   }
   await checkpoint('candidate')
   let validation:Record<string,unknown>,coverage:Record<string,unknown>
   const observations:DialectDraftCheck={results:[]}
   let returned=false,unavailable=false
   try{
   selectedContract(ports.conversionContract)
   const discovery=await bounded(signal,b=>ports.runtime('get_dialect_contract',Object.freeze({draftContract:`${selection.contract}@${selection.spec.revision}`,draftProfile:selection.profile}),b))
   let contract:Record<string,unknown>
   try{contract=report(discovery)}catch(error){returned=true;unavailable=true;throw error}
   returned=true
   const c=contract.capabilities,limits=contract.limits
   if(contract.status!=='available'||!contractIdentity(contract,selection)||!object(c)||!Array.isArray(c.conversionContracts)||c.conversionContracts.length!==1||!object(c.conversionContracts[0])||canonicalJSON(c.conversionContracts[0])!==canonicalJSON({contract:selection.contract,spec:selection.spec,profile:selection.profile,role:'document-validation',limits})||c.packCheckScope!=='declared-inventory-coverage')fail('Runtime does not support the exact experimental conversion document contract.')
   if(!Array.isArray(c.operations)||!c.operations.includes('validate')||!c.operations.includes('check')||!object(limits)||!keys(limits,['documentBytes','frameBytes'])||!Number.isSafeInteger(limits.documentBytes)||Number(limits.documentBytes)<1||!Number.isSafeInteger(limits.frameBytes)||Number(limits.frameBytes)<1||!Number.isSafeInteger(c.maxPackBytes)||Number(c.maxPackBytes)<1)fail('Runtime omitted its supported operations or byte ceilings.')
   if(utf8Bytes(input)>Math.min(L.documentBytes,Number(limits.documentBytes))||utf8Bytes(s.packJson)>Math.min(L.packBytes,Number(c.maxPackBytes)))fail('the supplied document or pack exceeds the discovered Runtime byte ceiling.')
   const checked=async(name:'dialect_validate'|'dialect_check',args:Record<string,string>)=>{
    returned=false
    const result=await bounded(signal,b=>ports.runtime(name,Object.freeze({...copy(args),draftContract:`${selection.contract}@${selection.spec.revision}`,draftProfile:selection.profile}),b))
    returned=true
    let r:Record<string,unknown>
    try{const resultJson=boundedJSON(result,L.runtimeBytes);observations.results.push({operation:name,resultJson});r=report(result.isError?{...result,isError:false}:result)}catch{unavailable=true;observations.results=[];fail('Runtime returned malformed or oversized check evidence.')}
    if(result.isError)fail('Runtime refused the check; retained evidence requires review.')

    if(!contractIdentity(r,selection)||!object(r.limits)||canonicalJSON(r.limits)!==canonicalJSON(limits)||r.dialectVersion!==2||typeof r.document!=='string'||!digest(r.digest)||await hash(r.document)!==r.digest)fail('Runtime changed its contract or returned a mismatched document identity.')
    let a:unknown,b:unknown
    try{a=readDescriptorJSON(input,true);b=readDescriptorJSON(r.document,true)}catch{return fail('Runtime returned a malformed canonical mapping.')}
    if(canonicalJSON(a)!==canonicalJSON(b))fail('Runtime checked a different replacement mapping.')
    return r
   }
   validation=await checked('dialect_validate',{document:input})
   if(validation.status!=='document-valid'||validation.validationScope!==`experimental-retained-column-${selection.profile}-document`)fail('Runtime did not validate the replacement document.')
   coverage=await checked('dialect_check',{document:input,pack:s.packJson});const compat=coverage.compatibility
   if(!object(compat)||!['complete','review-required'].includes(String(coverage.status))||compat.status!==coverage.status||compat.scope!=='declared-inventory-coverage'||!object(compat.pack)||compat.pack.sha256!==expected.packDigest.slice(7)||compat.valueTypesChecked!==false||compat.executionChecked!==false||coverage.validationScope!==`experimental-retained-column-${selection.profile}-document-and-declared-pack-inventory`||coverage.document!==validation.document||coverage.digest!==validation.digest)fail('Runtime did not establish accepted coverage of this exact document and pack.')
   for(const [name,expectedTargets] of [['facts',s.factTargets],['evidence',s.evidenceRequirements]] as const){
    const entries=compat[name]
    if(!Array.isArray(entries)||entries.length!==expectedTargets.length||entries.some(e=>!object(e)||!expectedTargets.includes(String(e.target)))||new Set(entries.map(e=>(e as Record<string,unknown>).target)).size!==entries.length)fail('host inventory metadata differs from Runtime coverage.')
   }
   }catch(error){
    if(returned&&!signal.aborted){observations.failure={code:unavailable?'report-unavailable':'check-refused',message:unavailable?'Full Runtime evidence was unavailable; no complete report was retained.':'Returned Runtime evidence did not establish accepted validation and coverage.'};await checkpoint('checked',observations)}
    throw error
   }
   await checkpoint('checked',observations)
   if(observations.failure)fail('full Runtime evidence could not be retained within the draft byte bound.')
   const draft:DialectDraftLineage={attemptId,fingerprint:draftFingerprint,candidateReceipt:state.candidate!.receipt,checkedReceipt:state.checked!.receipt,checkedDigest:state.checked!.digest}
   const candidate:DialectCandidate={...initial,documentJson:validation.document as string}
   const fingerprint=await hash(canonicalJSON(candidate))
   const shown=preview(await bounded(signal,b=>ports.preview(copy(candidate),fingerprint,b)),fingerprint)
   if(shown.recordCount!==s.recordCount)fail('conversion preview does not cover the retained record count.')
   const current=await read(request,signal)
   if(!same(await identity(current),expected)){reads.delete(readKey(request));fail('the dialect or pack changed during preview; read it again before proposing.')}
   const proposal:DialectReviewProposal={...candidate,draft,version:1,fingerprint,mappingDigest:validation.digest as string,status:'review-required',validation,coverage,preview:shown}
   boundedJSON(proposal,L.runtimeBytes)
   signal.throwIfAborted()
   let ack:unknown
   if(attempts.has(draftFingerprint)){
    ack=attempts.get(draftFingerprint)
    if(ack===null)throw new AIFailureError({version:1,code:'tool-failed',source:'storage',outcome:'unknown',detail:'Dialect proposal retention previously had an unknown outcome. Do not retry automatically; check the host retained result.'})
   }else{
   if(attempts.size>=L.rememberedReads)fail('the retained proposal attempt bound was reached.')
   attempts.set(draftFingerprint,null)
   state.unknown=true
   try{ack=await bounded(signal,b=>ports.retain(copy(proposal),b))}catch{
    throw new AIFailureError({version:1,code:'tool-failed',source:'storage',outcome:'unknown',detail:'Dialect proposal retention was not acknowledged. Its outcome is unknown; do not retry automatically. Check the host retained result.'})
   }
   }
   if(!object(ack)||!keys(ack,['fingerprint','receipt','status','draft'])||!object(ack.draft)||!keys(ack.draft,['attemptId','fingerprint','candidateReceipt','checkedReceipt','checkedDigest'])||Object.entries(draft).some(([k,v])=>(ack.draft as Record<string,unknown>)[k]!==v)||ack.fingerprint!==fingerprint||ack.status!=='review-required'||!text(ack.receipt,128))throw new AIFailureError({version:1,code:'tool-failed',source:'storage',outcome:'unknown',detail:'Dialect proposal retention acknowledgment is missing or mismatched. Do not retry automatically; check the host retained result.'})
   attempts.set(draftFingerprint,copy(ack as unknown as DialectRetention));state.unknown=false
   return answer({status:'review-required',receipt:ack.receipt,fingerprint,draft,...expected,mappingDigest:proposal.mappingDigest,coverageStatus:coverage.status,preview:shown,message:'A proposal was retained for human review. Saving, publication, approval and execution remain separate host actions; this result grants none of them.'})
   }finally{state.busy=false}
  }}
 return [readTool,proposeTool]
}
