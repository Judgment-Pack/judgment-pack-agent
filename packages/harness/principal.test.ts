// @vitest-environment node
import {describe,it,expect,vi} from 'vitest'
import {createHash} from 'node:crypto'
import {readFileSync} from 'node:fs'
import citation from '../../contract/testdata/principal/citation.json'
import chains from '../../contract/vectors/principal/chains.json'
import admissions from '../../contract/vectors/principal/admission.json'
import records from '../../contract/vectors/principal/record.json'
import answers from '../../contract/vectors/principal/answer.json'
import principalEvents from '../../contract/vectors/principal/events.json'
import reports from '../../contract/vectors/principal/report.json'
import {readRunEvent} from './recordReader'
import {readRunReport} from './runReportReader'
import {verifyChain,pinKeys,keyProblem,openPrincipal,admitCall,validPrincipalRecord,DelegationRefused,type AdmissionRead,type PrincipalCall} from './principal'

const gatewayText=readFileSync(new URL('../../contract/testdata/principal/gateway-statements.json',import.meta.url))
const gateway=JSON.parse(gatewayText.toString('utf8')) as {keys:Record<string,string[]>;vectors:{name:string;chain:unknown;expect:string}[]}
const outcome=async(text:string,keys:Record<string,string[]>)=>{try{await verifyChain(text,await pinKeys(keys));return 'ok'}catch(error){if(error instanceof DelegationRefused)return error.refusal.name;throw error}}

describe('the gateway statement vectors, cited (RFC 0030 steps 1 to 7)',()=>{
 it('reads the copy the citation names, byte for byte',()=>{
  expect(createHash('sha256').update(gatewayText).digest('hex')).toBe(citation.sha256)
  expect(citation.commit).toMatch(/^[0-9a-f]{40}$/)
 })
 for(const vector of gateway.vectors)it(vector.name,async()=>{
  expect(await outcome(JSON.stringify(vector.chain),gateway.keys)).toBe(vector.expect)
 })
})

describe('the key rule (the gateway SPEC.md §8.4)',()=>{
 it('refuses each small-order point, a non-canonical encoding and a point off the curve',()=>{
  expect(keyProblem('0000000000000000000000000000000000000000000000000000000000000000')).toBe('key-small-order')
  expect(keyProblem('edffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff7f')).toBe('key-not-canonical')
  expect(keyProblem('0100000000000000000000000000000000000000000000000000000000000080')).toBe('key-not-canonical')
  expect(keyProblem('0200000000000000000000000000000000000000000000000000000000000000')).toBe('key-not-on-curve')
  expect(keyProblem(gateway.keys.desk![0]!)).toBeUndefined()
 })
})

const chainOf=(name:string)=>JSON.stringify(name==='child'?chains.items[1]:chains.items[0])
const opened=(name:string,now:string|null,answer:unknown,reads:AdmissionRead[])=>openPrincipal({chain:chainOf(name),keys:chains.keys,software:chains.software,audience:chains.audience,clock:()=>now===null?Number.NaN:Date.parse(now),admission:read=>{reads.push(read);if(answer==='throw')throw Error('port down');return answer as never}})

describe('the agent host chains (handwritten, independently signed)',()=>{
 it('admits each chain, its record naming the leaf and every digest',async()=>{
  const root=await openPrincipal({chain:chainOf('root'),keys:chains.keys,software:chains.software,audience:'desk',admission:()=>({admission:'admit'})})
  expect(root.record).toEqual({actor:{agent:'b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1',host:'desk',software:chains.software},rootActor:'b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1',subject:chains.items[0]![0]!.sub,delegation:{id:'33333333333333333333333333333333',digest:chains.digests.root},chain:[chains.digests.root]})
  const child=await openPrincipal({chain:chainOf('child'),keys:chains.keys,software:chains.software,audience:'desk',admission:()=>({admission:'admit'})})
  expect(child.record.delegation).toEqual({id:'44444444444444444444444444444444',digest:chains.digests.child})
  expect(child.record.actor.agent).toBe('b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2')
  expect(child.record.rootActor).toBe('b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1')
  expect(child.record.chain).toEqual([chains.digests.root,chains.digests.child])
 })
 for(const refused of chains.refused)it(refused.problem,async()=>{
  expect(await outcome(JSON.stringify(refused.chain),chains.keys)).toBe(refused.refusal)
 })
 it("refuses a leaf whose software is not the run's",async()=>{
  await expect(openPrincipal({chain:chainOf('root'),keys:chains.keys,software:{package:'@judgment-pack/harness',version:'0.12.0'},audience:'desk',admission:()=>({admission:'admit'})})).rejects.toMatchObject({refusal:{name:'software-mismatch'}})
 })
})

describe('admission before dispatch (handwritten cases)',()=>{
 for(const c of admissions.cases)it(c.name,async()=>{
  const reads:AdmissionRead[]=[]
  const principal=await opened(c.chain,c.now,c.answer,reads)
  const got=await admitCall(principal,c.call as PrincipalCall,c.count)
  expect(got??'admit').toEqual(c.expect)
  expect(reads).toEqual(c.read===null?[]:[admissions.items[c.read]])
 })
 it('refuses a port that never answers once the admission time has passed',async()=>{
  vi.useFakeTimers()
  try{
   const principal=await openPrincipal({chain:chainOf('root'),keys:chains.keys,software:chains.software,audience:'desk',clock:()=>Date.parse('2026-10-30T01:00:00.000Z'),admission:()=>new Promise(()=>{})})
   const pending=admitCall(principal,{audience:{kind:'agent-host',name:'desk'},source:'notes',tool:'read_note',effect:'read'},true)
   await vi.advanceTimersByTimeAsync(5000)
   expect(await pending).toEqual({name:'admission-unknown'})
  }finally{vi.useRealTimers()}
 })
})

describe('the record members (handwritten vectors, read by both languages)',()=>{
 it('reads every record and refuses each malformed one',()=>{
  for(const item of records.items)expect(validPrincipalRecord(structuredClone(item))).toBe(true)
  for(const row of records.refused)expect(validPrincipalRecord(structuredClone(row.record)),row.problem).toBe(false)
 })
 it("holds the port's answer to its three forms",async()=>{
  for(const [answer,admitted] of [...answers.items.map(a=>[a,true] as const),...answers.refused.map(a=>[a,false] as const)]){
   const principal=await openPrincipal({chain:JSON.stringify(chains.items[0]),keys:chains.keys,software:chains.software,audience:'desk',clock:()=>Date.parse('2026-10-30T01:00:00.000Z'),admission:()=>answer as never})
   const got=await admitCall(principal,{audience:{kind:'agent-host',name:'desk'},source:'notes',tool:'read_note',effect:'read'},true)
   if(admitted)expect(got?.name).not.toBe('admission-unknown')
   else expect(got).toEqual({name:'admission-unknown'})
  }
 })
 it('reads the event 3 records',()=>{
  for(const event of principalEvents.items)expect(readRunEvent(structuredClone(event))).toEqual(event)
 })
 it('reads the report 3 and refuses each malformed one',()=>{
  for(const report of reports.items)expect(readRunReport(JSON.stringify(report))).toEqual(report)
  for(const row of reports.refused)expect(()=>readRunReport(JSON.stringify(row.report)),row.problem).toThrow()
 })
})
