// npm's lockfile audit covers every workspace, including development dependencies.
// Never publish npm's raw stdout/stderr: registry errors can contain credentials.
import { spawnSync } from 'node:child_process'
import { appendFileSync } from 'node:fs'
import { pathToFileURL } from 'node:url'

const severities = ['info', 'low', 'moderate', 'high', 'critical']
const object = value => value !== null && typeof value === 'object' && !Array.isArray(value)
const invalid = () => { throw new Error('invalid audit evidence') }

export function parseReport(text) {
  const report = JSON.parse(text)
  if (!object(report) || report.auditReportVersion !== 2 || 'error' in report ||
      !object(report.vulnerabilities) || !object(report.metadata?.vulnerabilities)) invalid()
  const counts = report.metadata.vulnerabilities
  const actual = Object.fromEntries(severities.map(key => [key, 0]))
  const advisories = new Set()
  const entries = Object.entries(report.vulnerabilities)
  for (const [name, entry] of entries) {
    if (!object(entry) || entry.name !== name || !severities.includes(entry.severity) ||
        !Array.isArray(entry.via) || entry.via.length === 0 ||
        !Array.isArray(entry.nodes) || entry.nodes.length === 0 ||
        !entry.nodes.every(node => typeof node === 'string' && node.length > 0)) invalid()
    actual[entry.severity]++
    for (const via of entry.via) {
      if (typeof via === 'string') {
        if (!Object.hasOwn(report.vulnerabilities, via)) invalid()
      } else {
        if (!object(via) || !Number.isSafeInteger(via.source) || via.source <= 0 ||
            typeof via.name !== 'string' || !severities.includes(via.severity) ||
            typeof via.url !== 'string' || typeof via.range !== 'string') invalid()
        const id = /^https:\/\/github\.com\/advisories\/(GHSA-[23456789cfghjmpqrvwx]{4}-[23456789cfghjmpqrvwx]{4}-[23456789cfghjmpqrvwx]{4})$/.exec(via.url)
        if (id) advisories.add(id[1])
      }
    }
  }
  if (counts.total !== entries.length ||
      severities.some(key => actual[key] !== counts[key])) invalid()
  return { total: counts.total, counts: actual, advisories: [...advisories].sort() }
}

export function audit(runner = spawnSync) {
  try {
    const result = runner('npm', ['audit', '--json', '--package-lock-only', '--ignore-scripts',
      '--include=dev', '--include=optional', '--include=peer', '--audit-level=low',
      '--registry=https://registry.npmjs.org'],
    { encoding: 'utf8', timeout: 120_000, maxBuffer: 16 * 1024 * 1024 })
    if (result.error || result.signal || ![0, 1].includes(result.status)) invalid()
    const report = parseReport(result.stdout)
    // npm exit 1 is evidence of findings, never a successful clean result.
    if ((result.status === 0) !== (report.total === 0)) invalid()
    return { status: report.total ? 'findings' : 'clean', exitCode: report.total ? 1 : 0, ...report }
  } catch {
    return { status: 'unavailable', exitCode: 2 }
  }
}

export function formatResult(result) {
  const lines = [`dependency audit: ${result.status}`,
    'Coverage: npm package-lock.json, all workspaces, production/development/optional/peer dependencies.',
    'Go modules, Python packages, OS packages and unpublished advisories are outside this audit.']
  if (result.status !== 'unavailable') {
    lines.push(`Affected packages: ${result.total}; ${severities.map(key => `${key}=${result.counts[key]}`).join(', ')}.`)
    lines.push(`GitHub advisories: ${result.advisories.join(', ') || 'none reported'}.`)
  } else lines.push('Audit evidence is unavailable or malformed; no clean result is established.')
  lines.push('Disposition: see docs/security/dependency-policy.md; findings are never suppressed by this job.')
  return lines.join('\n') + '\n'
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const result = audit()
  const summary = formatResult(result)
  process.stdout.write(summary)
  if (process.env.GITHUB_STEP_SUMMARY) appendFileSync(process.env.GITHUB_STEP_SUMMARY, summary)
  process.exitCode = result.exitCode
}
