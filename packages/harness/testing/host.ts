/**
 * The host every test and the certification suite run against.
 *
 * Tests only: nothing here is built or exported. It supplies each injection
 * point in `host.ts` the way the Desk's own test ground supplies it
 * (`web/src/testing/setup.ts` at the Desk's pinned commit): a page that
 * already holds a session, a relay addressed by path, no open desk and no
 * connection header. The reply-language table and instruction are the Desk's
 * own (`web/src/i18n/locales.ts`, `web/src/i18n/assistantLanguage.ts`),
 * carried here so the engines' language tests have a host that writes them.
 * A host's real values come from the host; see the package README.
 */
import type { HarnessHost, Language } from '../host'

/** BCP 47 language identifiers are preferences, never protocol/schema values. */
export const LANGUAGES = [
  { id: 'en', label: 'English', instruction: 'English' },
  { id: 'fr', label: 'Français', instruction: 'French' },
  { id: 'es', label: 'Español', instruction: 'Spanish' },
  { id: 'de', label: 'Deutsch', instruction: 'German' },
  { id: 'it', label: 'Italiano', instruction: 'Italian' },
  { id: 'pt-PT', label: 'Português (Portugal)', instruction: 'European Portuguese' },
  { id: 'pt-BR', label: 'Português (Brasil)', instruction: 'Brazilian Portuguese' },
  { id: 'ko', label: '한국어', instruction: 'Korean' },
  { id: 'zh-Hans', label: '普通话（简体中文）', instruction: 'Mandarin Chinese using Simplified Chinese characters' },
  { id: 'zh-Hant', label: '國語（繁體中文）', instruction: 'Mandarin Chinese using Traditional Chinese characters' },
  { id: 'yue-Hant', label: '廣東話（繁體中文）', instruction: 'Cantonese using Traditional Chinese characters and natural Cantonese wording' },
  { id: 'ja', label: '日本語', instruction: 'Japanese' }
] as const

/** A reply preference cannot change the runtime contract or source evidence. */
export function assistantLanguageInstructions(locale: Language = 'en'): string {
  const selected = LANGUAGES.find(item => item.id === locale) ?? LANGUAGES[0]
  return `\n\nRESPONSE LANGUAGE\nThe user's interface language is ${selected.instruction} (${selected.id}). Use that language for replies, clarifying questions, progress summaries, review explanations, and newly authored human-readable titles and descriptions, unless the user explicitly requests another language. Preserve existing authored content unless asked to translate it. Never translate tool names, JSON/schema keys, enumerated contract values, identifiers, fact paths, URLs, exact source quotations, or runtime output. Explain quoted output in the response language. Do not change decision semantics or test expectations when changing language.`
}

/** The session id the test page holds, as the Desk's test ground gives it. */
export const TEST_SESSION = 'a-session-this-test-was-given'

const NO_SESSION_MESSAGE = 'No session'

class NoSession extends Error {
  constructor(message: string = NO_SESSION_MESSAGE) {
    super(message)
    this.name = 'NoSession'
  }
}

/** A fresh test host: a session in hand that never ends, and no socket of its own. */
export function testHost(): HarnessHost {
  return {
    assistantLanguageInstructions,
    currentLanguage: () => 'en',
    sessionBearer: async () => TEST_SESSION,
    sessionEnded: () => null,
    whenSessionEnds: () => () => {},
    forgetSession: () => {},
    refusalCode: (answered) => answered.headers.get('X-Jpack-Desk-Refusal'),
    discardBody: async (answered) => {
      try {
        await answered.body?.cancel()
      } catch {
        // already read or cancelled
      }
    },
    NoSession,
    NO_SESSION_MESSAGE,
    chassisUrl: (path, params = {}) => {
      const query = new URLSearchParams(params).toString()
      return query === '' ? path : `${path}?${query}`
    },
    deskHeaders: () => ({}),
    deskSocketURL: (url) => url,
    aiHeaders: (id, revision) =>
      id ? { 'X-Assistant-Connection': id, ...(revision ? { 'X-Assistant-Revision': revision } : {}) } : {},
    socketURL: () => {
      throw new Error('the test host opens no socket: pass a transport')
    },
    socketProtocols: (id) => ['jpack-desk', `jpack-desk-session.${id}`],
    DeskWebSocketTransport: class {
      constructor() {
        throw new Error('the test host opens no socket: pass a transport')
      }
    } as unknown as HarnessHost['DeskWebSocketTransport'],
    deskFetch: () => Promise.reject(new Error('no host request in this test')),
    answer: () => Promise.reject(new Error('no host request in this test')),
    readFile: () => Promise.reject(new Error('no host file in this test')),
    dialectAPI: () => Promise.reject(new Error('no host preparation in this test'))
  }
}
