import {describe,it,expect} from 'vitest'
import {normalizeFailure,readFailure,failureMessage,failure,canRetryAdmission,retryReady,toolInputFailure,toolFailureResult} from './failures'
import {ExecutionPaused} from './execution/state'
import {ContextBudgetError} from './context/policy'
describe('canonical AI failures',()=>{
 it.each([[401,'authentication'],[403,'permission'],[429,'rate-limited'],[500,'unavailable'],[529,'unavailable'],[408,'timeout'],[413,'context-limit'],[400,'invalid-request']])('classifies HTTP %i without provider prose',(status,code)=>{
  const value=normalizeFailure({statusCode:status,responseBody:'{"error":{"message":"credential=SECRET private prompt https://private.test"}}',responseHeaders:{'authorization':'Bearer SECRET','x-request-id':'req_fixture','retry-after':'2'}},'model',0)
  expect(value).toMatchObject({code,status,requestId:'req_fixture',retryAt:'1970-01-01T00:00:02.000Z'})
  expect(JSON.stringify(value)+failureMessage(value)).not.toMatch(/SECRET|private.test|private prompt/)
 })
 it('preserves typed pauses, cancellation and bounded nested SDK causes',()=>{
  expect(normalizeFailure(new ExecutionPaused()).code).toBe('request-limit')
  expect(normalizeFailure(new ContextBudgetError()).code).toBe('context-limit')
  expect(normalizeFailure(new DOMException('secret','AbortError')).code).toBe('cancelled')
  expect(normalizeFailure({name:'AI_RetryError',lastError:{statusCode:429}}).code).toBe('rate-limited')
  const cycle:{cause?:unknown}={};cycle.cause=cycle;expect(normalizeFailure(cycle).code).toBe('unknown')
 })
 it('honors stable provider codes without matching prose',()=>{
  expect(normalizeFailure({statusCode:400,responseBody:'{"error":{"code":"context_length_exceeded"}}'}).code).toBe('context-limit')
  expect(normalizeFailure(Error('context_length_exceeded')).code).toBe('unknown')
  for(const code of ['__proto__','toString','constructor'])expect(normalizeFailure({code}).code).toBe('unknown')
  const spoof=normalizeFailure({statusCode:500,failure:{...failure('unknown'),detail:'PRIVATE_SENTINEL'},failureCode:'request-limit'})
  expect(spoof.code).toBe('unavailable');expect(failureMessage(spoof)).not.toContain('PRIVATE_SENTINEL')
  expect(normalizeFailure({name:'AI_StreamProviderError',statusCode:429})).toMatchObject({code:'rate-limited',outcome:'unknown'})
 })
 it('strips extra persisted data and refuses invalid metadata',()=>{
  expect(readFailure({...failure('timeout'),body:'secret',stack:'private'})).toEqual(failure('timeout'))
  expect(readFailure({...failure('timeout'),code:'made-up'})).toBeUndefined()
  expect(readFailure({...failure('timeout'),requestId:'https://private'})).toBeUndefined()
 })
 it('permits bounded automatic retry only for known refusal before admission',()=>{
  expect(canRetryAdmission(normalizeFailure({code:'busy'},'agent'),1,3)).toBe(true)
  expect(canRetryAdmission(normalizeFailure({code:'busy'},'agent'),3,3)).toBe(false)
  for(const code of ['timeout','connection-lost','unavailable','rate-limited'] as const)expect(canRetryAdmission(failure(code),1,3)).toBe(false)
  expect(retryReady({...failure('rate-limited'),retryAt:new Date(2000).toISOString()},1000)).toBe(false)
 })
})

it('retains explicit host validation guidance while concealing arbitrary thrown messages',()=>{
 const detail='Use a JSON pointer within this context item.'
 expect(toolFailureResult(toolInputFailure(detail))).toMatchObject({isError:true,content:[{text:detail}],structuredContent:{failure:{code:'invalid-tool-input'}}})
 expect(JSON.stringify(toolFailureResult(Error('secret provider payload')))).not.toContain('secret provider payload')
})
