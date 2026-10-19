/**
 * The host every test and the certification suite run against.
 *
 * Tests only: nothing here is built or exported. It supplies each injection
 * point in `host.ts` the way the Desk's own test ground supplies it
 * (`web/src/testing/setup.ts` at the Desk's pinned commit): a page that
 * already holds a session, a relay addressed by path, no open desk, no
 * connection header, and a socket transport that dials only when started and
 * offers the session on its subprotocols, as the Desk's (`web/src/mcp/transport.ts`)
 * does. The reply-language table and instruction are the Desk's
 * own (`web/src/i18n/locales.ts`, `web/src/i18n/assistantLanguage.ts`),
 * carried here so the engines' language tests have a host that writes them.
 * A host's real values come from the host; see the package README.
 */
import type { Transport } from '@modelcontextprotocol/sdk/shared/transport.js'
import { decodedBytes } from '../images'
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

/**
 * The host's socket, as far as a session sees it: nothing is dialled until
 * `start()`, which opens the page's `WebSocket` with the URL and the
 * subprotocol offer it was built with. The tests that connect pass a transport
 * of their own; this one is only ever built, started or closed.
 */
class TestSocketTransport implements Transport {
  private socket?: WebSocket
  constructor(
    private readonly url: string,
    private readonly protocols: string[] = []
  ) {}
  async start(): Promise<void> {
    this.socket = new WebSocket(this.url, this.protocols)
  }
  async send(): Promise<void> {
    throw new Error('the test host’s socket carries no frames: pass a transport')
  }
  async close(): Promise<void> {
    this.socket?.close()
  }
}

/** A fresh test host: a session in hand that never ends. */
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
    socketURL: () => `${window.location.protocol === 'https:' ? 'wss:' : 'ws:'}//${window.location.host}/ws`,
    socketProtocols: (id) => ['jpack-desk', `jpack-desk-session.${id}`],
    DeskWebSocketTransport: TestSocketTransport,
    deskFetch: () => Promise.reject(new Error('no host request in this test')),
    answer: () => Promise.reject(new Error('no host request in this test')),
    readFile: () => Promise.reject(new Error('no host file in this test')),
    dialectAPI: () => Promise.reject(new Error('no host preparation in this test')),
    endpointCapabilities: () => ({}),
    // A render adapter that says what it was given, so a test can find the rendering.
    renderImageToText: async (image) => `TEST RENDERING of a ${image.mediaType} image, ${decodedBytes(image.bytes)} bytes`
  }
}
