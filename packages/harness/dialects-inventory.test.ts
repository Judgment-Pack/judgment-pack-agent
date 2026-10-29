import {describe,expect,it} from 'vitest'
import paging from '../../contract/vectors/dialect-authoring/paging.json'
import retained from '../../contract/testdata/dialect-authoring/retained-policy.json'
import fixture from '../../contract/testdata/dialect-authoring/authoring.json'
import {DIALECT_AUTHORING_LIMITS as L,dialectResearchIndex,dialectTools,readDialectDeclarations,type DialectDeclarationPage,type DialectDeclarationPageRequest,type DialectResearchIndex,type DialectResearchRevision} from './dialects'
import {dialectFixture} from './testing/dialects'
import {canonicalJSON} from './context/chat'
import {contextDigest} from './context/ledger'
import {utf8Bytes} from './context/policy'
import type {McpToolResult} from './engine'

// #243: the complete original inventory travels by digest and its declarations through read-port pages.
const signal=()=>new AbortController().signal
const body=(r:McpToolResult)=>JSON.parse(r.content![0]!.text!) as Record<string,unknown>
const items=paging.items as DialectDeclarationPage[]
interface PagingCase {name:string; pages?:number[]; expect:string; message?:string; requests?:DialectDeclarationPageRequest[]; changeDuringPreview?:boolean; generate?:{revisions:number; fields:number; nameBytes:number}; indexBytes?:number}
const cases=paging.cases as PagingCase[]
const generated=(revisions:number,fields:number,nameBytes:number):DialectResearchRevision[]=>Array.from({length:revisions},(_,i)=>({revision:'revision-'+String(i).padStart(nameBytes-9,'0'),fields:Array.from({length:fields},(_,j)=>{const name='field-'+String(j).padStart(nameBytes-6,'0');return {name,fieldJson:`{"name":"${name}","type":"text"}`}})}))

describe('paging vectors through the exported tools',()=>{
 it('the package index equals the handwritten digests',async()=>{expect(await dialectResearchIndex(paging.inventory as DialectResearchRevision[])).toEqual(paging.index)})
 for(const row of cases)it(row.name,async()=>{
  const x=dialectFixture()
  let current=paging.index as DialectResearchIndex
  if(row.generate){
   const index=(await dialectResearchIndex(generated(row.generate.revisions,row.generate.fields,row.generate.nameBytes)))!
   expect(utf8Bytes(canonicalJSON(index))).toBe(row.indexBytes);expect(row.indexBytes).toBeGreaterThan(L.indexBytes)
   current=index
  }
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
   const candidate=x.state.retained[0]!
   expect(await readDialectDeclarations(x.ports.readDeclarations,candidate,candidate.researchIndex,signal())).toEqual(paging.inventory)
   return
  }
  await expect(run()).rejects.toThrow(row.message!)
  expect(x.state.retained).toHaveLength(0)
  if(row.changeDuringPreview){expect(x.state.drafts).toHaveLength(2);expect(x.state.previewCandidate).toBeDefined()}
  else expect(x.state.drafts).toHaveLength(0)
 })
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
 expect(index.revisions).toHaveLength(150);expect(index.revisions[149]!.revision).toBe('recipe-revision:150');expect(index.revisions[0]!.fields!.map(f=>f.name).at(-1)).toBe('field_30')
 expect(await readDialectDeclarations(x.ports.readDeclarations,candidate,index,signal())).toEqual(inventory)
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
 for(const [name,message] of [['missing page port','requires the paged declaration read port'],['index digest differs from its entries','index digest does not match'],['malformed index entry','index is malformed'],['page over its byte bound','page exceeds its byte limit'],['page over its declaration limit','exceeds its declaration bound'],['page from another inventory','another collection revision or inventory'],['page out of index order','broke its order'],['page echoing another cursor','another collection revision or inventory'],['page echoing another id','another collection revision or inventory'],['page echoing another packKey','another collection revision or inventory'],['malformed declaration on a page','actual original field declaration'],['inline v0.11.0 inventory','inline researchDeclarations']] as const)it(name,async()=>{
  const x=dialectFixture();x.state.declarations=structuredClone(retained.researchDeclarations)
  if(name==='malformed declaration on a page')x.state.declarations=[{revision:'original',fields:[{name:'typed',fieldJson:'{"name":"other","type":"text"}'}]}]
  const read=x.ports.read,pages=x.ports.readDeclarations!
  x.ports.read=async(r,s)=>{const snapshot=await read(r,s)
   if(name==='index digest differs from its entries')snapshot.researchIndex!.digest='sha256:'+await contextDigest('another inventory')
   if(name==='malformed index entry')snapshot.researchIndex!.revisions[0]!.fields![0]!.digest='sha256:short'
   if(name==='inline v0.11.0 inventory')Object.assign(snapshot,{researchDeclarations:x.state.declarations})
   return snapshot}
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
  await expect(dialectTools(x.ports)[0]!.execute(fixture.request,signal())).rejects.toThrow(message)
 })
 it('an index over its bound inside the snapshot bound',async()=>{
  const x=dialectFixture();x.state.declarations=generated(9,1000,160)
  expect(utf8Bytes(JSON.stringify(await x.ports.read(fixture.request,signal())))).toBeLessThanOrEqual(L.snapshotBytes)
  await expect(x.read()).rejects.toThrow('index exceeds its byte limit')
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
