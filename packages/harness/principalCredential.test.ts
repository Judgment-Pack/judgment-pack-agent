// A delegated run's session credential rotates between two requests of one session (spec RFC 0030 §2, The credential:
// a credential lives at most five minutes, and the host obtains a fresh one as the run proceeds). The library sends the
// credential current at each request and holds no earlier one.
import {afterEach,describe,expect,it,vi} from 'vitest'
import {bindModelCall} from './session'
import {configureHarness} from './host'
import {testHost} from './testing/host'

afterEach(()=>{vi.unstubAllGlobals();configureHarness(testHost())})

describe('a rotated session credential',()=>{
 it('sends the credential current at each request, and no earlier one',async()=>{
  let current='credential-one',asked=0
  configureHarness({...testHost(),sessionBearer:async()=>{asked++;return current}})
  const sent:string[]=[]
  vi.stubGlobal('fetch',async(_url:unknown,init:RequestInit)=>{sent.push(String((init.headers as Record<string,string>).Authorization));return new Response('{}',{status:200,headers:{'content-type':'application/json'}})})
  const call=bindModelCall('openai-compatible')
  await call('chat/completions',{body:'{}'})
  current='credential-two'
  await call('chat/completions',{body:'{}'})
  expect(sent).toEqual(['Bearer credential-one','Bearer credential-two'])
  expect(asked).toBe(2)
 })
})
