import {describe,it,expect} from 'vitest'
import reportVectors from '../../../contract/vectors/usage-budget/report.json'
import routeVectors from '../../../contract/fixtures/turn-route/decision.json'
import {decideTurnRoute,type RoutingPolicy,type RouteCatalogue,type RouteFacts,type RouteSelection,type RouteClassifierRecord} from '../turnRoute'
import currentReport from '../contract/run-report-v2.json'
const v2=reportVectors.items[0]!
import reports from '../../../contract/vectors/report/reports.json'
import {canonicalJSON} from '../context/chat'
import {readRunReport,canonicalRunReport,RunReportInvalid,RunReportOverflow} from '../runReport'
describe('version-discriminated closed usage report',()=>{
 it('retains published v1 bytes and reads handwritten feature v2 as v2',()=>{
  expect(canonicalRunReport(readRunReport(reports.items[0]))).toBe(canonicalJSON(reports.items[0]))
  for(const input of [v2,canonicalJSON(v2),new TextEncoder().encode(canonicalJSON(v2))]){
   const held=readRunReport(input)
   expect(held.version).toBe(2)
   if(held.version!==2)throw Error('missing report version discrimination')
   expect(held.usageTotals!.inputTokens).toEqual({known:4,complete:false})
   expect(canonicalRunReport(held)).toBe(canonicalJSON(v2))
  }
 })
 it('reads current report2 with no usage section as absent, without inventing zeros',()=>{const {usageTotals:_,...without}=v2;expect(canonicalRunReport(readRunReport(without))).toBe(canonicalJSON(without));expect(Object.hasOwn(readRunReport(without),'usageTotals')).toBe(false)})
 it('refuses future versions, unknown fields, undefined totals and unsafe exact numbers',()=>{
  for(const bad of [{...v2,version:3},{...v2,extension:{}},{...v2,usageTotals:undefined},{...v2,usageTotals:{...v2.usageTotals,inputTokens:{known:-1,complete:true}}},{...v2,usageTotals:{...v2.usageTotals,inputTokens:{known:4,complete:true,extra:0}}},{...v2,usageTotals:{...v2.usageTotals,inputTokens:{known:4}}}])expect(()=>readRunReport(bad)).toThrow(RunReportInvalid)
  const raw=canonicalJSON(v2)
  for(const bad of [raw.replace('"version":2','"version":2,"version":2'),raw.replace('"known":4','"known":9007199254740991.1'),raw.replace('"known":4','"known":9007199254740992'),raw.replace('"known":4','"known":1e-999')])expect(()=>readRunReport(bad)).toThrow(RunReportInvalid)
  expect(()=>readRunReport('['.repeat(66)+'0'+']'.repeat(66))).toThrow(RunReportOverflow)
 })
})

it('current report date guard accepts fractions and refuses trailing line terminators',()=>{
 const pattern=new RegExp(currentReport.definitions['memory.json#'].properties.createdAt.pattern)
 for(const value of ['2026-10-25T12:00:00Z','2026-10-25T12:00:00.000Z','2026-10-25T12:00:00+01:00'])expect(pattern.test(value)).toBe(true)
 for(const value of ['2026-10-25T12:00:00Z\n','2026-10-25T12:00:00.000Z\r\n','2026-10-25T12:00:00Z\u2028','2026-10-25T12:00:00Zsuffix'])expect(pattern.test(value)).toBe(false)
})

it('retains a closed actual routing decision and refuses union and cross-field violations',()=>{
 const input=routeVectors.cases[0]!.input as unknown as {policy:RoutingPolicy;catalogue:RouteCatalogue;facts:RouteFacts;binding:unknown;host?:RouteSelection;classifier?:RouteClassifierRecord},route=decideTurnRoute(input.policy,input.catalogue,input.facts,input.binding,input.host,input.classifier),report={...v2,route}
 expect(canonicalRunReport(readRunReport(report))).toBe(canonicalJSON(report))
 for(const invalid of [{...route,tools:{}},{...route,operations:3},{...route,widened:false,widenedFor:'undeclared'},{...route,unknown:true},{...route,reason:'bad\nlabel'}])expect(()=>readRunReport({...v2,route:invalid})).toThrow(RunReportInvalid)
})
