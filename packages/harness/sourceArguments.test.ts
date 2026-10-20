/**
 * A catalogued tool's arguments held to the schema `load_tool` loaded (ADR-0006): the subset of JSON
 * Schema the boundary refuses on, in the words it refuses with. `go/context/arguments_test.go` holds
 * the same cases to the same words.
 */
import { describe, expect, it } from 'vitest'
import { argumentProblem } from './sourceArguments'

const issue = {
  type: 'object',
  properties: {
    repository: { type: 'string', minLength: 1, maxLength: 20 },
    title: { type: 'string' },
    labels: { type: 'array', items: { type: 'string' }, minItems: 0, maxItems: 2 },
    priority: { type: 'integer', minimum: 1, maximum: 5 },
    state: { enum: ['open', 'closed'] },
    weight: { type: 'number' }
  },
  required: ['repository', 'title'],
  additionalProperties: false
}

describe('a catalogued tool’s arguments against the schema it loaded', () => {
  it('admits arguments that match, and ones the schema leaves open', () => {
    expect(argumentProblem(issue, { repository: 'expense', title: 'Missing mileage rate' })).toBeUndefined()
    expect(argumentProblem(issue, { repository: 'expense', title: 't', labels: ['a', 'b'], priority: 3, state: 'open', weight: 1 })).toBeUndefined()
    expect(argumentProblem({ type: 'object' }, { anything: [1, { deeper: null }] })).toBeUndefined()
    expect(argumentProblem(undefined, { a: 1 })).toBeUndefined()
    expect(argumentProblem(true, { a: 1 })).toBeUndefined()
  })

  it('refuses each way the arguments do not match, naming the member and the rule', () => {
    const cases: [unknown, string][] = [
      [[], 'the arguments must be object, not array'],
      [{ title: 't' }, '/repository is required and missing'],
      [{ repository: 'expense' }, '/title is required and missing'],
      [{ repository: 'expense', title: 7 }, '/title must be string, not integer'],
      [{ repository: '', title: 't' }, "/repository is shorter than the schema's 1 characters"],
      [{ repository: 'x'.repeat(21), title: 't' }, "/repository is longer than the schema's 20 characters"],
      [{ repository: 'expense', title: 't', labels: ['a', 2] }, '/labels/1 must be string, not integer'],
      [{ repository: 'expense', title: 't', labels: ['a', 'b', 'c'] }, "/labels has more than the schema's 2 items"],
      [{ repository: 'expense', title: 't', priority: 0 }, "/priority is below the schema's minimum of 1"],
      [{ repository: 'expense', title: 't', priority: 6 }, "/priority is above the schema's maximum of 5"],
      [{ repository: 'expense', title: 't', priority: 1.5 }, '/priority must be integer, not number'],
      [{ repository: 'expense', title: 't', state: 'merged' }, "/state is not one of the values the schema lists"],
      [{ repository: 'expense', title: 't', extra: true }, '/extra is not a member the schema names'],
      [{ repository: 'expense', title: 't', weight: 'heavy' }, '/weight must be number, not string']
    ]
    for (const [args, problem] of cases) expect(argumentProblem(issue, args)).toBe(problem)
  })

  it('reads a number as a number and an integer as both, a type list as any of its types, and null as its own', () => {
    expect(argumentProblem({ type: 'number' }, 3)).toBeUndefined()
    expect(argumentProblem({ type: ['string', 'null'] }, null)).toBeUndefined()
    expect(argumentProblem({ type: ['string', 'null'] }, 4)).toBe('the arguments must be string or null, not integer')
    expect(argumentProblem({ type: 'object', properties: { a: { const: 'x' } } }, { a: 'y' })).toBe('/a is not the value the schema requires')
  })

  it('reads allOf, anyOf and oneOf, and a member named with a slash as an escaped pointer', () => {
    const either = { anyOf: [{ type: 'object', required: ['a'] }, { type: 'object', required: ['b'] }] }
    expect(argumentProblem(either, { b: 1 })).toBeUndefined()
    expect(argumentProblem(either, {})).toBe("the arguments matches none of the schema's alternatives (/a is required and missing)")
    expect(argumentProblem({ oneOf: [{ type: 'string' }, { type: 'integer' }] }, true)).toBe("the arguments matches none of the schema's alternatives (the arguments must be string, not boolean)")
    expect(argumentProblem({ allOf: [{ type: 'object' }, { required: ['a'] }] }, {})).toBe('/a is required and missing')
    expect(argumentProblem({ type: 'object', properties: { 'a/b': { type: 'string' } } }, { 'a/b': 1 })).toBe('/a~1b must be string, not integer')
  })

  it('leaves to the host what it does not read: pattern, $ref, format and the like, and a schema nested past sixteen levels', () => {
    expect(argumentProblem({ type: 'object', properties: { a: { type: 'string', pattern: '^x$', format: 'uri' }, b: { $ref: '#/$defs/b' } }, $defs: { b: { type: 'integer' } } }, { a: 'nope', b: 'not an integer' })).toBeUndefined()
    let deep: Record<string, unknown> = { type: 'integer' }
    let value: unknown = 'text'
    for (let level = 0; level < 20; level++) {
      deep = { type: 'object', properties: { next: deep } }
      value = { next: value }
    }
    expect(argumentProblem(deep, value)).toBeUndefined()
  })
})
