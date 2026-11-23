// A delegated run's session credential rotates between two requests of one session (spec RFC 0030 §2, The credential:
// a credential lives at most five minutes, and the host obtains a fresh one as the run proceeds). The library sends the
// credential current at each request, holds no earlier one, and its principal and binding stay as they were.
import {afterEach,describe,expect,it,vi} from 'vitest'
import scope from '../../contract/vectors/principal/scope-binding.json'
import {bindModelCall} from './session'
import {configureHarness} from './host'
import {testHost} from './testing/host'
import {openPrincipal,principalBinding} from './principal'

afterEach(()=>{vi.unstubAllGlobals();configureHarness(testHost())})

describe('a rotated session credential',()=>{
 it('sends the credential current at each request, and leaves the principal and its binding as they were',async()=>{
  let current='credential-one',asked=0
  configureHarness({...testHost(),sessionBearer:async()=>{asked++;return current}})
  const sent:string[]=[]
  vi.stubGlobal('fetch',async(_url:unknown,init:RequestInit)=>{sent.push(String((init.headers as Record<string,string>).Authorization));return new Response('{}',{status:200,headers:{'content-type':'application/json'}})})
  const chain=scope.statements.find(s=>s.name==='bound-root')!.chain
  const principal=await openPrincipal({chain:JSON.stringify(chain),keys:scope.keys as never,software:scope.software,audience:scope.audience,clock:()=>Date.parse('2026-10-30T01:00:00.000Z'),admission:()=>({admission:'admit'})})
  const record=structuredClone(principal.record),binding=principalBinding(principal.record)
  const call=bindModelCall('openai-compatible')
  await call('chat/completions',{body:'{}'})
  current='credential-two'
  await call('chat/completions',{body:'{}'})
  expect(sent).toEqual(['Bearer credential-one','Bearer credential-two'])
  expect(asked).toBe(2)
  expect(principal.record).toEqual(record)
  expect(principalBinding(principal.record)).toEqual(binding)
  expect(principal.chain.scopeId).toBe('scope-a')
  expect(JSON.stringify(principal.record)+principal.text).not.toContain('credential-')
 })
})
