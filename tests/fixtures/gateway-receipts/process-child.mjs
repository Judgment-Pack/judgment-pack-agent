// Disposable handwritten process fixtures; no compiler or live producer required.
import {spawn} from 'node:child_process'
import {existsSync, readFileSync, writeFileSync} from 'node:fs'
const [mode, path] = process.argv.slice(2)
function identity(pid) {
  const text = readFileSync(`/proc/${pid}/stat`, 'utf8')
  const fields = text.slice(text.lastIndexOf(')') + 2).split(' ')
  return {pid, state: fields[0], group: Number(fields[2]), session: Number(fields[3]), start: fields[19]}
}
if (mode === 'success') { console.log('normal success'); process.exit(0) }
if (mode === 'failure') { console.error('deliberate child failure'); process.exit(7) }
if (mode === 'output') { process.stdout.write('x'.repeat(32768) + 'TAIL'); process.exit(0) }
if (mode === 'ignores-term') {
  process.on('SIGTERM', () => {})
  console.log('ignoring SIGTERM pid=' + process.pid)
  if (path) writeFileSync(path, 'ready')
  setInterval(() => {}, 1000)
}
if (mode === 'descendant' || mode === 'separate-group') {
  const nested = mode === 'separate-group'
    ? spawn('/usr/bin/python3', ['-c', 'import os,sys;os.setpgid(0,0);os.execv(sys.argv[1],sys.argv[1:])', process.execPath, new URL(import.meta.url).pathname, 'ignores-term'], {stdio: 'ignore'})
    : spawn(process.execPath, [new URL(import.meta.url).pathname, 'ignores-term'], {stdio: 'ignore'})
  console.log('descendant pid=' + nested.pid)
  // Handwritten topology: inherited session, optionally a separate process group.
  setTimeout(() => {
    if (process.env.RECEIPT_TEST_DESCENDANT_IDENTITY) writeFileSync(process.env.RECEIPT_TEST_DESCENDANT_IDENTITY, JSON.stringify(identity(nested.pid)))
    process.exit(0)
  }, 150)
}
if (mode === 'delayed-handshake') {
  writeFileSync(path, JSON.stringify(identity(process.pid)))
  setTimeout(() => { console.log('marker delivered after producer exit; consumer stop handshake received'); process.exit(0) }, 80)
}
if (mode.startsWith('producer')) {
  process.on('SIGTERM', () => {})
  if (mode !== 'producer-no-ready') writeFileSync(path, JSON.stringify({pid: process.pid}))
  setInterval(() => {
    if (mode !== 'producer-ignores-stop' && existsSync(path + '.stop')) {
      if (mode === 'producer-delayed-handshake') {
        spawn(process.execPath, [new URL(import.meta.url).pathname, 'delayed-handshake', path + '.pipe-child.json'], {stdio: ['ignore', 'inherit', 'inherit']})
        process.exit(0)
      }
      const message = (mode === 'producer-chatty' ? 'x'.repeat(4096) : '') + (mode === 'producer-bad-handshake' ? 'missing marker' : 'consumer stop handshake received') + '\n'
      process.stdout.write(message, () => process.exit(0))
    }
  }, 10)
}
