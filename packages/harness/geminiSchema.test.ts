/**
 * The removal list, keyword by keyword, and the two properties that make it a
 * ruling rather than a convenience: it is **closed**, and it removes **nothing
 * else**.
 *
 * The conformance session measures the same thing at the wire — the scripted
 * Gemini endpoint refuses a declaration carrying any of these — and this is
 * where each keyword is named individually, because a leg that fails tells you
 * a request was refused and this tells you which word did it.
 */
import { describe, expect, it } from 'vitest'
import {
  GEMINI_SCHEMA_REMOVALS,
  keywordsSent,
  refusedSchemaKeyword,
  refusedSchemaSentence,
  withoutUnsupportedKeywords
} from './geminiSchema'
import { servedSchemaFor } from './engines/contract'
import { RECORDED_TOOLS } from './certification/scriptedServer'

/** Every key in a value, at every depth, property names included. */
function everyKey(value: unknown, into: Set<string> = new Set()): Set<string> {
  if (Array.isArray(value)) {
    for (const item of value) everyKey(item, into)
    return into
  }
  if (value === null || typeof value !== 'object') return into
  for (const [key, inner] of Object.entries(value as Record<string, unknown>)) {
    into.add(key)
    everyKey(inner, into)
  }
  return into
}

describe('the closed removal list for the Gemini function-declaration schema', () => {
  it.each(GEMINI_SCHEMA_REMOVALS)('removes %s wherever it appears, at every depth', (keyword) => {
    const schema = {
      type: 'object',
      [keyword]: keyword === 'additionalProperties' ? false : 'x',
      properties: {
        outer: {
          type: 'array',
          items: { type: 'object', [keyword]: keyword === 'additionalProperties' ? false : 'x' }
        }
      },
      $defs: { named: { type: 'string', [keyword]: 'x' } }
    }
    const walked = withoutUnsupportedKeywords(schema)
    expect(everyKey(walked).has(keyword)).toBe(false)
    // …and the schema it came from is untouched: this returns a new object
    // rather than editing the runtime's own.
    expect(everyKey(schema).has(keyword)).toBe(true)
  })

  it('removes nothing else at all, and keeps every value by identity where it changed nothing', () => {
    const untouched = {
      type: 'object',
      description: 'a description',
      required: ['a'],
      properties: {
        a: { type: 'string', enum: ['x', 'y'], description: 'a' },
        b: { type: 'array', items: { type: 'integer' }, minItems: 1 }
      },
      oneOf: [{ type: 'string' }, { type: 'number' }],
      anyOf: [{ type: 'null' }]
    }
    // Identity, not equality: a subtree with no removal in it is the object the
    // runtime served.
    expect(withoutUnsupportedKeywords(untouched)).toBe(untouched)
  })

  it('keeps oneOf, because a union removed reads as “anything at all”', () => {
    // Deliberately not on the list: some models refuse it and some do not,
    // which makes it exactly the keyword this list must not grow to cover.
    expect(GEMINI_SCHEMA_REMOVALS).not.toContain('oneOf')
    const schema = { oneOf: [{ type: 'string' }, { type: 'number' }] }
    expect(withoutUnsupportedKeywords(schema)).toEqual(schema)
  })

  it.each(['properties', '$defs', 'definitions', 'dependentSchemas'])(
    'leaves an authored name alone under %s, and still walks the schema it points at',
    (map) => {
      // **The defect this row was written against**: a definition *called*
      // `const` was deleted as though it were the keyword, leaving
      // `{"$ref": "#/definitions/const"}` pointing at nothing. A dangling
      // reference is worse than the keyword the list exists to take out.
      const schema = {
        type: 'object',
        [map]: Object.fromEntries(
          GEMINI_SCHEMA_REMOVALS.map((keyword) => [
            keyword,
            { type: 'string', additionalProperties: false, const: 'x' }
          ])
        )
      }
      const walked = withoutUnsupportedKeywords(schema) as Record<
        string,
        Record<string, Record<string, unknown>>
      >
      // Every authored name survives, whatever it is called…
      expect(Object.keys(walked[map]!).sort()).toEqual([...GEMINI_SCHEMA_REMOVALS].sort())
      // …and the removable keywords inside each child schema are still gone.
      for (const name of GEMINI_SCHEMA_REMOVALS) {
        expect(walked[map]![name]!.additionalProperties, name).toBeUndefined()
        expect(walked[map]![name]!.const, name).toBeUndefined()
        expect(walked[map]![name]!.type, name).toBe('string')
      }
    }
  )

  it('removes patternProperties whole, names and all, because it is on the list', () => {
    // The fifth name map is the one that does not survive: it is a removal in
    // its own right, so its keys never reach the wire at all. It is still
    // classified as a name map, because `keywordsSent` must not read a regular
    // expression somebody wrote as a schema keyword.
    const schema = { type: 'object', patternProperties: { '^const$': { type: 'string' } } }
    expect(withoutUnsupportedKeywords(schema)).toEqual({ type: 'object' })
    const sent = keywordsSent({ patternProperties: { const: { type: 'string' } } })
    expect(sent.has('patternProperties')).toBe(true)
    expect(sent.has('const')).toBe(false)
  })

  it('keeps a $ref resolvable, which is the point of the rule', () => {
    const schema = {
      type: 'object',
      definitions: { const: { type: 'string', additionalProperties: false } },
      properties: { x: { $ref: '#/definitions/const' } }
    }
    const walked = withoutUnsupportedKeywords(schema) as {
      definitions: Record<string, unknown>
      properties: { x: { $ref: string } }
    }
    expect(Object.keys(walked.definitions)).toEqual(['const'])
    expect(walked.properties.x.$ref).toBe('#/definitions/const')
  })

  it('reads a keyword under a name map as a name and not as a keyword sent', () => {
    // The classifier has to agree with the walk, or a refusal naming a
    // definition would be reported as a refusal about a schema keyword.
    const sent = keywordsSent({
      type: 'object',
      definitions: { pattern: { type: 'string' } },
      dependentRequired: { pattern: ['x'] }
    })
    expect(sent.has('definitions')).toBe(true)
    expect(sent.has('dependentRequired')).toBe(true)
    expect(sent.has('pattern')).toBe(false)
  })

  it('leaves a property actually named like a keyword alone', () => {
    // Under `properties` the keys are the author's words, not JSON Schema's. A
    // document with a member called `const` keeps it; the *schema* of that
    // member is still walked.
    const schema = {
      type: 'object',
      properties: {
        const: { type: 'string', additionalProperties: false },
        examples: { type: 'string' }
      }
    }
    const walked = withoutUnsupportedKeywords(schema) as {
      properties: Record<string, Record<string, unknown>>
    }
    expect(Object.keys(walked.properties).sort()).toEqual(['const', 'examples'])
    expect(walked.properties.const!.additionalProperties).toBeUndefined()
    expect(walked.properties.const!.type).toBe('string')
  })

  it('applies to the gemini family and to no other, over the runtime’s own five', () => {
    // The real subject: every schema `jpack mcp` serves declares
    // `additionalProperties: false`, so this is not a rule waiting for a
    // hypothetical.
    for (const tool of RECORDED_TOOLS) {
      expect(everyKey(tool.inputSchema).has('additionalProperties'), tool.name).toBe(true)
      expect(everyKey(servedSchemaFor('gemini', tool)).has('additionalProperties')).toBe(false)
      for (const family of ['openai-compatible', 'anthropic'] as const) {
        expect(servedSchemaFor(family, tool)).toBe(tool.inputSchema)
      }
    }
  })

  it('refuses a tool the runtime served without a schema, on this family too', () => {
    expect(() => servedSchemaFor('gemini', { name: 'a_tool' })).toThrow(/will not write/)
  })
})

describe('a keyword the list does not name, refused by an endpoint', () => {
  const schemas = [{ type: 'object', properties: { a: { type: 'string' } }, oneOf: [] }]

  it('is named, out of what this desk actually sent', () => {
    expect(refusedSchemaKeyword(400, 'Invalid JSON payload: unknown name "oneOf"', schemas)).toBe(
      'oneOf'
    )
  })

  it('reports nothing where the refusal names no keyword the desk sent', () => {
    // The lesson the tier's own classifier learned: "Unsupported parameter" is
    // a sentence an endpoint writes about anything at all, and a rule that read
    // the prose alone hid real failures behind a schema story.
    expect(refusedSchemaKeyword(400, 'The document was too large', schemas)).toBe('')
    expect(refusedSchemaKeyword(400, 'unknown name "patternProperties"', schemas)).toBe('')
    // And a status that is not 400 is not this.
    expect(refusedSchemaKeyword(500, 'unknown name "oneOf"', schemas)).toBe('')
  })

  it('prefers the longest name, so a keyword inside another is not reported', () => {
    const nested = [{ type: 'object', properties: { a: { type: 'string' } } }]
    // `properties` contains `type`; a shorter match first would send a reader
    // to the wrong word.
    expect(refusedSchemaKeyword(400, 'unknown name "properties"', nested)).toBe('properties')
  })

  it('never names a property the author wrote', () => {
    const authored = [{ type: 'object', properties: { rehearsal: { type: 'boolean' } } }]
    expect(keywordsSent(authored[0]).has('rehearsal')).toBe(false)
    expect(refusedSchemaKeyword(400, 'unknown name "rehearsal"', authored)).toBe('')
  })

  it('says it was an error and not a strip, and names the closed list', () => {
    const said = refusedSchemaSentence('oneOf')
    expect(said).toContain('"oneOf"')
    expect(said).toContain('closed')
    for (const keyword of GEMINI_SCHEMA_REMOVALS) expect(said).toContain(keyword)
    expect(said).toContain('nothing was written')
  })
})
