import {describe,it,expect} from 'vitest'
import {readFileSync,mkdirSync,mkdtempSync,writeFileSync} from 'node:fs'
import {execFileSync} from 'node:child_process'
import {tmpdir} from 'node:os'
import {join,resolve} from 'node:path'
import discriminator from '../../contract/fixtures/record-versions/report-discriminator.json'
import reports from '../../contract/vectors/report/reports.json'
import retained from '../../contract/vectors/report/retained.json'
import plans from '../../contract/vectors/plan/extended-checkpoints.json'
import simplePlans from '../../contract/vectors/plan/checkpoints.json'
import {canonicalJSON} from './context/chat'
import {readRunReport,canonicalRunReport,RunReportInvalid,RunReportOverflow,MAX_RUN_REPORT_BYTES} from './runReport'
import {projectRunReport} from './runReport'
import {readExecution} from './execution/state'

describe('strict retained run report',()=>{
 it('reads handwritten reports unchanged, without checkpoint internals or authority',()=>{
  for(const value of [...reports.items,...retained.items]){
   const before=canonicalJSON(value)
   for(const input of [value,before,new TextEncoder().encode(before)]){
    const actual=readRunReport(input)
    expect(canonicalRunReport(actual)).toBe(before)
    expect(actual).not.toBe(value)
   }
   expect(canonicalJSON(value)).toBe(before)
  }
  const held=readRunReport(reports.items[1]);held.usage!.rows[0]!.tokens!.inputTokens=17
  expect(reports.items[1]!.usage!.rows[0]!.tokens!.inputTokens).toBe(0)
  expect(readRunReport(reports.items[0]).usage).toBeUndefined()
  expect(readRunReport(reports.items[2]).usage).toEqual({rows:[]})
 })
 it('round trips real terminal projections and every handwritten Plan/evidence history',()=>{
  for(const fixture of [...simplePlans.items,...plans.items]){
   const checkpoint=readExecution({...fixture,status:'stopped'})!
   const projected=projectRunReport(checkpoint),before=canonicalRunReport(projected)
   expect(canonicalRunReport(readRunReport(before))).toBe(before)
   expect(readRunReport(before).plan).toEqual(checkpoint.plan)
   expect(readRunReport(before).planState).toEqual(checkpoint.planState)
  }
 })
 for(const row of retained.invalid)it(`refuses shared ${row.name}`,()=>{
  const value:Record<string,unknown>=structuredClone('base' in row?retained.items[row.base!]!:reports.items[1]!) as unknown as Record<string,unknown>
  let target:any=value
  for(const key of row.path.slice(0,-1))target=target[key]
  const key=row.path.at(-1)!
  if('remove' in row)delete target[key];else target[key]=structuredClone(row.value)
  expect(()=>readRunReport(value)).toThrow(RunReportInvalid)
  expect(()=>readRunReport(JSON.stringify(value))).toThrow(RunReportInvalid)
 })
 it('refuses every required root member and non-data objects without invoking accessors',()=>{
  for(const name of Object.keys(reports.items[0])){const v:any={...reports.items[0]};delete v[name];expect(()=>readRunReport(v)).toThrow(RunReportInvalid)}
  let invoked=0
  const getter=Object.defineProperty({...reports.items[0]},'usage',{enumerable:true,get(){invoked++;throw Error('secret')}})
  const hidden=Object.defineProperty({...reports.items[0]},'secret',{value:1})
  const cycle:any={...reports.items[0]};cycle.cycle=cycle
  const sparse:any={...reports.items[0],plan:new Array(1)}
  const symbol={...reports.items[0],[Symbol('extra')]:1}
  const executable={...reports.items[0],toJSON(){invoked++;return reports.items[0]}}
  for(const value of [getter,hidden,cycle,sparse,symbol,executable,new Date(),new Map(),undefined,null,[],{...reports.items[0],extra:undefined},{...reports.items[0],plan:Object.assign([],{extra:1})},{...reports.items[0],explanation:'\ud800'},Object.assign(Object.create({}),reports.items[0])])expect(()=>readRunReport(value)).toThrow(RunReportInvalid)
  expect(invoked).toBe(0)
 })
 it('detects malformed bytes, duplicate names and number spelling before rounding',()=>{
  const good=canonicalJSON(retained.items[1])
  const spellings=['9007199254740991.1','9007199254740992','9.0071992547409911e15','1e999','1e-999','-1','NaN','Infinity']
  for(const spelling of spellings)expect(()=>readRunReport(good.replace('9007199254740991',spelling))).toThrow(RunReportInvalid)
  for(const suffix of ['', 'garbage'])if(suffix)expect(()=>readRunReport(good+suffix)).toThrow(RunReportInvalid)
  for(const input of ['','{','null','[]',good.replace('"version":1','"version":1,"version":1'),good.replace('"inputTokens":9007199254740991','"inputTokens":1,"inputTokens":2'),good.replace('Καλημέρα','\\ud800'),good.replace('Καλημέρα','\\udc00'),good.replace('"version":1','"__proto__":{},"version":1'),'\ufeff'+good,new Uint8Array([255]),new Uint8Array([0xed,0xa0,0x80])])expect(()=>readRunReport(input)).toThrow(RunReportInvalid)
  for(const spelling of ['90071992547409910e-1','9007199254740991.0','9.007199254740991e15'])expect(canonicalRunReport(readRunReport(good.replace('9007199254740991',spelling)))).toBe(good)
  expect(canonicalRunReport(readRunReport(good.replace('🌿','\\ud83c\\udf3f')))).toBe(good)
  expect(canonicalRunReport(readRunReport(good.replace('"outputTokens":0','"outputTokens":0e999999999999999999999999999')))).toBe(good)
 })
 it('bounds bytes and depth before recursive validation; admits large valid node counts',()=>{
  const minimal=canonicalJSON(reports.items[0]),padded=minimal+' '.repeat(MAX_RUN_REPORT_BYTES-minimal.length)
  expect(canonicalRunReport(readRunReport(padded))).toBe(minimal)
  expect(()=>readRunReport(padded+' ')).toThrow(RunReportOverflow)
  expect(()=>readRunReport(new TextEncoder().encode(padded+' '))).toThrow(RunReportOverflow)
  expect(()=>readRunReport({...reports.items[0],explanation:'x'.repeat(MAX_RUN_REPORT_BYTES)})).toThrow(RunReportOverflow)
  expect(()=>readRunReport('['.repeat(66)+'0'+']'.repeat(66))).toThrow(RunReportOverflow)
  let deep:unknown=0;for(let i=0;i<66;i++)deep=[deep];expect(()=>readRunReport(deep)).toThrow(RunReportOverflow)
  const row={source:'m',connection:'c',role:'a',requests:0,invocations:0,milliseconds:0,bytes:0}
  const large={...reports.items[0],childUsage:Array.from({length:250},(_,i)=>({id:`child-${i}`,usage:{rows:Array.from({length:128},()=>({...row}))}}))}
  expect(JSON.stringify(large).length).toBeLessThan(MAX_RUN_REPORT_BYTES)
  expect(readRunReport(large).childUsage).toHaveLength(250)
 })
 it('keeps serializers nonvalidating and mirrors byte-identical frozen v1 schema',()=>{
  expect(canonicalRunReport({version:1,status:'running',extra:true} as never)).toBe('{"extra":true,"status":"running","version":1}')
  const source=readFileSync('../../contract/records/run-report-v1.json','utf8')
  expect(readFileSync('contract/run-report-v1.json','utf8')).toBe(source)
  expect(readFileSync('../../go/context/run_report_v1.json','utf8')).toBe(source)
 })
 it('refuses coordinated widening of every frozen graph copy',()=>{
  const directory=mkdtempSync(join(tmpdir(),'retained-report-graph-'))
  const script=readFileSync(resolve('../../scripts/run-report-shapes.mjs'),'utf8')
  writeFileSync(join(directory,'run-report-shapes.mjs'),script)
  const frozen=JSON.parse(readFileSync('../../contract/records/run-report-v1.json','utf8'))
  frozen.root.properties.future={type:'string'}
  const changed=JSON.stringify(frozen)+'\n'
  for(const path of ['contract/records/run-report-v1.json','packages/harness/contract/run-report-v1.json','go/context/run_report_v1.json']){
   const full=join(directory,path);mkdirSync(full.slice(0,full.lastIndexOf('/')),{recursive:true});writeFileSync(full,changed)
  }
  expect(()=>execFileSync(process.execPath,['run-report-shapes.mjs','--check'],{cwd:directory,stdio:'pipe'})).toThrow('Published report v1 graph digest differs.')
 })

})

// The Go generated union and this byte reader share exact lexical numeric semantics.
describe('exact report version discriminator',()=>{
 for(const row of discriminator.cases)it(row.name,()=>{
  const suffix=JSON.stringify({...discriminator.report,checkpointVersion:row.checkpointVersion}).slice(1)
  const text='{'+(row.token?'"version":'+row.token+',':'')+suffix
  for(const input of [text,new TextEncoder().encode(text)]){
   if(row.version)expect(readRunReport(input).version).toBe(row.version)
   else expect(()=>readRunReport(input)).toThrow(RunReportInvalid)
  }
 })
})
