import {describe,it,expect} from 'vitest'
import vectors from '../../contract/vectors/report/reports.json'
import checkpointSchema from '../../contract/schemas/checkpoint.json'
import reportSchema from '../../contract/schemas/run-report.json'
import planVectors from '../../contract/vectors/plan/checkpoints.json'
import extendedPlanVectors from '../../contract/vectors/plan/extended-checkpoints.json'
import {readExecution,type ExecutionCheckpoint} from './execution/state'
import {canonicalJSON} from './context/chat'
import type {RunHead,RunEventPage} from './storage/run'
import {projectRunReport,buildRunReport,canonicalRunReport,RUN_REPORT_SOURCES,RUN_REPORT_EXCLUDED,RunReportInputInvalid,RunReportPrefixInvalid,RunReportOverflow,MAX_RUN_REPORT_BYTES,type RunReport} from './runReport'
const encode=(value:unknown)=>new TextEncoder().encode(canonicalJSON(value))
function checkpoint(report:unknown):ExecutionCheckpoint {
 const {version:_,checkpointVersion,...facts}=report as RunReport
 return readExecution({...facts,version:checkpointVersion,requestKey:'a'.repeat(64),operations:[],context:{scope:'report-scope',entries:[]}})!
}
function prefix(cp=checkpoint(vectors.items[0])):{cp:ExecutionCheckpoint;head:RunHead;pages:RunEventPage[]} {
 const row=(cursor:string)=>({cursor,bytes:encode({version:1,scope:{execution:cp.id,role:'author'},event:{type:'execution_checkpoint',checkpoint:cp,durable:true}})})
 return {cp,head:{runId:cp.id,revision:'revision',commitId:'commit',cursor:'a',checkpoint:encode(cp)},pages:[{items:[row('z')],nextCursor:'z',hasMore:true},{items:[row('a')],nextCursor:'a',hasMore:false}]}
}
describe('canonical recorded run report',()=>{
 it('matches every handwritten vector and preserves absent, empty, zero and recorded array order',()=>{
  for(const expected of vectors.items){
   const cp=checkpoint(expected),before=canonicalJSON(cp),actual=projectRunReport(cp)
   expect(canonicalRunReport(actual)).toBe(canonicalJSON(expected));expect(canonicalJSON(cp)).toBe(before)
   for(const name of RUN_REPORT_SOURCES)if(Object.hasOwn(cp,name))expect(actual[name]).toEqual(cp[name])
  }
  const rich=projectRunReport(checkpoint(vectors.items[1]));rich.prompts!.reverse()
  expect(checkpoint(vectors.items[1]).prompts![0]!.request).toBe(1)
 })
 it('accounts for every checkpoint member and covers each declared report member',()=>{
  expect(new Set([...RUN_REPORT_SOURCES,...RUN_REPORT_EXCLUDED,'version']).size).toBe(Object.keys(checkpointSchema.properties).length)
  expect([...RUN_REPORT_SOURCES,...RUN_REPORT_EXCLUDED,'version'].sort()).toEqual(Object.keys(checkpointSchema.properties).sort())
  expect(new Set(vectors.items.flatMap(row=>Object.keys(row)))).toEqual(new Set(Object.keys(reportSchema.properties)))
  expect(Object.keys(reportSchema.properties).sort()).toEqual(['version','checkpointVersion',...RUN_REPORT_SOURCES].sort())
 })
 it('carries every whole shared plan record without redefining or summarizing it',()=>{
  for(const fixture of [...planVectors.items,...extendedPlanVectors.items]){
   // Project each shared nested plan state in a terminal checkpoint shell.
   const cp=readExecution({...fixture,status:'stopped'})!,report=projectRunReport(cp)
   expect(report.plan).toEqual(cp.plan);expect(report.planState).toEqual(cp.planState)
  }
 })
 it('accepts opaque nonlexical cursor order and the exact canonical head independent of input key order',()=>{
  const {cp,head,pages}=prefix()
  expect(canonicalRunReport(buildRunReport(cp,head,pages))).toBe(canonicalRunReport(projectRunReport(cp)))
  head.checkpoint=new TextEncoder().encode(JSON.stringify(cp,null,2))
  expect(buildRunReport(cp,head,pages)).toEqual(projectRunReport(cp))
 })
 it('treats supplied pages as corroboration without claiming predecessor or child membership proof',()=>{
  const p=prefix()
  // RunEventPage has no predecessor witness: the caller owns fetching from null.
  expect(buildRunReport(p.cp,p.head,p.pages.slice(1))).toEqual(projectRunReport(p.cp))
  const child={...p.cp,id:'child-not-in-retained-history'}
  p.pages[0]!.items[0]!.bytes=encode({version:1,scope:{execution:child.id,role:'author',task:child.id},event:{type:'execution_checkpoint',checkpoint:child}})
  expect(buildRunReport(p.cp,p.head,p.pages)).toEqual(projectRunReport(p.cp))
 })
 it('refuses incomplete, duplicated, malformed, foreign and mismatched prefix evidence',()=>{
  const mutations:Array<(p:ReturnType<typeof prefix>)=>void>=[
   p=>{p.pages=[]},p=>{p.pages[0]!.hasMore=false},p=>{p.pages[1]!.hasMore=true},
   p=>{p.pages[0]!.nextCursor='wrong'},p=>{p.pages[1]!.items[0]!.cursor='z';p.pages[1]!.nextCursor='z';p.head.cursor='z'},
   p=>{p.head.cursor='later'},p=>{p.head.runId='foreign'},p=>{p.head.revision=''},p=>{p.head.checkpoint=encode({...p.cp,reason:'different'})},
   p=>{p.pages[1]!.items[0]!.bytes=encode({version:1,scope:{execution:'foreign',role:'author'},event:{type:'execution_checkpoint',checkpoint:p.cp}})},
   p=>{p.pages[1]!.items[0]!.bytes=encode({version:1,scope:{execution:p.cp.id,role:'author'},event:{type:'execution_checkpoint',checkpoint:{...p.cp,reason:'different'}}})},
   p=>{for(const page of p.pages)page.items[0]!.bytes=encode({version:1,scope:{execution:p.cp.id,role:'author'},event:{type:'execution_checkpoint',checkpoint:{...p.cp,status:'running'}}})},
   p=>{p.pages[0]!.items[0]!.bytes=encode({version:1,scope:{execution:p.cp.id,role:'author'},event:{type:'unknown'}})},
   p=>{p.pages[0]!.items[0]!.bytes=encode({version:1,scope:{execution:p.cp.id,role:'author'},event:{type:'execution_checkpoint',checkpoint:{...p.cp,used:999}}})},
   p=>{p.pages[0]!.items[0]!.bytes=new Uint8Array([255])},p=>{p.pages[0]!.items=[]},
  ]
  for(const mutate of mutations){const p=prefix();mutate(p);expect(()=>buildRunReport(p.cp,p.head,p.pages)).toThrow(RunReportPrefixInvalid)}
 })
 it('accepts 400 retained events and refuses event-count, byte and report overflows by name',()=>{
  const p=prefix(),bytes=p.pages[0]!.items[0]!.bytes
  const items=Array.from({length:400},(_,i)=>({cursor:`cursor-${i}`,bytes}))
  p.pages=Array.from({length:4},(_,i)=>({items:items.slice(i*100,i*100+100),nextCursor:`cursor-${i*100+99}`,hasMore:i<3}));p.head.cursor='cursor-399'
  expect(buildRunReport(p.cp,p.head,p.pages)).toEqual(projectRunReport(p.cp))
  p.pages[3]!.hasMore=true;p.pages.push({items:[{cursor:'overflow',bytes}],nextCursor:'overflow',hasMore:false});p.head.cursor='overflow'
  expect(()=>buildRunReport(p.cp,p.head,p.pages)).toThrow(RunReportOverflow)
  const big=prefix();big.pages[0]!.items[0]!.bytes=new Uint8Array(MAX_RUN_REPORT_BYTES+1)
  expect(()=>buildRunReport(big.cp,big.head,big.pages)).toThrow(RunReportOverflow)
  expect(()=>canonicalRunReport({...projectRunReport(p.cp),explanation:'x'.repeat(MAX_RUN_REPORT_BYTES)})).toThrow(RunReportOverflow)
 })
 it('refuses malformed and nonterminal checkpoint inputs with a bounded named refusal',()=>{
  for(const value of [undefined,{}, {...checkpoint(vectors.items[0]),status:'running'}, {...checkpoint(vectors.items[0]),status:'paused'}])expect(()=>projectRunReport(value as ExecutionCheckpoint)).toThrow(RunReportInputInvalid)
 })
})
