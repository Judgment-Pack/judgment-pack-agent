/**
 * The gate, measured at the frame and at the socket.
 *
 * Every assertion here is about **what left the page**, never about what the
 * gate believes it did: the recorder is a transport that keeps the frames it
 * was handed, so a gate that emitted the right notice and sent the wrong frame
 * fails. That is the same discipline the chassis' relay tests use, and it is
 * the reason these are not assertions on `inspectOutboundFrame` alone.
 */
import { Client } from '@modelcontextprotocol/sdk/client/index.js'
import { InMemoryTransport } from '@modelcontextprotocol/sdk/inMemory.js'
import type { Transport } from '@modelcontextprotocol/sdk/shared/transport.js'
import type { JSONRPCMessage } from '@modelcontextprotocol/sdk/types.js'
import { describe, expect, it } from 'vitest'
import gateFramesVector from '../../contract/vectors/tool-call/gate-frames.json'
import phaseOffersVector from '../../contract/vectors/tool-offer/phase-offers.json'
import { TASK_PROFILES } from './delegationContract'
import type { HostTool, McpTool } from './engine'
import { ASSISTANT_TOOLS } from './host'
import { PLAN_RUNTIME_READS, planningTool } from './planning/policy'
import { criticMayCall, criticMayRead } from './refutation'
import {
  GateViolation,
  allowedTools,
  gateTransport,
  inspectOutboundFrame,
  type GuardrailNotice
} from './toolGate'

// The whole ceiling, under the Desk's name for it: the Desk's five and, since agent#7, the test tool.
const FIVE = allowedTools([...ASSISTANT_TOOLS])

/**
 * A transport that sends nowhere and remembers everything — **as bytes**.
 *
 * The serialized frame is what the chassis relays and what the runtime reads,
 * and it is not the object: an inherited `rehearsal: true` reads as `true` from
 * the object and is absent from the bytes. So assertions are made on
 * `JSON.stringify`'d text parsed back, exactly as `DeskWebSocketTransport`
 * writes it, and the object is kept only where identity is the claim.
 */
function recorder(): {
  transport: Transport
  sent: JSONRPCMessage[]
  wire: string[]
  onWire: () => Record<string, unknown>[]
} {
  const sent: JSONRPCMessage[] = []
  const wire: string[] = []
  const transport: Transport = {
    async start() {},
    async close() {},
    async send(message: JSONRPCMessage) {
      sent.push(message)
      // The transport's own line, byte for byte.
      wire.push(JSON.stringify(message))
    }
  }
  return {
    transport,
    sent,
    wire,
    onWire: () => wire.map((line) => JSON.parse(line) as Record<string, unknown>)
  }
}

function call(name: string, args: Record<string, unknown> = {}): JSONRPCMessage {
  return {
    jsonrpc: '2.0',
    id: 1,
    method: 'tools/call',
    params: { name, arguments: args }
  } as unknown as JSONRPCMessage
}

function gated(allowed: ReadonlySet<string>) {
  const { transport, sent, wire, onWire } = recorder()
  const notices: GuardrailNotice[] = []
  gateTransport(transport, { allowed, onGuardrail: (notice) => notices.push(notice) })
  return { transport, sent, wire, onWire, notices }
}

/** The `arguments` object of the first frame that actually reached the wire. */
function wireArguments(onWire: () => Record<string, unknown>[]): Record<string, unknown> {
  const frames = onWire()
  expect(frames.length, 'no frame reached the wire').toBeGreaterThan(0)
  return (frames[0]!.params as { arguments: Record<string, unknown> }).arguments
}

describe('the allow-list', () => {
  it('is the configured subset intersected with the ceiling', () => {
    expect([...allowedTools(['validate', 'get_schema'])]).toEqual(['validate', 'get_schema'])
    // The ceiling holds even where a caller wrote a longer list.
    expect([...allowedTools(['validate', 'write_file', 'bash'])]).toEqual(['validate'])
    expect([...allowedTools([])]).toEqual([])
  })

  it('refuses a name outside it, and the frame never reaches the socket', async () => {
    const { transport, sent, notices } = gated(FIVE)
    await expect(transport.send(call('write_file', { path: 'p', content: 'c' }))).rejects.toThrow(
      GateViolation
    )
    expect(sent).toEqual([])
    expect(notices).toHaveLength(1)
    expect(notices[0]!.action).toBe('refused')
    expect(notices[0]!.tool).toBe('write_file')
  })

  it('names the tool on the violation, not only in its sentence', async () => {
    const { transport } = gated(FIVE)
    const refused = await transport.send(call('get_pack')).catch((error: unknown) => error)
    expect(refused).toBeInstanceOf(GateViolation)
    expect((refused as GateViolation).tool).toBe('get_pack')
  })

  it('refuses a tool the desk allows but this file did not grant', async () => {
    const { transport, sent, notices } = gated(allowedTools(['get_schema']))
    await expect(transport.send(call('validate', { document: '{}' }))).rejects.toThrow(GateViolation)
    expect(sent).toEqual([])
    expect(notices[0]!.detail).toContain('get_schema')
  })

  it('refuses everything where the file granted nothing', async () => {
    const { transport, sent } = gated(allowedTools([]))
    for (const tool of ASSISTANT_TOOLS) {
      await expect(transport.send(call(tool))).rejects.toThrow(GateViolation)
    }
    expect(sent).toEqual([])
  })

  it('refuses a frame whose tool name is not a string at all', async () => {
    const { transport, sent } = gated(FIVE)
    const frame = {
      jsonrpc: '2.0',
      id: 2,
      method: 'tools/call',
      params: { name: 7, arguments: {} }
    } as unknown as JSONRPCMessage
    await expect(transport.send(frame)).rejects.toThrow(GateViolation)
    expect(sent).toEqual([])
  })
})

describe('the rehearsal rewrite, read off the wire', () => {
  it('rewrites a call that carried no rehearsal member', async () => {
    const { transport, onWire, notices } = gated(FIVE)
    await transport.send(call('experimental_evaluate', { pack: '{"a":1}', facts: '{}' }))
    const args = wireArguments(onWire)
    expect(args.rehearsal).toBe(true)
    // Everything else travels exactly as the engine wrote it.
    expect(args.pack).toBe('{"a":1}')
    expect(args.facts).toBe('{}')
    expect(notices[0]!.action).toBe('rewrote')
  })

  it('rewrites a call that said rehearsal: false', async () => {
    const { transport, onWire, notices } = gated(FIVE)
    await transport.send(call('experimental_evaluate', { pack: '{}', rehearsal: false }))
    expect(wireArguments(onWire).rehearsal).toBe(true)
    expect(notices[0]!.detail).toContain('rehearsal: false')
  })

  it('rewrites a call whose rehearsal member is a truthy non-true value', async () => {
    // `"true"` is not `true`, and a gate that read this as satisfied would let
    // a call through on the strength of a string.
    const { transport, onWire } = gated(FIVE)
    await transport.send(call('experimental_evaluate', { pack: '{}', rehearsal: 'true' }))
    expect(wireArguments(onWire).rehearsal).toBe(true)
  })

  it('sends an own rehearsal: true on, and says nothing about it', async () => {
    const { transport, onWire, notices } = gated(FIVE)
    await transport.send(call('experimental_evaluate', { pack: '{}', rehearsal: true }))
    expect(wireArguments(onWire)).toEqual({ pack: '{}', rehearsal: true })
    expect(notices).toEqual([])
  })

  /**
   * **The shape corpus.** Every one of these reads as satisfied to a check on
   * the object, or defeats a copy written the obvious way; the assertion is
   * always on the bytes, because the bytes are what the runtime reads.
   */
  describe('whatever shape the arguments arrive in', () => {
    it('sends an own rehearsal: true where the value was only inherited', async () => {
      // The defect this rebuild exists for. `args.rehearsal` reads `true`, and
      // `JSON.stringify` drops it: an inspect-and-forward gate would have sent
      // an unrehearsed evaluation and the runtime would have recorded it.
      const inherited = Object.create({ rehearsal: true }) as Record<string, unknown>
      inherited.pack = '{"a":1}'
      const { transport, onWire, notices } = gated(FIVE)
      await transport.send(call('experimental_evaluate', inherited))
      expect(wireArguments(onWire)).toEqual({ pack: '{"a":1}', rehearsal: true })
      // The canonical arguments never carried it — an inherited property is one
      // no serializer sends — so that is what the notice says.
      expect(notices[0]!.detail).toContain('survived serialization')
    })

    it('sends an own rehearsal: true for a null-prototype object', async () => {
      const bare = Object.create(null) as Record<string, unknown>
      bare.pack = '{}'
      const { transport, onWire } = gated(FIVE)
      await transport.send(call('experimental_evaluate', bare))
      expect(wireArguments(onWire)).toEqual({ pack: '{}', rehearsal: true })
    })

    it('does not mutate a frozen arguments object, and still sends the member', async () => {
      const frozen = Object.freeze({ pack: '{}' })
      const { transport, onWire } = gated(FIVE)
      await transport.send(call('experimental_evaluate', frozen as Record<string, unknown>))
      expect(wireArguments(onWire)).toEqual({ pack: '{}', rehearsal: true })
      expect(Object.hasOwn(frozen, 'rehearsal')).toBe(false)
    })

    it('reads a getter once, so it cannot answer the check and the wire differently', async () => {
      // Two reads, two answers, is how a check on the object and a
      // serialization of the object come apart. There is one read.
      let reads = 0
      const shifty = {
        pack: '{}',
        get rehearsal() {
          reads += 1
          return reads === 1
        }
      }
      const { transport, onWire } = gated(FIVE)
      await transport.send(call('experimental_evaluate', shifty as Record<string, unknown>))
      expect(wireArguments(onWire).rehearsal).toBe(true)
      // **The counter, asserted.** Round 2 pointed out that this case proved
      // nothing without it: a second read is exactly how the check and the wire
      // come apart, and one read is the claim.
      expect(reads).toBe(1)
    })

    it('sends an own rehearsal: true through a proxy that lies about the member', async () => {
      const lying = new Proxy(
        { pack: '{}' } as Record<string, unknown>,
        {
          get(target, key) {
            if (key === 'rehearsal') return true
            return Reflect.get(target, key)
          }
        }
      )
      const { transport, onWire } = gated(FIVE)
      await transport.send(call('experimental_evaluate', lying))
      // `rehearsal` is not an own key of the target, so the proxy's `get` lie
      // never reaches the bytes — and the rebuild puts a real one there.
      expect(wireArguments(onWire)).toEqual({ pack: '{}', rehearsal: true })
    })

    it('sends canonical bytes even where the call already read as rehearsed', async () => {
      // The `already` branch has nothing to *report* — and still may not
      // forward the caller's object. This `toJSON` carries the member the first
      // time it is asked and drops it the second, so a gate that checked the
      // canonical form and then handed the transport the original would send
      // an unrehearsed evaluation with nothing to show for it.
      let serializations = 0
      const alternating = {
        pack: '{"a":1}',
        toJSON() {
          serializations += 1
          return serializations === 1
            ? { pack: '{"a":1}', rehearsal: true }
            : { pack: '{"a":1}' }
        }
      }
      const { transport, onWire, notices } = gated(FIVE)
      await transport.send(
        call('experimental_evaluate', alternating as unknown as Record<string, unknown>)
      )
      expect(wireArguments(onWire)).toEqual({ pack: '{"a":1}', rehearsal: true })
      // Nothing to say: what it serialized into already carried the member.
      expect(notices).toEqual([])
      // And it was asked exactly once, by the canonicalization.
      expect(serializations).toBe(1)
    })

    it('sends an own rehearsal: true where the arguments carry an enumerable toJSON', async () => {
      // The round-2 defect. A copied `toJSON` is invoked at serialization, so a
      // rebuild that carried rehearsal: true produced bytes that did not — and
      // the runtime would have recorded the evaluation.
      const lying = {
        pack: '{"a":1}',
        toJSON() {
          return { pack: '{"a":1}' }
        }
      }
      const { transport, onWire, notices } = gated(FIVE)
      await transport.send(
        call('experimental_evaluate', lying as unknown as Record<string, unknown>)
      )
      expect(wireArguments(onWire)).toEqual({ pack: '{"a":1}', rehearsal: true })
      expect(notices[0]!.action).toBe('rewrote')
    })

    it('reads a nested toJSON once, and sends what it produced', async () => {
      const nested = {
        pack: {
          toJSON() {
            return { title: 'from toJSON' }
          }
        }
      }
      const { transport, onWire } = gated(FIVE)
      await transport.send(
        call('experimental_evaluate', nested as unknown as Record<string, unknown>)
      )
      expect(wireArguments(onWire)).toEqual({
        pack: { title: 'from toJSON' },
        rehearsal: true
      })
    })

    it('drops a symbol-keyed property, which no serializer would have sent', async () => {
      const marked: Record<string | symbol, unknown> = { pack: '{}' }
      marked[Symbol('rehearsal')] = true
      const { transport, onWire } = gated(FIVE)
      await transport.send(call('experimental_evaluate', marked as Record<string, unknown>))
      expect(wireArguments(onWire)).toEqual({ pack: '{}', rehearsal: true })
    })

    it.each([
      ['a capitalized key', 'Rehearsal'],
      ['a mixed-case key', 'rehearsaL'],
      ['a Cyrillic confusable', 'rehea\u0433sal'],
      ['a key with a zero-width space', 'rehe\u200barsal']
    ])('writes the ASCII member beside %s rather than mistaking it for one', async (_what, key) => {
      // None of these is `rehearsal` to the runtime, and none of them stops the
      // real one being written.
      const { transport, onWire } = gated(FIVE)
      await transport.send(
        call('experimental_evaluate', { pack: '{}', [key]: true } as Record<string, unknown>)
      )
      const args = wireArguments(onWire)
      expect(args.rehearsal).toBe(true)
      expect(Object.hasOwn(args, 'rehearsal')).toBe(true)
      expect(args[key]).toBe(true)
    })

    it('refuses arguments that cannot be serialized at all', async () => {
      const cyclic: Record<string, unknown> = { pack: '{}' }
      cyclic.self = cyclic
      const { transport, sent } = gated(FIVE)
      await expect(transport.send(call('experimental_evaluate', cyclic))).rejects.toThrow(
        GateViolation
      )
      expect(sent).toEqual([])
    })

    it('sends the member where the call carried no arguments at all', async () => {
      const { transport, onWire } = gated(FIVE)
      await transport.send({
        jsonrpc: '2.0',
        id: 5,
        method: 'tools/call',
        params: { name: 'experimental_evaluate' }
      } as unknown as JSONRPCMessage)
      expect(wireArguments(onWire)).toEqual({ rehearsal: true })
    })

    it.each([
      ['a string', 'not an object'],
      ['a number', 7],
      ['null', null],
      ['an array', [1, 2]]
    ])('refuses arguments that are %s rather than guessing', async (_what, args) => {
      const { transport, sent } = gated(FIVE)
      await expect(
        transport.send({
          jsonrpc: '2.0',
          id: 6,
          method: 'tools/call',
          params: { name: 'experimental_evaluate', arguments: args }
        } as unknown as JSONRPCMessage)
      ).rejects.toThrow(GateViolation)
      expect(sent).toEqual([])
    })
  })

  it('never puts the pack or the facts in the notice', async () => {
    const { transport, notices } = gated(FIVE)
    await transport.send(
      call('experimental_evaluate', { pack: '{"title":"SECRET DRAFT"}', facts: '{"x":"SECRET"}' })
    )
    expect(notices[0]!.detail).not.toContain('SECRET')
  })

  it('refuses before it rewrites, where the file granted no evaluate', async () => {
    // The one case the order decides: a tool nobody granted must not be
    // corrected on its way out.
    const { transport, sent, notices } = gated(allowedTools(['validate']))
    await expect(transport.send(call('experimental_evaluate', { pack: '{}' }))).rejects.toThrow(
      GateViolation
    )
    expect(sent).toEqual([])
    expect(notices.map((notice) => notice.action)).toEqual(['refused'])
  })
})

describe('the frames the gate will not send at all', () => {
  it.each([
    ['a JSON-RPC batch', [{ jsonrpc: '2.0', id: 1, method: 'tools/call', params: { name: 'validate' } }]],
    [
      'a batch mixing an allowed call with a write',
      [
        { jsonrpc: '2.0', id: 1, method: 'tools/call', params: { name: 'validate' } },
        { jsonrpc: '2.0', id: 2, method: 'tools/call', params: { name: 'write_file' } }
      ]
    ],
    ['null', null],
    ['a string', 'tools/call'],
    ['a number', 7],
    ['a frame with a numeric method', { jsonrpc: '2.0', id: 1, method: 9 }],
    ['a frame that is neither request nor response', { jsonrpc: '2.0', id: 1 }],
    ['a response with no id', { jsonrpc: '2.0', result: {} }],
    ['Tools/Call', { jsonrpc: '2.0', id: 1, method: 'Tools/Call', params: { name: 'validate' } }],
    ['TOOLS/CALL', { jsonrpc: '2.0', id: 1, method: 'TOOLS/CALL', params: { name: 'validate' } }],
    [
      'tools/call with whitespace around it',
      { jsonrpc: '2.0', id: 1, method: ' tools/call ', params: { name: 'validate' } }
    ],
    ['tools/call with no params', { jsonrpc: '2.0', id: 1, method: 'tools/call' }],
    [
      'tools/call whose params are a string',
      { jsonrpc: '2.0', id: 1, method: 'tools/call', params: 'name=validate' }
    ],
    ['a frame with no jsonrpc member', { id: 1, method: 'tools/list', params: {} }],
    ['a frame declaring jsonrpc 1.0', { jsonrpc: '1.0', id: 1, method: 'tools/list' }],
    ['a request whose id is an object', { jsonrpc: '2.0', id: { n: 1 }, method: 'tools/list' }],
    ['a request whose id is a boolean', { jsonrpc: '2.0', id: true, method: 'tools/list' }],
    [
      'a frame carrying a method and a result',
      { jsonrpc: '2.0', id: 1, method: 'tools/list', result: {} }
    ],
    [
      'a response carrying both a result and an error',
      { jsonrpc: '2.0', id: 1, result: {}, error: { code: -1, message: 'x' } }
    ],
    ['a response carrying neither', { jsonrpc: '2.0', id: 1 }],
    [
      'a non-tools/call frame whose params are a string',
      { jsonrpc: '2.0', id: 1, method: 'prompts/get', params: 'name=p' }
    ],
    // The three the hand-written rule accepted and the SDK's own schema does
    // not: an id that is not a string or an integer, a result that is not an
    // object, and an error that is not one.
    ['a response with a null id', { jsonrpc: '2.0', id: null, result: {} }],
    ['a response whose result is a number', { jsonrpc: '2.0', id: 1, result: 7 }],
    ['a response whose error is a string', { jsonrpc: '2.0', id: 1, error: 'nope' }],
    [
      'a response whose error carries no code',
      { jsonrpc: '2.0', id: 1, error: { message: 'no code' } }
    ],
    ['a request with a fractional id', { jsonrpc: '2.0', id: 1.5, method: 'tools/list' }]
  ])('refuses %s, and nothing reaches the socket', async (_what, frame) => {
    // **Fail closed.** The rule this replaces was "anything whose method is not
    // exactly tools/call is traffic I have no opinion about", and a batch has
    // no method at all — so an array carrying an allowed call beside a
    // write_file went out whole. The SDK's type says one message; the type is
    // not what runs.
    const { transport, sent } = gated(FIVE)
    await expect(transport.send(frame as unknown as JSONRPCMessage)).rejects.toThrow(GateViolation)
    expect(sent).toEqual([])
  })
})

describe('the shapes that change under an inspection', () => {
  it('refuses a frame that cannot be serialized at all', async () => {
    // A cycle is a refusal here rather than an exception thrown at the socket.
    const cyclic: Record<string, unknown> = {
      jsonrpc: '2.0',
      id: 1,
      method: 'tools/call',
      params: { name: 'validate', arguments: {} }
    }
    ;(cyclic.params as { arguments: Record<string, unknown> }).arguments.self = cyclic
    const { transport, sent } = gated(FIVE)
    await expect(transport.send(cyclic as unknown as JSONRPCMessage)).rejects.toThrow(GateViolation)
    expect(sent).toEqual([])
  })

  it('sends what a shifting method getter serialized into, and never its later answer', async () => {
    // The response exemption used to read `method` off the object and then
    // forward **that object**, so a getter answering `undefined` for the check
    // and `tools/call` for the socket carried a method out after all. The
    // canonical frame is taken from the one read `JSON.stringify` makes, and it
    // is what leaves: whatever the getter says afterwards reaches nothing.
    let reads = 0
    const shifty = {
      jsonrpc: '2.0',
      id: 1,
      result: {},
      get method() {
        reads += 1
        return reads === 1 ? undefined : 'tools/call'
      }
    }
    const { transport, sent, onWire } = gated(FIVE)
    await transport.send(shifty as unknown as JSONRPCMessage)
    // **One read, asserted before anything else touches the object**, because
    // this whole case is about how many times it is asked.
    expect(reads).toBe(1)
    expect(onWire()).toEqual([{ jsonrpc: '2.0', id: 1, result: {} }])
    expect(sent[0]).not.toBe(shifty)
    // The object still answers `tools/call` — to anyone who asks it again, and
    // after this line nobody does.
    expect(shifty.method).toBe('tools/call')
  })

  it('refuses a response whose method getter answers tools/call to the serializer', async () => {
    // The other order: the read that matters is the one the bytes came from,
    // and a frame carrying both a method and a result is not any of the three
    // shapes this gate will send.
    const shifty = {
      jsonrpc: '2.0',
      id: 1,
      result: {},
      get method() {
        return 'tools/call'
      }
    }
    const { transport, sent } = gated(FIVE)
    await expect(transport.send(shifty as unknown as JSONRPCMessage)).rejects.toThrow(GateViolation)
    expect(sent).toEqual([])
  })

  it('refuses a response-shaped object that serializes as a tools/call', async () => {
    const disguised = {
      jsonrpc: '2.0',
      id: 1,
      result: {},
      toJSON() {
        return {
          jsonrpc: '2.0',
          id: 1,
          method: 'tools/call',
          params: { name: 'write_file', arguments: { path: 'p' } }
        }
      }
    }
    const { transport, sent } = gated(FIVE)
    await expect(transport.send(disguised as unknown as JSONRPCMessage)).rejects.toThrow(
      GateViolation
    )
    expect(sent).toEqual([])
  })

  it('checks the tool a frame serializes into, not the one it claims', async () => {
    // The mirror image: a frame that reads as an allowed call and serializes as
    // a write. The canonical form is what the allow-list is applied to.
    const disguised = {
      jsonrpc: '2.0',
      id: 1,
      method: 'tools/call',
      params: { name: 'validate', arguments: {} },
      toJSON() {
        return {
          jsonrpc: '2.0',
          id: 1,
          method: 'tools/call',
          params: { name: 'write_file', arguments: { path: 'p' } }
        }
      }
    }
    const { transport, sent, notices } = gated(FIVE)
    await expect(transport.send(disguised as unknown as JSONRPCMessage)).rejects.toThrow(
      GateViolation
    )
    expect(sent).toEqual([])
    expect(notices[0]!.tool).toBe('write_file')
  })
})

describe('what the gate leaves alone', () => {
  it.each([
    ['initialize', { jsonrpc: '2.0', id: 1, method: 'initialize', params: {} }],
    ['tools/list', { jsonrpc: '2.0', id: 2, method: 'tools/list', params: {} }],
    ['prompts/get', { jsonrpc: '2.0', id: 3, method: 'prompts/get', params: { name: 'p' } }],
    ['a notification', { jsonrpc: '2.0', method: 'notifications/initialized' }],
    ['a notification with no params', { jsonrpc: '2.0', method: 'notifications/cancelled' }],
    ['a response', { jsonrpc: '2.0', id: 4, result: {} }],
    ['an error response', { jsonrpc: '2.0', id: 5, error: { code: -32601, message: 'no' } }]
  ])('sends %s on as the same bytes, and says nothing', async (_what, frame) => {
    // **Not by identity, and that is the point.** What travels is the canonical
    // frame, so an object that would serialize into something else does not get
    // to be classified as one thing and sent as another.
    const { transport, sent, onWire, notices } = gated(FIVE)
    await transport.send(frame as unknown as JSONRPCMessage)
    expect(onWire()).toEqual([frame])
    expect(sent[0]).not.toBe(frame)
    expect(notices).toEqual([])
  })

  it('carries the send options through', async () => {
    const seen: unknown[] = []
    const transport: Transport = {
      async start() {},
      async close() {},
      async send(_message: JSONRPCMessage, options?: unknown) {
        seen.push(options)
      }
    }
    gateTransport(transport, { allowed: FIVE })
    await transport.send(call('validate', { document: '{}' }), {
      relatedRequestId: 9
    } as never)
    await transport.send(call('experimental_evaluate', { pack: '{}' }), {
      relatedRequestId: 10
    } as never)
    expect(seen).toEqual([{ relatedRequestId: 9 }, { relatedRequestId: 10 }])
  })
})

describe('inspectOutboundFrame, with no socket at all', () => {
  it('reports the verdict a frame would get', () => {
    expect(inspectOutboundFrame(call('validate', { document: '{}' }), FIVE).verdict).toBe('send')
    expect(inspectOutboundFrame(call('write_file'), FIVE).verdict).toBe('refused')
    expect(inspectOutboundFrame(call('experimental_evaluate', { pack: '{}' }), FIVE).verdict).toBe(
      'send'
    )
  })

  it('hands back canonical data and never the caller’s object', () => {
    // The whole shape of the fix: what was checked and what is sent are the
    // same bytes, and the caller keeps whatever it had.
    const original = call('validate', { document: '{}' })
    const decided = inspectOutboundFrame(original, FIVE)
    expect(decided.verdict).toBe('send')
    expect(decided.verdict === 'send' && decided.frame).not.toBe(original)
    expect(decided.verdict === 'send' && decided.frame).toEqual(original)
  })

  it('leaves the caller’s frame unmutated when it rewrites', () => {
    // The rewrite writes onto canonical data. A gate that edited the caller's
    // object would change what the engine believes it asked for, which is the
    // one thing a guardrail may not do quietly.
    const original = call('experimental_evaluate', { pack: '{}' })
    const decided = inspectOutboundFrame(original, FIVE)
    expect(decided.verdict).toBe('send')
    const params = (original as unknown as { params: { arguments: Record<string, unknown> } }).params
    expect(params.arguments.rehearsal).toBeUndefined()
  })

  it('rebuilds an evaluate frame even where nothing needed reporting', () => {
    // There is no branch that forwards the caller's object.
    const original = call('experimental_evaluate', { pack: '{}', rehearsal: true })
    const decided = inspectOutboundFrame(original, FIVE)
    expect(decided.verdict).toBe('send')
    expect(decided.verdict === 'send' && decided.notice).toBeNull()
    expect(decided.verdict === 'send' && decided.frame).not.toBe(original)
  })
})

/**
 * **One whole conversation, through the gate, with a real SDK client.**
 *
 * Every rule above is a refusal, and a suite made only of refusals can be
 * satisfied by a gate that refuses everything. This is the other half: an
 * ordinary MCP session — the handshake, the notification that follows it, the
 * server's own `ping` and the client's automatic answer to it, a listing, a
 * prompt, a tool call, and a response to a request the server made — every
 * frame of which has to pass. Round 3 asked for it in exactly these terms,
 * because tightening the frame rules to the SDK's own schema is the kind of
 * change that can break a connection while every refusal test stays green.
 */
describe('an ordinary session, every frame of it', () => {
  it('completes a handshake, a listing, a prompt, a call and a server request', async () => {
    const [clientSide, serverSide] = InMemoryTransport.createLinkedPair()
    const sentByClient: Record<string, unknown>[] = []
    const refused: string[] = []

    // The gate on the client's side, exactly as a session installs it, with a
    // recorder in front of the socket so every frame it let out is readable.
    const realSend = clientSide.send.bind(clientSide)
    clientSide.send = async (message) => {
      sentByClient.push(JSON.parse(JSON.stringify(message)) as Record<string, unknown>)
      return realSend(message)
    }
    gateTransport(clientSide, {
      allowed: FIVE,
      onGuardrail: (notice) => {
        if (notice.action === 'refused') refused.push(notice.detail)
      }
    })

    serverSide.onmessage = (message: JSONRPCMessage) => {
      const frame = message as unknown as {
        id?: number
        method?: string
        params?: { name?: string }
      }
      if (frame.method === undefined) return // the client's answer to our ping
      const reply = (result: unknown) =>
        void serverSide.send({ jsonrpc: '2.0', id: frame.id, result } as never)
      if (frame.method === 'initialize') {
        reply({
          protocolVersion: '2025-06-18',
          capabilities: { tools: {}, prompts: {} },
          serverInfo: { name: 'exchange', version: '0' }
        })
        return
      }
      if (frame.method === 'notifications/initialized') return
      if (frame.method === 'tools/list') {
        reply({ tools: [{ name: 'validate', description: 'check', inputSchema: { type: 'object' } }] })
        return
      }
      if (frame.method === 'prompts/get') {
        reply({ messages: [{ role: 'user', content: { type: 'text', text: 'the prompt' } }] })
        return
      }
      if (frame.method === 'tools/call') {
        reply({ content: [{ type: 'text', text: '{"status":"valid"}' }] })
        return
      }
      reply({})
    }
    await serverSide.start()

    const client = new Client({ name: 'exchange', version: '0' }, { capabilities: {} })
    await client.connect(clientSide)

    expect((await client.listTools()).tools.map((tool) => tool.name)).toEqual(['validate'])
    const prompt = (await client.getPrompt({ name: 'author_pack' })) as {
      messages: { content: { text: string } }[]
    }
    expect(prompt.messages[0]!.content.text).toBe('the prompt')
    const answered = (await client.callTool({
      name: 'validate',
      arguments: { document: '{}' }
    })) as { content: { text: string }[] }
    expect(answered.content[0]!.text).toBe('{"status":"valid"}')

    // A request the **server** makes, which the client answers — the one frame
    // shape a client only ever sends in reply.
    await serverSide.send({ jsonrpc: '2.0', id: 9001, method: 'ping' } as never)
    await new Promise((resolve) => setTimeout(resolve, 20))

    // Nothing was refused, and the whole conversation is on the record.
    expect(refused).toEqual([])
    const methods = sentByClient.map((frame) => frame.method ?? '(a response)')
    expect(methods).toContain('initialize')
    expect(methods).toContain('notifications/initialized')
    expect(methods).toContain('tools/list')
    expect(methods).toContain('prompts/get')
    expect(methods).toContain('tools/call')
    expect(methods).toContain('(a response)')
    // And the client's answer to the ping is a well-formed response.
    const answer = sentByClient.find((frame) => frame.method === undefined)!
    expect(answer.id).toBe(9001)
    expect(answer.result).toEqual({})

    await client.close()
    await serverSide.close()
  })
})

/**
 * **The contract's gate vectors** (agent#53): the same items the Go gate (`go/gate`) is held to,
 * so both gates are held to one set. Each item of `tool-call/gate-frames.json` is the params of a
 * `tools/call` frame and each of its `frames` a whole frame; the verdict, the notice and what left
 * are the vector's. `tool-offer/phase-offers.json` is what each phase of a run is offered of one
 * session, by the rules the boundary applies (refutation.ts, planning/policy.ts, delegation.ts).
 */
describe('the contract’s gate vectors', () => {
  type Notice = { tool: string; action: string; detail: string } | null
  type Outcome = {
    what: string
    verdict: 'send' | 'refused'
    notice: Notice
    sent?: unknown
    allowed?: string[]
    bounds?: { testPackBytes: number; testMatrixBytes: number }
  }
  const vector = gateFramesVector as unknown as {
    allowed: string[]
    bounds: { testPackBytes: number; testMatrixBytes: number }
    items: Record<string, unknown>[]
    outcomes: Outcome[]
    frames: (Outcome & { frame: unknown })[]
  }
  const held = (frame: unknown, outcome: Outcome, sent: (frame: Record<string, unknown>) => unknown) => {
    const allowed = allowedTools(outcome.allowed ?? vector.allowed)
    const bounds = outcome.bounds ?? vector.bounds
    const decided = inspectOutboundFrame(frame as JSONRPCMessage, allowed, bounds)
    expect(decided.verdict).toBe(outcome.verdict)
    expect(decided.notice).toEqual(outcome.notice)
    if (decided.verdict === 'send') expect(sent(decided.frame as unknown as Record<string, unknown>)).toEqual(outcome.sent)
    else {
      expect(decided.violation.message).toBe(`refused on the wire: ${outcome.notice!.detail}`)
      expect(decided.violation.tool).toBe(outcome.notice!.tool)
    }
  }

  it('has one outcome for each item', () => {
    expect(vector.outcomes).toHaveLength(vector.items.length)
  })

  it.each(vector.items.map((params, at) => [vector.outcomes[at]!.what, params, vector.outcomes[at]!] as const))(
    'decides a tools/call frame: %s',
    (_what, params, outcome) => {
      held({ jsonrpc: '2.0', id: 1, method: 'tools/call', params }, outcome, (frame) => frame.params)
    }
  )

  it.each(vector.frames.map((frame) => [frame.what, frame] as const))('decides a whole frame: %s', (_what, frame) => {
    held(frame.frame, frame, (sent) => sent)
  })

  it('offers each phase what the vector says, of one session', () => {
    const phases = phaseOffersVector as unknown as {
      phases: { phase: string; profile?: string; within?: string }[]
      items: { tools: McpTool[]; hostTools: HostTool[] }[]
    }
    const session = phases.items[0]!
    // The boundary's own context tools, which it adds to a run after a phase's filter of the
    // session's host tools (contextualSession), in every phase but the critic's and the reformat run's.
    const context = new Set(['list_context', 'read_context', 'read_window', 'search', 'update_plan', 'request_budget_increase'])
    const offered = (phase: { phase: string; profile?: string; within?: string }): { tools: McpTool[]; hostTools: HostTool[] } => {
      switch (phase.phase) {
        case 'loop':
          return session
        case 'critic':
          return { tools: session.tools.filter((tool) => criticMayCall(tool.name)), hostTools: session.hostTools.filter((tool) => criticMayRead(tool.name)) }
        case 'plan':
          return {
            tools: session.tools.filter((tool) => PLAN_RUNTIME_READS.includes(tool.name)),
            hostTools: session.hostTools.filter((tool) => context.has(tool.name) || planningTool(tool))
          }
        case 'reformat':
          return { tools: [], hostTools: [] }
        case 'child': {
          // A child's session is its parent's in the parent's phase (delegation.ts passes the
          // phase on, and planningSession applies to children), narrowed by its profile.
          const parent = offered({ phase: phase.within ?? 'loop' })
          const profile = TASK_PROFILES.find((candidate) => candidate.id === phase.profile)!
          const runtime = new Set(profile.runtimeTools ?? [])
          return {
            tools: parent.tools.filter((tool) => runtime.has(tool.name)),
            hostTools: parent.hostTools.filter(
              (tool) => context.has(tool.name) || (!tool.interaction && tool.delegation !== undefined && profile.capabilities.includes(tool.delegation))
            )
          }
        }
      }
      throw new Error(`no phase ${phase.phase}`)
    }
    expect(phases.phases).toHaveLength(phases.items.length)
    phases.phases.forEach((phase, at) => {
      expect(offered(phase), `${phase.phase} ${phase.profile ?? ''} ${phase.within ?? ''}`).toEqual(phases.items[at])
    })
  })
})
