import privateDraft from '../../contract/testdata/dialect-authoring/superseded-private-draft-identity.json'
// @vitest-environment node
import rc2 from '../../contract/testdata/dialect-authoring/superseded-rc2-identity.json'
import {expect,it,vi} from 'vitest'
import {createHash} from 'node:crypto'
import {transformSync} from 'esbuild'
import reference from '../../contract/testdata/dialect-authoring/desk-reference.json'
import compatibility from '../../contract/testdata/dialect-authoring/compatibility.json'
import fixture from '../../contract/testdata/dialect-authoring/authoring.json'
import {dialectFixture} from './testing/dialects'
import {checkArtifact} from './execution/checkArtifact'
import {canonicalJSON} from './context/chat'
import {contextDigest} from './context/ledger'
import {DIALECT_AUTHORING_LIMITS} from './dialects'
import type {DialectDraftCheckpoint} from './dialects'

// Execute exact pinned bodies; only host I/O/build/Runtime ports are fixtures. No Desk checkout/browser.
function source(path:string){const s=reference.sources.find(s=>s.path===path)!;const raw=Buffer.from(s.source);expect(createHash('sha1').update(Buffer.concat([Buffer.from(`blob ${raw.length}\0`),raw])).digest('hex')).toBe(s.blob);return s.source}
function executeBody(body:string,bindings:Record<string,unknown>,parameters:string){const code=transformSync(`async function pinned(${parameters}){${body}\n}`,{loader:'ts',target:'es2022'}).code;return new Function(...Object.keys(bindings),`${code};return pinned`)(...Object.values(bindings))}
function desk(mode:string){
 const text=source('dialectChatTools.ts'),marker='execute:async(args,signal)=>{',start=text.indexOf(marker,text.indexOf("{name:'propose_dialect'"))+marker.length,body=text.slice(start,text.lastIndexOf('\n  }},'))
 const id='prep_'+'a'.repeat(32),revision='b'.repeat(64),pack={key:'declared',digest:'sha256:'+'c'.repeat(64)},saved={sha256:revision,document:{id,name:'Records',recipeVersion:1}},actions=new Map<string,any>(),trace:string[]=[]
 let changed=false
 class CheckError extends Error{constructor(readonly report:any){super('Check refused')}}
 const report={status:mode==='failed-validation'?'refused':'complete',document:fixture.expectedDocument,digest:fixture.expectedMappingDigest,diagnostics:[],compatibility:{status:'complete'}}
 const execute=executeBody(body,{
  options:{afterTurnId:()=> 'turn',retainAction:async(action:any)=>{trace.push(action.dialectCheck.report?'checked':'candidate');actions.set(action.id,structuredClone(action))}},
  snapshots:new Map([[id+'\n'+pack.key,{revision,packDigest:pack.digest}]]),validDialectRevision:()=>true,validDialectProposal:()=>true,toolInputFailure:(s:string)=>Error(s),
  read:async()=>({saved:changed?{...saved,sha256:'d'.repeat(64)}:structuredClone(saved),pack}),
  buildDialect:()=>({mapping:JSON.parse(fixture.expectedDocument),pack:'{}'}),canonicalize:canonicalJSON,checkArtifact,
  checkDialect:async()=>{trace.push('check');if(mode==='transport-failed')throw Error('Runtime unavailable');if(mode==='failed-validation')throw new CheckError(report);return report},DialectCheckError:CheckError,
  dialectAPI:async()=>{trace.push('preview');if(mode==='preview-failed')throw Error('Preview failed');if(mode==='changed-preview')changed=true;return {rows:[]}},
  result:(v:unknown)=>v,
 },'args,signal')
 return {actions,trace,run:()=>execute({id,revision,packKey:pack.key,packDigest:pack.digest,...fixture.bindings},new AbortController().signal)}
}
for(const row of compatibility.cases)it('compares pinned actual Desk retention with exported package: '+row.mode,async()=>{
 const old=desk(row.mode),x=dialectFixture();x.state.mode=row.mode
 const bound=await x.read();await old.run().catch(()=>{});await x.propose(bound).catch(()=>{})
 expect(old.trace.filter(s=>s==='candidate'||s==='checked')).toEqual(row.draftStages)
 expect(x.state.drafts.map(d=>d.stage)).toEqual(row.draftStages)
 expect(x.state.retained).toHaveLength(row.finals)
 expect(x.state.calls.includes('preview')).toBe(row.preview)
 expect(old.trace.includes('preview')).toBe(row.preview)
 const retained=[...old.actions.values()][0]
 expect(retained.dialectCheck.document).toBe(fixture.expectedDocument)
 expect(!!retained.dialectCheck.report).toBe(row.report)
 expect(retained.applied).toBeUndefined()
 expect(x.state.drafts[0]!.candidate.documentJson).toBe(fixture.expectedDocument)
 if(row.report)expect(x.state.drafts[1]!.check!.results.length).toBeGreaterThan(0)
 // Drafts remain editable intent; they never carry applied/save authority or final-success status.
 expect(x.state.drafts.every(d=>!('applied'in d)&&!('status'in d))).toBe(true)
 if(row.mode==='complete'){
  await old.run();await x.propose(bound)
  expect(old.actions.size).toBe(2);expect(x.state.retained).toHaveLength(2)
  expect(x.state.drafts.map(d=>d.stage)).toEqual(['candidate','checked','candidate','checked'])
  expect(x.state.retained[0]!.draft.attemptId).not.toBe(x.state.retained[1]!.draft.attemptId)
  expect(x.state.retained[0]!.fingerprint).toBe(x.state.retained[1]!.fingerprint)
 }
})
for(const stage of ['candidate','checked'] as const)for(const drift of ['lost','stage','digest','previousReceipt','fingerprint','abort'])it(`latches ${stage} ${drift} before further effects`,async()=>{
 const x=dialectFixture(),bound=await x.read(),original=x.ports.retainDraft,stop=new AbortController()
 x.ports.retainDraft=async(c,s)=>{const ack=await original(c,s);if(c.stage===stage){if(drift==='lost')throw Error('unknown');if(drift==='abort'){stop.abort();return ack}return {...ack,[drift]:drift==='previousReceipt'?'wrong':'drift'}}return ack}
 const args={...Object.fromEntries(['id','revision','packKey','packDigest','dialectDigest'].map(k=>[k,bound[k]])),...fixture.bindings}
 await expect(x.tools[1]!.execute(args,stop.signal)).rejects.toMatchObject({failure:{outcome:'unknown'}})
 const before=[...x.state.calls]
 await expect(x.propose(bound)).rejects.toMatchObject({failure:{outcome:'unknown'}})
 expect(x.state.calls.slice(before.length)).toEqual(['read','field-index','page'])
 expect(x.state.drafts).toHaveLength(stage==='candidate'?1:2);expect(x.state.retained).toHaveLength(0)
 expect(x.state.calls.includes('preview')).toBe(false)
})
for(const mode of ['malformed-check','oversized-report'])it('retains bounded authored failure without inventing full report '+mode,async()=>{
 const x=dialectFixture(),bound=await x.read();x.state.mode=mode;await expect(x.propose(bound)).rejects.toThrow()
 expect(x.state.drafts).toHaveLength(2);const last=x.state.drafts[1]!
 expect(last.check).toEqual({results:[],failure:{code:'report-unavailable',message:'Full Runtime evidence was unavailable; no complete report was retained.'}})
 expect(Buffer.byteLength(JSON.stringify(last))).toBeLessThanOrEqual(DIALECT_AUTHORING_LIMITS.draftBytes)
 expect(last.candidate).toEqual(x.state.drafts[0]!.candidate);expect(x.state.retained).toHaveLength(0)
})
it('binds exact checkpoint digest, previous receipt, initial spelling and canonical final lineage',async()=>{
 const x=dialectFixture(),bound=await x.read(),original=x.ports.runtime
 x.ports.runtime=async(name,args,s)=>{const r=await original(name,args,s);if(name!=='get_dialect_contract'){const content=r.content?.[0];if(!content||typeof content.text!=='string')throw Error('Expected Runtime text report');const report=JSON.parse(content.text);report.document=JSON.stringify(JSON.parse(args.document!),null,2);report.digest='sha256:'+await contextDigest(report.document);content.text=JSON.stringify(report)}return r}
 await x.propose(bound)
 const [candidate,checked]=x.state.drafts
 for(const c of x.state.drafts){const {digest,...content}=c;expect(digest).toBe('sha256:'+await contextDigest(canonicalJSON(content)))}
 expect(checked!.previousReceipt).toBe('draft:candidate')
 expect(candidate!.candidate.documentJson).toBe(fixture.expectedDocument)
 expect(x.state.retained[0]!.documentJson).not.toBe(candidate!.candidate.documentJson)
 expect(x.state.retained[0]!.draft).toEqual({attemptId:candidate!.attemptId,fingerprint:candidate!.fingerprint,candidateReceipt:'draft:candidate',checkedReceipt:'draft:checked',checkedDigest:checked!.digest})
})
it('refuses final receipt lineage drift and never retries through new nonce',async()=>{
 const x=dialectFixture(),bound=await x.read(),original=x.ports.retain;x.ports.retain=async(...args)=>{const ack=await original(...args);return {...ack,draft:{...ack.draft,checkedReceipt:'wrong'}}}
 await expect(x.propose(bound)).rejects.toMatchObject({failure:{outcome:'unknown'}})
 const before=[...x.state.calls];await expect(x.propose(bound)).rejects.toMatchObject({failure:{outcome:'unknown'}})
 expect(x.state.calls.slice(before.length)).toEqual(['read','field-index','page']);expect(x.state.retained).toHaveLength(1)
})
it('pinned actual review rechecks edited draft; preview refusal and fresh pack/CAS refusal cannot save',async()=>{
 const preview=source('ConversionPreview.tsx'),from=preview.indexOf('async function check(){')+'async function check(){'.length,to=preview.indexOf('return <Disclosure',from)
 const body=preview.slice(from,to).trim().slice(0,-1),trace:string[]=[],errors:string[]=[]
 await executeBody(body,{setBusy:()=>{},setError:(e:string)=>errors.push(e),checkDialect:async()=>{trace.push('fresh-check');throw Error('Refused edited mapping')},value:{mapping:{edited:true},pack:'{}'},saved:{sha256:'current',document:{id:'opaque'}},dialectAPI:async()=>{trace.push('preview');return {rows:[]}},setResult:()=>{},onChecked:()=>trace.push('checked'),identity:'edited'},'')()
 expect(trace).toEqual(['fresh-check']);expect(errors.at(-1)).toBe('Refused edited mapping')
 const review=source('DialectProposalReview.tsx'),start=review.indexOf('const save=async()=>{')+'const save=async()=>{'.length,end=review.indexOf('\n return <Dialog',start),saveBody=review.slice(start,end).trim().slice(0,-1)
 for(const failure of ['pack','cas']){
  const writes:string[]=[],error:string[]=[]
  const saveSource=source('saveRecipe.ts'),casBody=saveSource.slice(saveSource.indexOf(') {')+3,saveSource.lastIndexOf('}'))
  const actualSave=executeBody(casBody,{dialectAPI:async(path:string,method:string,payload:unknown,revision:string)=>{expect(path).toBe('/opaque');expect(payload).toMatchObject({name:'Records',recipe:{qualification:{edited:true}}});expect(method).toBe('PUT');expect(revision).toBe('stale');writes.push('cas');throw Error('Revision changed')}},'saved,name,recipe')
  await executeBody(saveBody,{inFlight:{current:false},blocked:false,previewRequired:false,setBusy:()=>{},setError:(e:string)=>error.push(e),readDialectPack:async()=>({digest:failure==='pack'?'changed':'expected'}),value:{key:'declared',digest:'expected',edited:true},saved:{sha256:'stale',document:{id:'opaque',name:'Records',recipe:{}}},saveDialectRecipe:actualSave,msg:(s:string)=>s,onSaved:()=>writes.push('saved'),clear:()=>{},onClose:()=>{}},'')()
  expect(writes).toEqual(failure==='pack'?[]:['cas']);expect(error.at(-1)).toContain(failure==='pack'?'pack changed':'Revision changed')
 }
 expect(review).toContain('<DialectEditor');expect(review).toContain('action.dialectCheck?.report')
})
it('reads every public draft-retention vector with exact closed type members',async()=>{
 const {default:vectors}=await import('../../contract/vectors/dialect-authoring/retention.json')
 const stages=new Set<string>(),operations=new Set<string>(),failures=new Set<string>()
 for(const row of vectors.items){
  if('candidate'in row){const c=row as unknown as DialectDraftCheckpoint;stages.add(c.stage);expect(c.attemptId).toMatch(/^[a-f0-9]{32}$/);expect(c.candidate.columns[0]!.samples[0]!.valueJson).toBe('false');if(c.candidate.conversionContract)expect([fixture.conversionContract,privateDraft,rc2]).toContainEqual(c.candidate.conversionContract);for(const result of c.check?.results??[])operations.add(result.operation);if(c.check?.failure)failures.add(c.check.failure.code)}
  else if('status'in row){expect(row.status).toBe('draft');expect(row.receipt).toBeTruthy()}
  else expect(row.checkedReceipt).toBe('checked:one')
 }
 expect([...stages]).toEqual(['candidate','checked']);expect([...operations]).toEqual(['dialect_validate','dialect_check']);expect([...failures]).toEqual(['check-refused','report-unavailable'])
})
it('bounds explicit attempt count and never writes a thirty-third draft',async()=>{
 const x=dialectFixture(),bound=await x.read()
 for(let i=0;i<32;i++)await x.propose(bound)
 await expect(x.propose(bound)).rejects.toThrow('draft attempt bound')
 expect(x.state.drafts).toHaveLength(64);expect(x.state.retained).toHaveLength(32)
})

it('claims candidate before async attempt hashing so concurrent explicit calls cannot overlap writes',async()=>{
 const x=dialectFixture(),bound=await x.read(),original=crypto.subtle.digest.bind(crypto.subtle)
 let release!:()=>void,firstEntered!:()=>void,secondEntered!:()=>void,calls=0
 const held=new Promise<void>(r=>{release=r}),entered=new Promise<void>(r=>{firstEntered=r}),overlap=new Promise<void>(r=>{secondEntered=r})
 const spy=vi.spyOn(crypto.subtle,'digest').mockImplementation(async(algorithm,data)=>{
  if(new TextDecoder().decode(data).startsWith('{"attemptId":')){if(++calls===1)firstEntered();else secondEntered();await held}
  return original(algorithm,data)
 })
 let first:Promise<unknown>|undefined,second:Promise<unknown>|undefined
 try{
  first=x.propose(bound);await entered;second=x.propose(bound)
  const boundary=await Promise.race([second.then(()=> 'completed',()=> 'refused'),overlap.then(()=> 'overlap')])
  release();await first;await second.catch(()=>{})
  expect(x.state.drafts).toHaveLength(2);expect(x.state.retained).toHaveLength(1)
  expect(boundary).toBe('refused')
 }finally{release();await Promise.allSettled([first,second]);spy.mockRestore()}
})
