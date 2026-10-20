/**
 * A catalogued tool's arguments held to the schema `load_tool` loaded (ADR-0006).
 *
 * A catalogued tool is offered with an open schema, so nothing on the wire checks what the model
 * sends; the boundary does, before the host's `execute`. This is the subset of JSON Schema a tool's
 * arguments are held to, and it only ever refuses what it understands: `type`, `enum`, `const`,
 * `required`, `properties`, `additionalProperties`, `items`, `minItems`, `maxItems`, `minimum`,
 * `maximum`, `minLength`, `maxLength`, `allOf`, `anyOf` and `oneOf` (read as `anyOf`: a value that
 * matches two is not refused here). Any other keyword, `$ref` and `pattern` among them, is left to
 * the host, which validates what it executes. `go/context/arguments.go` is the same subset, and
 * `sourceArguments.test.ts` and `go/context/arguments_test.go` hold the same cases to the same words.
 */
import { canonicalJSON } from './context/chat'

/** How deep a schema is read; past it the schema asks nothing. */
const DEPTH = 16

const object = (value: unknown): value is Record<string, unknown> =>
  typeof value === 'object' && value !== null && !Array.isArray(value)

const kind = (value: unknown): string =>
  value === null ? 'null' : Array.isArray(value) ? 'array' : typeof value === 'number' ? (Number.isInteger(value) ? 'integer' : 'number') : typeof value

/** Whether `value` is of the JSON Schema type `type` (an integer is also a number). */
function isType(value: unknown, type: unknown): boolean {
  const actual = kind(value)
  return actual === type || (type === 'number' && actual === 'integer')
}

const at = (path: string, name: string): string => `${path}/${name.replace(/~/g, '~0').replace(/\//g, '~1')}`
const where = (path: string): string => (path === '' ? 'the arguments' : path)

/** The first way `value` does not match `schema`, in words, or `undefined` where it does. */
export function argumentProblem(schema: unknown, value: unknown, path = '', depth = 0): string | undefined {
  if (!object(schema) || depth >= DEPTH) return undefined
  const types = schema.type === undefined ? undefined : Array.isArray(schema.type) ? schema.type : [schema.type]
  if (types !== undefined && !types.some((type) => isType(value, type))) {
    return `${where(path)} must be ${types.join(' or ')}, not ${kind(value)}`
  }
  if (Array.isArray(schema.enum) && !schema.enum.some((member) => canonicalJSON(member) === canonicalJSON(value))) {
    return `${where(path)} is not one of the values the schema lists`
  }
  if ('const' in schema && canonicalJSON(schema.const) !== canonicalJSON(value)) return `${where(path)} is not the value the schema requires`
  if (Array.isArray(schema.allOf)) {
    for (const part of schema.allOf) {
      const problem = argumentProblem(part, value, path, depth + 1)
      if (problem !== undefined) return problem
    }
  }
  for (const choice of [schema.anyOf, schema.oneOf]) {
    if (!Array.isArray(choice) || choice.length === 0) continue
    const problems = choice.map((part) => argumentProblem(part, value, path, depth + 1))
    if (!problems.some((problem) => problem === undefined)) return `${where(path)} matches none of the schema's alternatives (${problems[0]})`
  }
  if (typeof value === 'number') {
    if (typeof schema.minimum === 'number' && value < schema.minimum) return `${where(path)} is below the schema's minimum of ${schema.minimum}`
    if (typeof schema.maximum === 'number' && value > schema.maximum) return `${where(path)} is above the schema's maximum of ${schema.maximum}`
  }
  if (typeof value === 'string') {
    const length = [...value].length
    if (typeof schema.minLength === 'number' && length < schema.minLength) return `${where(path)} is shorter than the schema's ${schema.minLength} characters`
    if (typeof schema.maxLength === 'number' && length > schema.maxLength) return `${where(path)} is longer than the schema's ${schema.maxLength} characters`
  }
  if (Array.isArray(value)) {
    if (typeof schema.minItems === 'number' && value.length < schema.minItems) return `${where(path)} has fewer than the schema's ${schema.minItems} items`
    if (typeof schema.maxItems === 'number' && value.length > schema.maxItems) return `${where(path)} has more than the schema's ${schema.maxItems} items`
    if (object(schema.items)) {
      for (const [index, item] of value.entries()) {
        const problem = argumentProblem(schema.items, item, at(path, String(index)), depth + 1)
        if (problem !== undefined) return problem
      }
    }
  }
  if (object(value)) {
    if (Array.isArray(schema.required)) {
      for (const name of schema.required) {
        if (typeof name === 'string' && !Object.hasOwn(value, name)) return `${at(path, name)} is required and missing`
      }
    }
    const properties = object(schema.properties) ? schema.properties : {}
    for (const [name, member] of Object.entries(value)) {
      if (Object.hasOwn(properties, name)) {
        const problem = argumentProblem(properties[name], member, at(path, name), depth + 1)
        if (problem !== undefined) return problem
      } else if (schema.additionalProperties === false && !object(schema.patternProperties)) {
        return `${at(path, name)} is not a member the schema names`
      } else if (object(schema.additionalProperties)) {
        const problem = argumentProblem(schema.additionalProperties, member, at(path, name), depth + 1)
        if (problem !== undefined) return problem
      }
    }
  }
  return undefined
}
