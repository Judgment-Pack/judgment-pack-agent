import { describe, expect, it } from 'vitest'
import vector from '../../contract/vectors/tool-offer/descriptor-snapshot.json'
import { DESCRIPTOR_SNAPSHOT_BYTES, descriptorSnapshotDigest, readDescriptorJSON, readDescriptorSnapshot } from './descriptorSnapshot'
const bytes = (text: string) => Uint8Array.from(new TextEncoder().encode(text))

describe('Gateway descriptor snapshot bytes (agent#168)', () => {
  it('reads the same handwritten snapshot and complete-byte digest as Go and pinned Gateway', async () => {
    expect(await descriptorSnapshotDigest(bytes(vector.snapshotText))).toBe(vector.digest)
    expect(await descriptorSnapshotDigest(bytes(vector.changedText))).toBe(vector.changedDigest)
    expect(vector.changedDigest).not.toBe(vector.digest)
    const snapshot = readDescriptorSnapshot(bytes(vector.snapshotText))
    expect(snapshot.tools.search_tickets!.inputSchemaText).toContain('"minimum": 1.0')
    expect(Object.keys(snapshot.tools).slice(-2)).toEqual(['�', '𐀀'])
    for (const [old, replacement] of [['2026-10-24T01:02:03Z', '2026-10-24T01:02:04Z'], ['tickets-mcp', 'other-mcp'], ['tickets.example', 'other.example'], ['"minimum\\": 1.0', '"minimum\\": 1']]) {
      const changed = vector.snapshotText.replace(old!, replacement!)
      expect(changed).not.toBe(vector.snapshotText)
      expect(await descriptorSnapshotDigest(bytes(changed))).not.toBe(vector.digest)
    }
  })
  for (const row of vector.invalid) it(`refuses ${row.name}`, () => {
    expect(() => readDescriptorSnapshot(bytes(vector.snapshotText.replace(row.old, row.new)))).toThrow('descriptor snapshot:')
  })
  it('refuses empty, trailing, oversized and invalid UTF-8 inputs', () => {
    for (const data of [new Uint8Array(), bytes(vector.snapshotText + '\n'), bytes(vector.snapshotText + '{}'), bytes(vector.snapshotText.replace('Search tickets', 'x'.repeat(DESCRIPTOR_SNAPSHOT_BYTES))), new Uint8Array([0xff]), new Uint8Array([0xef, 0xbb, 0xbf, ...bytes(vector.snapshotText)])]) expect(() => readDescriptorSnapshot(data)).toThrow('descriptor snapshot:')
  })
  it('keeps captured schema text opaque in the snapshot reader', async () => {
    const opaque = vector.snapshotText.replace('{\\"type\\":\\"object\\"}', 'not JSON')
    expect(readDescriptorSnapshot(bytes(opaque)).tools.close!.inputSchemaText).toBe('not JSON')
    expect(await descriptorSnapshotDigest(bytes(opaque))).not.toBe(vector.digest)
  })
  it('refuses raw fractional and exponent spellings in the Gateway integer domain', () => {
    for (const text of ['1.0', '1e0', '9007199254740992']) expect(() => readDescriptorJSON(text, true)).toThrow()
    expect(readDescriptorJSON('-0', true)).toBe(-0)
  })
  it('reads schema numbers but refuses duplicate names and malformed Unicode before offering', () => {
    expect(readDescriptorJSON('{"minimum":1.0,"maximum":1e2}')).toEqual({ minimum: 1, maximum: 100 })
    for (const text of ['{"type":"object","type":"string"}', '{"x":"\\ud800"}', '{"x":"\\udc00"}', '{"x":"a\\"}', '{"x":true}{}', '{"minimum":1e999}']) expect(() => readDescriptorJSON(text)).toThrow()
  })
  it('accepts Gateway platform graphic characters and endpoint forms without host Unicode drift', () => {
    for (const name of ['tickets', 'café', '𐀀']) expect(readDescriptorSnapshot(bytes(vector.snapshotText.replace('"platform":"tickets"', `"platform":"${name}"`))).platform).toBe(name)
    for (const endpoint of ['https://example.test', 'https://[::ffff:192.0.2.1]:443/path%20name', 'http://127.1.2.3:80', 'http://[::1]/mcp']) expect(readDescriptorSnapshot(bytes(vector.snapshotText.replace('https://tickets.example/mcp', endpoint))).endpoint).toBe(endpoint)
    for (const endpoint of ['https://user@example.test', 'https://example.test:0', 'https://example.test:01', 'https://example.test/%zz', 'https://[::ffff:192.000.2.1]', 'http://localhost', 'https://example.test?x=1']) expect(() => readDescriptorSnapshot(bytes(vector.snapshotText.replace('https://tickets.example/mcp', endpoint)))).toThrow()
  })
})
