import {describe,expect,it} from 'vitest'
import paging from '../../contract/vectors/dialect-authoring/paging.json'
import retained from '../../contract/testdata/dialect-authoring/retained-policy.json'
import fixture from '../../contract/testdata/dialect-authoring/authoring.json'
import {DIALECT_AUTHORING_LIMITS as L,dialectFieldIndex,dialectResearchIndex,dialectTools,readDialectDeclarations,type DialectDeclarationPage,type DialectDeclarationPageRequest,type DialectFieldIndexPage,type DialectIndexRevision,type DialectResearchIndex,type DialectResearchRevision} from './dialects'
import {dialectFixture} from './testing/dialects'
import {canonicalJSON} from './context/chat'
import {contextDigest} from './context/ledger'
import {utf8Bytes} from './context/policy'
import type {McpToolResult} from './engine'

// #243, #252: the complete original inventory travels as a bounded top index; revision field indexes and
// declarations arrive through read-port pages.
const signal=()=>new AbortController().signal
const body=(r:McpToolResult)=>JSON.parse(r.content![0]!.text!) as Record<string,unknown>
const items=paging.items as DialectDeclarationPage[]
const fieldIndexes=paging.fieldIndexes as DialectFieldIndexPage[]
interface PagingCase {name:string; pages?:number[]; fieldIndexPages?:number[]; expect:string; message?:string; requests?:DialectDeclarationPageRequest[]; changeDuringPreview?:boolean}
const cases=paging.cases as PagingCase[]

describe('paging vectors through the exported tools',()=>{
 it('the package index equals the handwritten digests',async()=>{expect(await dialectResearchIndex(paging.inventory as DialectResearchRevision[])).toEqual(paging.index)})
 for(const row of cases)it(row.name,async()=>{
  const x=dialectFixture()
  let current=paging.index as DialectResearchIndex
  x.ports.readFieldIndex=async request=>{x.state.calls.push('field-index');x.state.fieldIndexes.push(structuredClone(request));const at=(row.fieldIndexPages??[0,1]).find(i=>fieldIndexes[i]!.entry===request.entry);if(at===undefined)throw Error('no vector field index at entry '+request.entry);return structuredClone(fieldIndexes[at]!)}
  x.ports.read=async()=>{x.state.calls.push('read');return {...structuredClone(x.state.snapshot),researchIndex:structuredClone(current)}}
  x.ports.readDeclarations=async request=>{x.state.calls.push('page');x.state.pages.push(structuredClone(request));if(x.state.pages.length>50)throw Error('page call bound exceeded');const at=(row.pages??[]).find(i=>items[i]!.cursor===request.cursor);if(at===undefined)throw Error('no vector page at cursor '+request.cursor);return structuredClone(items[at]!)}
  const preview=x.ports.preview
  x.ports.preview=async(...args)=>{if(row.changeDuringPreview)current=paging.changedIndex as DialectResearchIndex;return preview(...args)}
  const tools=dialectTools(x.ports)
  const run=async()=>{const read=body(await tools[0]!.execute(fixture.request,signal()));return tools[1]!.execute({...Object.fromEntries(['id','revision','packKey','packDigest','dialectDigest'].map(k=>[k,read[k]])),...fixture.bindings},signal())}
  if(row.expect==='retained'){
   await run()
   expect(x.state.retained).toHaveLength(1)
   expect(x.state.retained[0]!.researchIndex).toEqual(paging.index);expect(x.state.drafts[0]!.candidate.researchIndex).toEqual(paging.index);expect(x.state.previewCandidate!.researchIndex).toEqual(paging.index)
   expect(x.state.pages.slice(0,row.requests!.length)).toEqual(row.requests)
   expect(x.state.fieldIndexes.slice(0,paging.fieldIndexRequests.length)).toEqual(paging.fieldIndexRequests)
   const candidate=x.state.retained[0]!
   expect(await readDialectDeclarations(x.ports,candidate,candidate.researchIndex,signal())).toEqual(paging.inventory)
   return
  }
  await expect(run()).rejects.toThrow(row.message!)
  expect(x.state.retained).toHaveLength(0)
  if(row.changeDuringPreview){expect(x.state.drafts).toHaveLength(2);expect(x.state.previewCandidate).toBeDefined()}
  else expect(x.state.drafts).toHaveLength(0)
 })
})

// #252 bound proof. The generators follow the vector's rules; the sizes were computed outside the package.
interface BoundCase {name:string; inventory?:{revisions:number; fields:number; label:string; name:string}; top?:{revisions:number; fields:number; bytes:number}; indexBytes:number; fieldIndexBytes?:number; oneLevelIndexBytes?:number; expect:string; message?:string}
const escapedDigits=Array.from({length:32},(_,c)=>c).filter(c=>![8,9,10,12,13].includes(c)).map(c=>String.fromCharCode(c))
const boundLabel=(kind:string,r:number)=>kind==='issue-252'?'prep_'+'c'.repeat(32)+':recipe:'+(r+1):kind==='max'?'r'.repeat(156)+String(r).padStart(4,'0'):'\u0001'.repeat(157)+escapedDigits[Math.floor(r/729)]!+escapedDigits[Math.floor(r/27)%27]!+escapedDigits[r%27]!
const boundName=(kind:string,f:number)=>kind==='wide'?'w'.repeat(157)+String(f).padStart(3,'0'):'f'+'a'.repeat(61)+String(f).padStart(2,'0')
const boundInventory=(g:NonNullable<BoundCase['inventory']>):DialectResearchRevision[]=>Array.from({length:g.revisions},(_,r)=>({revision:boundLabel(g.label,r),fields:Array.from({length:g.fields},(_,f)=>{const name=boundName(g.name,f);return {name,fieldJson:`{"name":"${name}","description":"r${String(r+1).padStart(3,'0')}","type":"text"}`}})}))
const boundTop=async(g:NonNullable<BoundCase['top']>):Promise<DialectResearchIndex>=>{
 const revisions:DialectIndexRevision[]=Array.from({length:g.revisions},(_,r)=>({revision:boundLabel('escaped',r),fields:g.fields,digest:'sha256:'+'ab'.repeat(32),bytes:g.bytes}))
 return {digest:'sha256:'+await contextDigest(canonicalJSON(revisions)),revisions}
}
describe('the #252 bound proof: every accepted collection fits, by the arithmetic in ADR-0028',()=>{
 it('carries every bound row',()=>{expect(paging.bounds).toHaveLength(6)})
 for(const row of paging.bounds as BoundCase[])it(row.name,async()=>{
  const x=dialectFixture()
  if(row.top){
   const index=await boundTop(row.top)
   expect(utf8Bytes(canonicalJSON(index))).toBe(row.indexBytes)
   x.ports.read=async()=>({...structuredClone(x.state.snapshot),researchIndex:structuredClone(index)})
   x.ports.readFieldIndex=async request=>{x.state.fieldIndexes.push(structuredClone(request));throw Error('the top index was accepted')}
   const tools=dialectTools(x.ports)
   if(row.expect==='top-accepted'){
    expect(row.indexBytes).toBeLessThanOrEqual(L.indexBytes)
    await expect(tools[0]!.execute(fixture.request,signal())).rejects.toThrow('the top index was accepted')
    expect(x.state.fieldIndexes.map(r=>r.entry)).toEqual([0])
    return
   }
   await expect(tools[0]!.execute(fixture.request,signal())).rejects.toThrow(row.message!)
   expect(x.state.fieldIndexes).toHaveLength(0)
   return
  }
  const inventory=boundInventory(row.inventory!),index=(await dialectResearchIndex(inventory))!
  expect(utf8Bytes(canonicalJSON(index))).toBe(row.indexBytes);expect(row.indexBytes).toBeLessThanOrEqual(L.indexBytes)
  expect(Math.max(...index.revisions.map(r=>r.bytes!))).toBe(row.fieldIndexBytes);expect(row.fieldIndexBytes).toBeLessThanOrEqual(L.pageBytes)
  if(row.oneLevelIndexBytes){
   // #250's one-level index of the same inventory, encoded by hand: it exceeded the cap.
   const revisions=await Promise.all(inventory.map(async r=>({revision:r.revision,fields:await Promise.all(r.fields!.map(async f=>({name:f.name,digest:'sha256:'+await contextDigest(f.fieldJson)})))})))
   const oneLevel=utf8Bytes(canonicalJSON({digest:'sha256:'+await contextDigest(canonicalJSON(revisions)),revisions}))
   expect(oneLevel).toBe(row.oneLevelIndexBytes);expect(oneLevel).toBeGreaterThan(L.indexBytes)
  }
  // The host serves the precomputed index, field indexes and flattened declarations; every page it serves is measured.
  const flat=inventory.flatMap(r=>r.fields!.map(f=>({revision:r.revision,name:f.name,fieldJson:f.fieldJson})))
  x.ports.read=async()=>({...structuredClone(x.state.snapshot),researchIndex:structuredClone(index)})
  x.ports.readFieldIndex=async request=>{const page={...request,fieldIndex:(await dialectFieldIndex(inventory[request.entry]!))!};expect(utf8Bytes(canonicalJSON(page))).toBeLessThanOrEqual(L.pageBytes);return page}
  x.ports.readDeclarations=async request=>{const {limit,...binding}=request,page={...binding,declarations:flat.slice(request.cursor,request.cursor+limit)};expect(utf8Bytes(canonicalJSON(page))).toBeLessThanOrEqual(L.pageBytes);expect(limit).toBeLessThanOrEqual(L.pageDeclarations);return page}
  const tools=dialectTools(x.ports)
  const read=body(await tools[0]!.execute(fixture.request,signal()))
  await tools[1]!.execute({...Object.fromEntries(['id','revision','packKey','packDigest','dialectDigest'].map(k=>[k,read[k]])),...fixture.bindings},signal())
  expect(x.state.retained).toHaveLength(1)
  const snapshot=await x.ports.read(fixture.request,signal())
  for(const [value,limit] of [[snapshot,L.snapshotBytes],[x.state.drafts[0]!.candidate,L.candidateBytes],[x.state.drafts[1]!,L.draftBytes],[x.state.retained[0]!,L.runtimeBytes]] as const)expect(utf8Bytes(JSON.stringify(value))).toBeLessThanOrEqual(limit)
  expect(x.state.retained[0]!.researchIndex).toEqual(index)
  expect(await readDialectDeclarations(x.ports,x.state.retained[0]!,index,signal())).toEqual(inventory)
 },60000)
})

it('reproduces the 150 x 30 x 1,000-byte Desk domain that v0.11.0 refused at the snapshot bound',async()=>{
 const inventory:DialectResearchRevision[]=Array.from({length:150},(_,r)=>({revision:`recipe-revision:${String(r+1).padStart(3,'0')}`,fields:Array.from({length:30},(_,f)=>{
  const name=`field_${String(f+1).padStart(2,'0')}`,start=`revision ${String(r+1).padStart(3,'0')} field ${String(f+1).padStart(2,'0')}: `
  const description=start+String.fromCharCode(97+((r+1)*31+f+1)%26).repeat(1000-start.length)
  return {name,fieldJson:JSON.stringify({name,type:'text',required:false,description})}
 })}))
 expect(utf8Bytes(JSON.stringify(inventory))).toBeGreaterThan(L.snapshotBytes)
 const x=dialectFixture();x.state.declarations=inventory
 let served=0
 const pages=x.ports.readDeclarations!
 x.ports.readDeclarations=async(request,s)=>{const page=await pages(request,s);expect(utf8Bytes(canonicalJSON(page))).toBeLessThanOrEqual(L.pageBytes);expect(page.declarations.length).toBeLessThanOrEqual(request.limit);served+=page.declarations.length;return page}
 const tools=dialectTools(x.ports)
 const read=body(await tools[0]!.execute(fixture.request,signal()))
 await tools[1]!.execute({...Object.fromEntries(['id','revision','packKey','packDigest','dialectDigest'].map(k=>[k,read[k]])),...fixture.bindings},signal())
 expect(x.state.retained).toHaveLength(1);expect(served).toBe(2*150*30);expect(x.state.pages.length).toBeGreaterThanOrEqual(10)
 const snapshot=await x.ports.read(fixture.request,signal())
 for(const [value,limit] of [[snapshot,L.snapshotBytes],[x.state.drafts[0]!.candidate,L.candidateBytes],[x.state.drafts[1]!,L.draftBytes],[x.state.retained[0]!,L.runtimeBytes]] as const)expect(utf8Bytes(JSON.stringify(value))).toBeLessThanOrEqual(limit)
 const candidate=x.state.retained[0]!,index=candidate.researchIndex!
 expect(index.revisions).toHaveLength(150);expect(index.revisions[149]!.revision).toBe('recipe-revision:150');expect(index.revisions[0]!.fields).toBe(30)
 expect(await readDialectDeclarations(x.ports,candidate,index,signal())).toEqual(inventory)
})

describe('the paged read tool',()=>{
 const page=async(x:ReturnType<typeof dialectFixture>,cursor:unknown)=>body(await x.tools[0]!.execute({...fixture.request,cursor},signal()))
 it('summarizes the inventory by digest and pages declarations by nextCursor',async()=>{
  const x=dialectFixture();x.state.declarations=structuredClone(retained.researchDeclarations);x.state.pageSize=2
  const shown=await x.read()
  expect(shown).not.toHaveProperty('researchDeclarations')
  expect(shown.researchInventory).toMatchObject({digest:(await dialectResearchIndex(retained.researchDeclarations))!.digest})
  const seen:unknown[]=[];let cursor:unknown=0
  while(cursor!==null){const out=await page(x,cursor);seen.push(...(out.declarations as {name:string;fieldJson:string}[]).map(d=>({name:d.name,fieldJson:d.fieldJson})));cursor=out.nextCursor}
  expect(seen).toEqual(retained.researchDeclarations.flatMap(r=>r.fields??[]))
 })
 for(const [name,message,change,cursor] of [['paging needs a prior read','before paging',undefined,0],['cursor outside the inventory','outside the original declaration inventory',()=>{},1000],['negative cursor','outside the original declaration inventory',()=>{},-1],['null cursor','outside the original declaration inventory',()=>{},null],['fractional cursor','outside the original declaration inventory',()=>{},1.5],['inventory changed since the read','changed; read it again',(x:ReturnType<typeof dialectFixture>)=>{x.state.declarations=x.state.declarations!.slice(0,1)},0]] as const)it('refuses '+name,async()=>{
  const x=dialectFixture();x.state.declarations=structuredClone(retained.researchDeclarations)
  if(change){await x.read();change(x)}
  await expect(page(x,cursor)).rejects.toThrow(message)
 })
})

describe('host refusals',()=>{
 for(const [name,message] of [['missing page port','requires the paged declaration read port'],['index digest differs from its entries','index digest does not match'],['malformed index entry','index is malformed'],['page over its byte bound','page exceeds its byte limit'],['page over its declaration limit','exceeds its declaration bound'],['page from another inventory','another collection revision or inventory'],['page out of index order','broke its order'],['page echoing another cursor','another collection revision or inventory'],['page echoing another id','another collection revision or inventory'],['page echoing another packKey','another collection revision or inventory'],['malformed declaration on a page','actual original field declaration'],['inline v0.11.0 inventory','inline researchDeclarations'],['missing field index port','requires the revision field index read port'],['unavailable entry with a digest','index is malformed'],['field index page over its byte bound','field index page exceeds its byte limit'],['field index page from another inventory','field index page is bound to another collection revision or inventory'],['field index page echoing another entry','field index page is bound to another collection revision or inventory'],['field index differs from its top entry','field index does not match its indexed digest'],['field index with another field count','revision field index is malformed'],['field index for another revision','revision field index is malformed'],['malformed field index page','field index page is malformed'],['empty revision with another field index digest','field index does not match its indexed digest']] as const)it(name,async()=>{
  const x=dialectFixture();x.state.declarations=structuredClone(retained.researchDeclarations)
  if(name==='empty revision with another field index digest')x.state.declarations!.push({revision:'original:empty',fields:[]})
  if(name==='malformed declaration on a page')x.state.declarations=[{revision:'original',fields:[{name:'typed',fieldJson:'{"name":"other","type":"text"}'}]}]
  const read=x.ports.read,fieldIndexes=x.ports.readFieldIndex!,pages=x.ports.readDeclarations!
  x.ports.read=async(r,s)=>{const snapshot=await read(r,s),top=snapshot.researchIndex!.revisions
   if(name==='index digest differs from its entries')snapshot.researchIndex!.digest='sha256:'+await contextDigest('another inventory')
   if(name==='malformed index entry')top[0]!.digest='sha256:short'
   if(name==='inline v0.11.0 inventory')Object.assign(snapshot,{researchDeclarations:x.state.declarations})
   if(name==='unavailable entry with a digest')top[0]!.fields=null
   // The host's top entry and its digest agree, but the count or label is not the field index's.
   if(name==='field index with another field count')top[0]!.fields!+=1
   if(name==='field index for another revision'){const other=canonicalJSON({...(await dialectFieldIndex(x.state.declarations![0]!))!,revision:'another revision'});top[0]!.digest='sha256:'+await contextDigest(other);top[0]!.bytes=utf8Bytes(other)}
   if(name==='empty revision with another field index digest')top.at(-1)!.digest='sha256:'+await contextDigest('another field index')
   if(['field index with another field count','field index for another revision','unavailable entry with a digest','empty revision with another field index digest'].includes(name))snapshot.researchIndex!.digest='sha256:'+await contextDigest(canonicalJSON(top))
   return snapshot}
  x.ports.readFieldIndex=async(r,s)=>{const p=await fieldIndexes(r,s)
   if(name==='field index page over its byte bound')p.fieldIndex.fields[0]!.name+=' '.repeat(L.pageBytes)
   if(name==='field index page from another inventory')p.inventoryDigest='sha256:'+await contextDigest('another inventory')
   if(name==='field index page echoing another entry')p.entry+=1
   if(name==='field index differs from its top entry')p.fieldIndex.fields[0]!.digest='sha256:'+await contextDigest('another declaration')
   if(name==='field index for another revision')p.fieldIndex.revision='another revision'
   if(name==='malformed field index page')Object.assign(p,{extra:true})
   return p}
  x.ports.readDeclarations=async(r,s)=>{const p=await pages(r,s)
   if(name==='page over its byte bound')p.declarations[0]!.fieldJson+=' '.repeat(L.pageBytes)
   if(name==='page over its declaration limit')p.declarations.push(p.declarations[0]!)
   if(name==='page from another inventory')p.inventoryDigest='sha256:'+await contextDigest('another inventory')
   if(name==='page echoing another cursor')p.cursor+=1
   if(name==='page echoing another id')p.id='collection:opaque/other'
   if(name==='page echoing another packKey')p.packKey='declared:other'
   if(name==='page out of index order')[p.declarations[0],p.declarations[1]]=[p.declarations[1]!,p.declarations[0]!]
   return p}
  if(name==='missing page port')delete x.ports.readDeclarations
  if(name==='missing field index port')delete x.ports.readFieldIndex
  await expect(dialectTools(x.ports)[0]!.execute(fixture.request,signal())).rejects.toThrow(message)
 })
})
it('refuses a proposal when the inventory changed since its read',async()=>{
 const x=dialectFixture();x.state.declarations=structuredClone(retained.researchDeclarations)
 const read=await x.read();x.state.declarations=x.state.declarations!.slice(0,1)
 await expect(x.propose(read)).rejects.toThrow('original declaration inventory changed; read it again before proposing')
 expect(x.state.drafts).toHaveLength(0);expect(x.state.retained).toHaveLength(0)
})
it('answers only the checked bytes when a host accessor changes a declaration after its first reads',async()=>{
 const x=dialectFixture();x.state.declarations=structuredClone(retained.researchDeclarations)
 const pages=x.ports.readDeclarations!
 x.ports.readDeclarations=async(r,s)=>{const p=await pages(r,s),real=p.declarations[0]!.fieldJson;let reads=0
  Object.defineProperty(p.declarations[0]!,'fieldJson',{enumerable:true,get:()=>++reads>4?'{"name":"UNCHECKED","type":"text"}':real})
  return p}
 const tools=dialectTools(x.ports)
 await tools[0]!.execute(fixture.request,signal())
 const out=body(await tools[0]!.execute({...fixture.request,cursor:0},signal()))
 const first=(out.declarations as {fieldJson:string}[])[0]!
 expect(first.fieldJson).toBe(retained.researchDeclarations.flatMap(r=>r.fields??[])[0]!.fieldJson)
 expect(JSON.stringify(out)).not.toContain('UNCHECKED')
})
