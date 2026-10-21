import {describe,it,expect} from 'vitest'
import {retryingCall} from './retry'

const response=(status:number)=>new Response(status===200?'ok':'{}',{status})

describe('ADR-0020 provider failover',()=>{
 it('switches once on unavailable and resets the retry count',async()=>{
  const answers=[response(503),response(200)],attempts:number[]=[],events:unknown[]=[]
  const call=retryingCall(async()=>answers.shift()!,{signal:new AbortController().signal,onAttempt:n=>attempts.push(n),fallback:reason=>reason==='unavailable'?{from:'primary',to:'secondary'}:undefined,onSwitch:e=>{events.push(e)}})
  expect((await call('',{method:'POST',headers:{},body:'{}'})).status).toBe(200)
  expect(attempts).toEqual([1,1])
  expect(events).toEqual([{from:'primary',to:'secondary',reason:'unavailable'}])
 })
 it('never switches a refusal, content filter or invalid request',async()=>{
  for(const status of [400,403]){
   let switched=false
   const call=retryingCall(async()=>response(status),{signal:new AbortController().signal,fallback:()=>{switched=true;return {from:'a',to:'b'}}})
   expect((await call('',{method:'POST',headers:{},body:'{}'})).status).toBe(status)
   expect(switched).toBe(false)
  }
 })
})
