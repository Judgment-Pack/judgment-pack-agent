import { afterEach, beforeEach, expect } from 'vitest'
import { configureHarness } from '../host'
import { testHost } from './host'
import { isProviderOptionsKeyWarning, recordSdkWarnings, takeSdkWarnings } from './sdkWarnings'

/**
 * The ground every test and the certification suite run on, registered as
 * vitest's one `setupFiles`.
 *
 * **The host is supplied before any test file loads**, so a file that mocks
 * `host.ts` around the original starts from the test host's values, and again
 * before every test, so no test inherits another's.
 */
configureHarness(testHost())
beforeEach(() => {
  configureHarness(testHost())
})

/**
 * **The AI SDK's deprecation of a provider-options key fails the test that caused it** (agent#38).
 * The warnings are kept rather than printed (`testing/sdkWarnings.ts`), and checked after each
 * test, so a request that puts options under a key the SDK is retiring cannot pass quietly.
 */
recordSdkWarnings()
afterEach(() => {
  expect(takeSdkWarnings().filter(isProviderOptionsKeyWarning)).toEqual([])
})

/**
 * **A default `fetch` that refuses rather than dials**, as the Desk's own test
 * ground has it: a test that wants a real answer stubs `fetch` itself, and one
 * that forgot gets an immediate, named rejection instead of a socket.
 */
if (!('__harnessDefaultFetch' in globalThis)) {
  ;(globalThis as unknown as { __harnessDefaultFetch: boolean }).__harnessDefaultFetch = true
  globalThis.fetch = (() =>
    Promise.reject(new Error('no fetch stub in this test — see testing/setup.ts'))) as unknown as typeof fetch
}
