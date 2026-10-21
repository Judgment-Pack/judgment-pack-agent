import {fitReport} from './delegation'
import {SUBAGENT_RESULT_MIN_BYTES} from './subagents'
import minimum from '../../contract/vectors/subagents/result-minimum.json'
import {describe,it,expect} from 'vitest'
import {declareSubagents,subagentDigest,subagentRecord,catalogueSubagents,subagentProfile,type SubagentDeclaration} from './subagents'
import {CONTEXT_DEFAULTS} from './context/policy'
import {TASK_PROFILES,readDelegationRequest} from './delegationContract'
import declaration from '../../contract/vectors/subagents/declaration.json'
import request from '../../contract/vectors/subagents/delegation.json'
import type {HostTool} from './engine'
const tool:HostTool={name:'lookup',effect:'read',description:'Read evidence.',inputSchema:{type:'object'},execute:async()=>({content:[]})}
const definition=()=>structuredClone(declaration.items[0]) as SubagentDeclaration
async function admit(s=definition(),tools=[tool],policy=CONTEXT_DEFAULTS){return declareSubagents([s],tools,[],policy)}
describe('declared sub-agents',()=>{
 it('shares the vector report minimum with fitting, including escaped and whitespace summaries',()=>{
  expect(SUBAGENT_RESULT_MIN_BYTES).toBe(minimum.resultBytes)
  expect(new TextEncoder().encode(JSON.stringify(minimum.items[0])).length).toBe(minimum.resultBytes)
  for(const summary of ['Child evidence.'.repeat(50),' '.repeat(100)+'tail','\n'.repeat(100)+'tail','\u0001'.repeat(100),'😀'.repeat(100)]){
   const report={...minimum.items[0]!,summary,confidence:'unstated' as const}
   const {shown}=fitReport(report,0)
   expect(shown.summary.trim()).not.toBe('')
   expect(new TextEncoder().encode(JSON.stringify(shown)).length).toBeLessThanOrEqual(SUBAGENT_RESULT_MIN_BYTES)
  }
 })
 it('withholds resultBytes below the representable report minimum',async()=>{
  for(const resultBytes of [1,112]){
   const s=definition();s.bounds.resultBytes=resultBytes;s.pinnedDigest=await subagentDigest(s)
   const result=await admit(s)
   expect(result.subagents).toEqual([])
   expect(result.withheld[0]).toMatchObject({reason:'subagent withheld',detail:expect.stringContaining(`resultBytes ${resultBytes}`)})
   expect(result.withheld[0]?.detail).toContain('minimum 113')
  }
  const s=definition();s.bounds.resultBytes=113;s.pinnedDigest=await subagentDigest(s)
  expect((await admit(s)).subagents).toHaveLength(1)
 })
 it('matches the hand-authored digest and carries the approved source record',async()=>{
  const s=definition();expect(await subagentDigest(s)).toBe(s.pinnedDigest)
  const {subagents,withheld}=await admit(s);expect(withheld).toEqual([])
  expect(subagentRecord(subagents[0]!)).toEqual(request.items[0]!.subagent)
  expect(await subagentDigest(subagents[0]!)).toBe(s.pinnedDigest)
  expect(subagentProfile(subagents[0]!)).toMatchObject({id:s.id,capabilities:[],budget:{modelRequests:3,toolCalls:2,milliseconds:30000}})
 })
 it('withholds an undeclared tool or skill and any tool that is not a read',async()=>{
  expect((await admit(definition(),[])).withheld[0]?.detail).toContain('Undeclared tool')
  for(const effect of ['workflow','plan'] as const)expect((await admit(definition(),[{...tool,effect}])).subagents).toEqual([])
  const s=definition();s.skills=['missing'];s.pinnedDigest=await subagentDigest(s)
  expect((await admit(s)).withheld[0]?.detail).toContain('undeclared skill')
 })
 it('holds every bounds ceiling to the turn and refuses task overrides',async()=>{
  for(const [key,value] of Object.entries({modelRequests:11,agentRuns:6,toolCalls:13,bytes:180001,milliseconds:360001,resultBytes:12001})){
   const s=definition();Object.assign(s.bounds,{[key]:value});s.pinnedDigest=await subagentDigest(s)
   expect((await admit(s)).withheld[0]?.detail).toContain('ceiling')
  }
  expect((await admit(definition(),[tool],{...CONTEXT_DEFAULTS,maxInputBytes:10000})).subagents).toEqual([])
  expect(()=>readDelegationRequest({tasks:[{title:'Evidence',objective:'Check.',profile:'auditor',bounds:{bytes:999999}}]})).toThrow()
 })
 it('withholds a changed digest until re-approval',async()=>{
  const s=definition();s.instructions+=' Changed.'
  expect((await admit(s)).withheld[0]?.detail).toContain('re-approval')
  s.pinnedDigest=await subagentDigest(s);expect((await admit(s)).subagents).toHaveLength(1)
 })
 it('declarations cannot alter the built-ins',async()=>{
  const before=JSON.stringify(TASK_PROFILES)
  for(const profile of TASK_PROFILES){const s=definition();s.id=profile.id;s.pinnedDigest=await subagentDigest(s);expect((await admit(s)).subagents).toEqual([])}
  expect(JSON.stringify(TASK_PROFILES)).toBe(before)
 })
 it('catalogues framed purpose lines under both bounds',async()=>{
  const {subagents}=await admit();const s=subagents[0]!
  const c=catalogueSubagents([s,{...s,id:'second'}],{cataloguedSubagents:1,subagentCatalogueBytes:4000},'marker')
  expect(c.subagents).toHaveLength(1);expect(c.withheld).toHaveLength(1);expect(c.section).toContain('marker');expect(c.section).toContain(s.purpose)
  expect(catalogueSubagents([s],{cataloguedSubagents:16,subagentCatalogueBytes:1},'marker').subagents).toEqual([])
 })
 it('reads definitions closed and rejects nesting',async()=>{
  await expect(declareSubagents([{...definition(),subagents:[]}],[tool],[],CONTEXT_DEFAULTS)).rejects.toThrow('closed')
 })
})
