import {describe,it,expect} from 'vitest'
import vectors from '../../contract/testdata/taxonomy/gateway-v2.json'
import catalogue from '../../contract/testdata/taxonomy/gateway-catalog-v5.json'
import {readDescriptorSnapshot,descriptorSnapshotDigest,descriptorApprovalProblem,type DescriptorApproval} from './descriptorSnapshot'
import {declareSources} from './toolSources'
import {summarizeTaxonomies,taxonomyFromMcpMetadata,type ToolTaxonomy} from './toolTaxonomy'
import type {HostTool} from './engine'
const bytes=(s:string)=>Uint8Array.from(new TextEncoder().encode(s))
const approval=():DescriptorApproval=>({platform:vectors.platform,binding:vectors.binding,operation:'live',endpoint:vectors.endpoint,toolTaxonomies:structuredClone(vectors.tools) as Record<string,ToolTaxonomy>})
const tool=():HostTool=>({name:'write',taxonomy:structuredClone(vectors.tools.write) as ToolTaxonomy,description:'host constraint',inputSchema:{type:'object'},execute:async()=>({content:[]}),source:{kind:'mcp',id:'tickets',trust:'connected',toolListDigest:vectors.snapshotDigest,descriptorSnapshot:bytes(vectors.snapshotText),descriptorApproval:approval(),currentDescriptorSnapshot:()=>bytes(vectors.snapshotText),verifyReceipt:()=>false}})
describe('paired Gateway taxonomy current admission',()=>{
 it('reads exact shared V2 bytes, owner identity and every refusal',async()=>{
  const snapshot=readDescriptorSnapshot(bytes(vectors.snapshotText))
  expect(await descriptorSnapshotDigest(bytes(vectors.snapshotText))).toBe(vectors.snapshotDigest)
  expect(descriptorApprovalProblem(snapshot,approval(),'search',vectors.tools.search as ToolTaxonomy)).toBe('')
  for(const row of vectors.refusals){
   const changed=vectors.snapshotText.replace(row.find,row.replace)
   let refused=false
   try{refused=descriptorApprovalProblem(readDescriptorSnapshot(bytes(changed)),approval())!==''}catch{refused=true}
   expect(refused,row.name).toBe(true)
  }
  for(const key of ['platform','binding','operation','endpoint','toolTaxonomies'] as const){
   const wrong=approval()
   if(key==='toolTaxonomies')delete wrong.toolTaxonomies.write
   else Object.assign(wrong,{[key]:key==='operation'?'write':'wrong'})
   expect(descriptorApprovalProblem(snapshot,wrong),key).not.toBe('')
  }
 })
 it('admits only a matching selected leaf under a separately supplied complete approval',async()=>{
  const t=tool(),out=await declareSources([t],[])
  expect(out.hostTools).toHaveLength(1)
  expect(out.hostTools[0]!.taxonomy).toEqual(vectors.tools.write)
  for(const changed of [{...tool(),taxonomy:{operation:'read',effect:'read-external'}},{...tool(),name:'not-approved'}])await expect(declareSources([changed as HostTool],[])).rejects.toThrow('descriptor snapshot')
  const missing=tool();delete missing.source!.descriptorApproval
  await expect(declareSources([missing],[])).rejects.toThrow('independent approval')
  const historical=tool();historical.source!.descriptorSnapshot=bytes('{"binding":"'+vectors.binding+'","capturedAt":"2026-10-25T00:00:00Z","platform":"tickets","policy":1,"tools":{"search":{"description":"Old"}}}')
  expect(()=>readDescriptorSnapshot(historical.source!.descriptorSnapshot!)).not.toThrow()
  await expect(declareSources([historical],[])).rejects.toThrow('descriptor snapshot')
 })
 it('derives complete strongest profiles independent of child order and maps flat metadata',()=>{
  for(const row of vectors.groups){
   const map=new Map(Object.entries(row.tools) as [string,ToolTaxonomy][])
   for(const ordered of [map,new Map([...map].reverse())])expect(summarizeTaxonomies(ordered)).toEqual({taxonomy:row.taxonomy,profile:{effect:row.taxonomy.effect,operations:row.operations}})
  }
  expect(summarizeTaxonomies(new Map(Object.entries(catalogue.status.components) as [string,ToolTaxonomy][]))).toEqual({taxonomy:catalogue.status.taxonomy,profile:catalogue.status.profile})
  expect(taxonomyFromMcpMetadata({operation:'evaluate',effect:'act',experimental:true,transportNote:'untrusted'},true)).toEqual({operation:'evaluate',effect:'act',experimental:true})
  expect(()=>taxonomyFromMcpMetadata({taxonomy:{operation:'read',effect:'read'}})).toThrow()
 })
})
