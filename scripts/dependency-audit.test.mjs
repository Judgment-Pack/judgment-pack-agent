import assert from 'node:assert/strict'
import { test } from 'node:test'
import { audit, formatResult, parseReport } from './dependency-audit.mjs'

// Hand authored npm audit v2 examples, never captured from npm or the parser.
const clean = () => ({ auditReportVersion: 2, vulnerabilities: {}, metadata: {
  vulnerabilities: { info: 0, low: 0, moderate: 0, high: 0, critical: 0, total: 0 },
} })
const finding = () => {
  const report = clean()
  report.vulnerabilities.example = { name: 'example', severity: 'high', nodes: ['node_modules/example'],
    via: [{ source: 123, name: 'example', severity: 'high', range: '<2.0.0',
      url: 'https://github.com/advisories/GHSA-2345-6789-cfgh' }] }
  report.metadata.vulnerabilities.high = 1
  report.metadata.vulnerabilities.total = 1
  return report
}
const run = (report, status = 0, extra = {}) => audit(() => ({ stdout: JSON.stringify(report), status, ...extra }))

test('clean evidence and findings have distinct failing/successful statuses', () => {
  assert.equal(run(clean()).status, 'clean')
  assert.equal(run(clean()).exitCode, 0)
  const result = run(finding(), 1)
  assert.equal(result.status, 'findings')
  assert.equal(result.exitCode, 1)
  assert.deepEqual(result.advisories, ['GHSA-2345-6789-cfgh'])
  const transitive = finding()
  transitive.vulnerabilities.parent = { name: 'parent', severity: 'high', via: ['example'], nodes: ['node_modules/parent'] }
  transitive.metadata.vulnerabilities.high = 2
  transitive.metadata.vulnerabilities.total = 2
  assert.equal(run(transitive, 1).total, 2)
})

test('empty, missing, oversized and malformed evidence refuses without exposing raw data', () => {
  for (const stdout of ['', '{', '{}', JSON.stringify({ error: { summary: 'private-token' } })]) {
    assert.throws(() => parseReport(stdout))
    const result = audit(() => ({ stdout, stderr: 'private-token', status: 0 }))
    assert.equal(result.status, 'unavailable')
    assert.equal(result.exitCode, 2)
    assert.doesNotMatch(formatResult(result), /private-token/)
  }
  for (const extra of [{ error: new Error('private-token') }, { error: { code: 'ENOBUFS' } },
    { signal: 'SIGTERM' }, { status: null }, { status: 2 }]) {
    assert.equal(run(clean(), 0, extra).status, 'unavailable')
  }
  assert.equal(audit(() => { throw new Error('private-token') }).exitCode, 2)
})

test('inconsistent exit status, counts, version and advisory shape refuse', () => {
  assert.equal(run(clean(), 1).status, 'unavailable')
  assert.equal(run(finding(), 0).status, 'unavailable')
  for (const change of [
    r => { r.error = {} }, r => { r.auditReportVersion = 1 },
    r => { delete r.metadata.vulnerabilities.high },
    r => { r.metadata.vulnerabilities.high = -1 },
    r => { r.metadata.vulnerabilities.total = 0 },
    r => { r.metadata.vulnerabilities.high = 0 },
    r => { r.vulnerabilities.example.via = [] },
    r => { r.vulnerabilities.example.via = ['missing'] },
    r => { r.vulnerabilities.example.via[0].source = '123' },
    r => { r.vulnerabilities.example.nodes = [] },
    r => { r.vulnerabilities.example.severity = 'unknown' },
  ]) {
    const report = finding(); change(report)
    assert.equal(run(report, 1).status, 'unavailable')
  }
})

test('invocation covers the lockfile without installation and bounds captured output', () => {
  const result = audit((command, args, options) => {
    assert.equal(command, 'npm')
    for (const arg of ['audit', '--package-lock-only', '--ignore-scripts', '--include=dev',
      '--include=optional', '--include=peer', '--audit-level=low', '--registry=https://registry.npmjs.org']) assert.ok(args.includes(arg), arg)
    assert.equal(options.timeout, 120_000)
    assert.equal(options.maxBuffer, 16 * 1024 * 1024)
    return { stdout: JSON.stringify(clean()), status: 0 }
  })
  assert.equal(result.status, 'clean')
})

test('public summary contains only validated counts and advisory identifiers', () => {
  const report = finding()
  report.vulnerabilities.example.via[0].url = 'https://registry.example/private-token'
  report.vulnerabilities.example.nodes = ['private-token']
  report.vulnerabilities.example.via[0].title = 'private-token'
  const result = run(report, 1)
  assert.equal(result.status, 'findings')
  assert.doesNotMatch(formatResult(result), /private-token|registry\.example/)
  assert.match(formatResult(result), /findings are never suppressed/)
})
