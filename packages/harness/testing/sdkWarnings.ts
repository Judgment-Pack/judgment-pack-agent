/**
 * What the AI SDK says about a call, kept where a test can read it (agent#38).
 *
 * The SDK writes its warnings to the process (a `DeprecationWarning` once per code) unless the
 * global `AI_SDK_LOG_WARNINGS` is a function, which then receives every one. This module is that
 * function: registered through `testing/setup.ts` for every test file, it keeps each warning and
 * leaves the suite's output free of them. `testing/setup.ts` fails a test that left one about a
 * `providerOptions` key (the deprecated spelling of a provider's key, which a later SDK pin may stop
 * reading, dropping the options it carried), and a test may read the warnings of its own run.
 */
export interface SdkWarning {
  type: string
  setting?: string
  message?: string
  [member: string]: unknown
}

const kept: SdkWarning[] = []

/** Registered once per test file by `testing/setup.ts`. */
export function recordSdkWarnings(): void {
  ;(globalThis as { AI_SDK_LOG_WARNINGS?: unknown }).AI_SDK_LOG_WARNINGS = (call: { warnings: SdkWarning[] }) => {
    kept.push(...call.warnings)
  }
}

/** The warnings since the last call, and none after it. */
export function takeSdkWarnings(): SdkWarning[] {
  return kept.splice(0, kept.length)
}

/** Whether a warning is the SDK saying a provider's options sit under a key it deprecates. */
export function isProviderOptionsKeyWarning(warning: SdkWarning): boolean {
  return warning.type === 'deprecated' && typeof warning.setting === 'string' && warning.setting.startsWith('providerOptions key ')
}
