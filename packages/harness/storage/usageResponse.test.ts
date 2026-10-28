import {describe,it,expect} from 'vitest'
import {usageFromVerifiedResponse} from './usageResponse'
import {encodedUsageOutputMaximum} from './usageJournal'
import {UsageBudgetUnknown,UsageBudgetUnsupported} from './usageBudget'
describe('exact final encoded ceilings and provider usage facts',()=>{
 it('takes the maximum conflicting supported field and refuses duplicates/unknown protocols',()=>{
  const bytes=(text:string)=>new TextEncoder().encode(text)
  expect(encodedUsageOutputMaximum('openai-compatible',bytes('{"max_tokens":5,"max_completion_tokens":9,"max_output_tokens":7}'))).toBe(9)
  expect(()=>encodedUsageOutputMaximum('openai-compatible',bytes('{"max_tokens":5,"max_tokens":900}'))).toThrow(UsageBudgetUnsupported)
  for(const token of ['5.0000000000000001','9007199254740991.1','0','null'])expect(()=>encodedUsageOutputMaximum('openai-compatible',bytes(`{"max_tokens":${token}}`))).toThrow(UsageBudgetUnsupported)
  expect(()=>encodedUsageOutputMaximum('opaque',bytes('{"max_tokens":5}'))).toThrow(UsageBudgetUnsupported)
 })
 it('never rounds fractional/unsafe reported figures and allows unrelated argument fractions',async()=>{
  for(const token of ['9007199254740991.1','9007199254740992','4.0000000000000001','1e-999','-1','null','"4"']){
   const response=new Response(`{"usage":{"prompt_tokens":${token}},"tool":{"fraction":1.25}}`)
   await expect(usageFromVerifiedResponse('openai-compatible',response)).rejects.toBeInstanceOf(UsageBudgetUnknown)
  }
  for(const token of ['4','4.0','40e-1'])expect(await usageFromVerifiedResponse('openai-compatible',new Response(`{"usage":{"prompt_tokens":${token}},"tool":{"fraction":1.25}}`))).toEqual({inputTokens:4})
  expect(await usageFromVerifiedResponse('openai-compatible',new Response('{"usage":{}}'))).toEqual({})
 })
})
