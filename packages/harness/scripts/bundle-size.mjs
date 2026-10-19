#!/usr/bin/env node
/**
 * Measure what each browser engine adds to a page (ADR-0003, decision 6; the package README).
 *
 *   npm run bundle:size --workspace @judgment-pack/harness
 *
 * One Vite build (the package's own Vite) of the two engines as two entries, for the browser, minified
 * (Vite's default), each entry keeping its exports, with nothing written: the chunks only `deepagents` needs, the chunks only `vercel`
 * needs, and the chunks both import (the harness's core and their common dependencies), which a host
 * that already ships one already ships. Sizes are Vite's own: kilobytes of 1,000 bytes, and the
 * gzip size of each chunk. The build also prints the Node built-ins the browser build replaces with an
 * empty module, as Vite names them.
 *
 * The settings are the ones the figures in ADR-0003 and the README were taken with: no plugin but one
 * that stands an empty module in for `ws`, an optional peer of LangSmith's client that no page loads;
 * no `define`, alias, manual chunking or other minifier setting; no config file. A change to this
 * file or to a pinned dependency moves the figures, and the ADR and the README say so where they cite them.
 */
import { gzipSync } from 'node:zlib'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { build } from 'vite'

const PACKAGE = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const ENTRIES = { deepagents: 'engines/deepagents/index.ts', vercel: 'engines/vercel/index.ts' }
const EMPTY = '\0empty-module:'

const warnings = []
const logger = {
  hasWarned: false,
  info() {},
  warn(message) {
    warnings.push(message)
  },
  warnOnce(message) {
    warnings.push(message)
  },
  error(message) {
    console.error(message)
  },
  clearScreen() {},
  hasErrorLogged: () => false
}

const output = await build({
  root: PACKAGE,
  configFile: false,
  logLevel: 'warn',
  customLogger: logger,
  plugins: [
    {
      name: 'empty-ws',
      enforce: 'pre',
      resolveId: (id) => (id === 'ws' ? EMPTY + id : null),
      load: (id) => (id.startsWith(EMPTY) ? 'export default undefined' : null)
    }
  ],
  build: {
    write: false,
    reportCompressedSize: false,
    rollupOptions: {
      preserveEntrySignatures: 'strict',
      input: Object.fromEntries(Object.entries(ENTRIES).map(([name, file]) => [name, resolve(PACKAGE, file)]))
    }
  }
})

const chunks = (Array.isArray(output) ? output.flatMap((one) => one.output) : output.output).filter((file) => file.type === 'chunk')
const byFile = new Map(chunks.map((chunk) => [chunk.fileName, chunk]))

/** The chunks an entry reaches by static import, itself included. */
function reached(entry) {
  const seen = new Set()
  const visit = (name) => {
    if (seen.has(name) || !byFile.has(name)) return
    seen.add(name)
    for (const next of byFile.get(name).imports) visit(next)
  }
  visit(entry)
  return seen
}

const roots = Object.fromEntries(Object.keys(ENTRIES).map((name) => [name, chunks.find((chunk) => chunk.isEntry && chunk.name === name)?.fileName]))
for (const [name, file] of Object.entries(roots)) if (file === undefined) throw new Error(`the build has no entry chunk for ${name}`)
const sets = Object.fromEntries(Object.entries(roots).map(([name, file]) => [name, reached(file)]))

const kB = (bytes) => (bytes / 1000).toFixed(1)
const sizes = (names) => {
  let raw = 0
  let gz = 0
  for (const name of names) {
    const code = Buffer.from(byFile.get(name).code)
    raw += code.length
    gz += gzipSync(code).length
  }
  return { raw, gz }
}
const row = (label, names) => {
  const { raw, gz } = sizes(names)
  console.log(`${label.padEnd(24)} ${kB(raw).padStart(8)} kB  ${kB(gz).padStart(8)} kB gzip  (${names.length} chunk${names.length === 1 ? '' : 's'})`)
}

const [a, b] = Object.keys(ENTRIES)
// Each chunk, by the entries that reach it (the hash in a chunk's file name is left off).
for (const chunk of chunks) {
  const reachedBy = Object.keys(ENTRIES).filter((name) => sets[name].has(chunk.fileName))
  const { raw, gz } = sizes([chunk.fileName])
  const label = chunk.fileName.replace(/^assets\//, '').replace(/-[A-Za-z0-9_]{8}\.js$/, '.js')
  console.log(`  ${label.padEnd(30)} ${kB(raw).padStart(8)} kB  ${kB(gz).padStart(8)} kB gzip  reached by ${reachedBy.join(' and ') || 'neither'}`)
}
console.log()
const only = (x, y) => [...sets[x]].filter((name) => !sets[y].has(name))
row(`only ${a}`, only(a, b))
row(`only ${b}`, only(b, a))
row('shared by both', [...sets[a]].filter((name) => sets[b].has(name)))

const externalized = [...new Set(warnings.flatMap((text) => [...text.matchAll(/Module "([^"]+)" has been externalized/g)].map((match) => match[1])))].sort()
console.log(`\nNode built-ins replaced with an empty module (${externalized.length}): ${externalized.join(', ') || 'none'}`)
const others = warnings.filter((text) => !/has been externalized/.test(text))
if (others.length > 0) console.log(`\nOther warnings (${others.length}):\n${others.join('\n')}`)
