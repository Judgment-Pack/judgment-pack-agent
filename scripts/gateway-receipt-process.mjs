// Linux session ownership and bounded teardown for the controlled receipt fixtures.
import {spawn} from 'node:child_process'
import {existsSync, readFileSync, readdirSync, readlinkSync, realpathSync, writeFileSync} from 'node:fs'
import {setTimeout as delay} from 'node:timers/promises'

const DEFAULT_LIMIT = 256 * 1024
const now = () => performance.now()
function processIdentity(pid) {
  try {
    const text = readFileSync(`/proc/${pid}/stat`, 'utf8')
    const fields = text.slice(text.lastIndexOf(')') + 2).split(' ')
    return {pid, state: fields[0], parent: Number(fields[1]), group: Number(fields[2]), session: Number(fields[3]), start: fields[19]}
  } catch (error) {
    if (error.code === 'ENOENT' || error.code === 'ESRCH') return undefined
    throw error
  }
}
function sessionMembers(session) {
  return readdirSync('/proc').filter(name => /^\d+$/.test(name))
    .map(name => processIdentity(Number(name)))
    .filter(item => item && item.session === session && item.state !== 'Z')
}
function boundedCapture(limit) {
  let tail = Buffer.alloc(0), bytes = 0
  return {
    add(chunk) { bytes += chunk.length; tail = Buffer.concat([tail, chunk]).subarray(-limit) },
    text() { return tail.toString('utf8') },
    get truncated() { return bytes > limit },
    get bytes() { return bytes },
  }
}
// Unlike a bare race with a sleeping promise, every completed wait clears its timer.
async function within(promise, milliseconds, expired) {
  let timer
  try { return await Promise.race([promise, new Promise(resolve => { timer = setTimeout(() => resolve(expired), Math.max(0, milliseconds)) })]) }
  finally { clearTimeout(timer) }
}
function combine(first, next, phase) {
  return first ? new AggregateError([first, next], first.message + '; ' + phase + ': ' + next.message) : next
}

export function startOwnedProcess(program, argv, {
  cwd, env = process.env, outputLimit = DEFAULT_LIMIT, forward = false,
  termWaitMs = 1500, killWaitMs = 1500, outputWaitMs = 1000, expectedExecutable,
  onProcessEvent = () => {},
} = {}) {
  if (process.platform !== 'linux') throw Error('receipt process ownership requires Linux /proc')
  // Gateway anchors use separate groups within this new session. The controlled
  // fixtures do not call setsid; arbitrary processes escaping the session are not contained.
  // Every signal targets one freshly verified PID, never a group or process pattern.
  const child = spawn(program, argv, {cwd, env, detached: true, stdio: ['ignore', 'pipe', 'pipe']})
  const stdout = boundedCapture(outputLimit), stderr = boundedCapture(outputLimit)
  let exitResult, forwarded = 0, cleanupPromise
  const exit = new Promise(resolve => {
    child.once('error', error => { exitResult = {error}; resolve(exitResult) })
    child.once('exit', (code, signal) => { exitResult = {code, signal}; record({event: 'exit', result: exitResult}); resolve(exitResult) })
  })
  const close = new Promise(resolve => child.once('close', resolve))
  for (const [stream, capture, destination] of [[child.stdout, stdout, process.stdout], [child.stderr, stderr, process.stderr]]) {
    stream.on('data', chunk => {
      capture.add(chunk)
      if (forward && forwarded < outputLimit) {
        const part = chunk.subarray(0, outputLimit - forwarded)
        destination.write(part); forwarded += part.length
      }
    })
  }
  const identity = child.pid ? processIdentity(child.pid) : undefined
  const observed = new Map()
  function record(event) { onProcessEvent({root: identity, ...event}) }
  record({event: 'start', program, argv})
  if (identity && identity.session !== child.pid) throw Error('detached child did not establish its own session')
  function members() {
    const active = identity ? sessionMembers(identity.session) : []
    for (const member of active) {
      const key = member.pid + ':' + member.start
      if (BigInt(member.start) < BigInt(identity.start)) throw Error('owned session contains an older unknown process: ' + member.pid)
      if (!observed.has(key)) { observed.set(key, member); record({event: 'observed', member}) }
    }
    return active
  }
  function signalOwned(member, signal) {
    const current = processIdentity(member.pid)
    if (!current || current.state === 'Z') return
    if (current.start !== member.start || current.group !== member.group || current.session !== identity.session) throw Error(`owned PID identity changed: ${member.pid}; no signal sent`)
    if (member.pid === child.pid && current.start !== identity.start) throw Error('child PID start identity changed; no signal sent')
    if (member.pid === child.pid && expectedExecutable && realpathSync(readlinkSync(`/proc/${member.pid}/exe`)) !== realpathSync(expectedExecutable)) throw Error('producer PID executable identity changed; no signal sent')
    try { process.kill(member.pid, signal); record({event: 'signal', member: current, signal}) }
    catch (error) { if (error.code !== 'ESRCH') throw error }
  }
  async function waitEmpty(milliseconds, signal) {
    const deadline = now() + milliseconds
    do {
      const active = members()
      if (!active.length) return true
      for (const member of active.reverse()) signalOwned(member, signal)
      await delay(Math.min(20, Math.max(1, deadline - now())))
    } while (now() < deadline)
    return members().length === 0
  }
  async function cleanup() {
    if (cleanupPromise) return cleanupPromise
    cleanupPromise = (async () => {
      try {
        if (!await waitEmpty(termWaitMs, 'SIGTERM') && !await waitEmpty(killWaitMs, 'SIGKILL')) throw Error('owned processes survived bounded SIGKILL wait: ' + JSON.stringify(members()))
        const missing = Symbol('missing output close')
        if (await within(close, outputWaitMs, missing) === missing) throw Error('owned child output did not close within bounded drain: ' + child.pid)
        if (!exitResult) throw Error('owned child exit event missing after bounded teardown: ' + child.pid)
        if (!child.stdout.readableEnded || !child.stderr.readableEnded) throw Error('owned child output ended incompletely: ' + child.pid)
      } finally {
        child.stdout.destroy(); child.stderr.destroy()
        let active, observationError
        try { active = members() } catch (error) { observationError = error.message }
        record({event: 'settled', observationError, rootFinal: identity ? processIdentity(identity.pid) ?? null : null, result: exitResult?.error ? {error: exitResult.error.message} : exitResult,
          observed: [...observed.values()].map(member => ({...member, final: processIdentity(member.pid) ?? null})),
          active, stdoutBytes: stdout.bytes, stderrBytes: stderr.bytes})
      }
    })()
    return cleanupPromise
  }
  return {child, exit, close, cleanup, members, stdout, stderr, get result() { return exitResult }}
}

export async function runCommand(program, argv, {
  cwd, env, milliseconds = 300000, signal, quiet = false,
  outputLimit = DEFAULT_LIMIT, requireCompleteOutput = false, ...options
} = {}) {
  signal?.throwIfAborted()
  if (!quiet) console.log('gateway receipts command: ' + JSON.stringify([program, ...argv]))
  const owned = startOwnedProcess(program, argv, {cwd, env, outputLimit, forward: !quiet, ...options})
  let timer, cancel, failure, result
  const interrupted = new Promise(resolve => {
    timer = setTimeout(() => resolve(Error(program + ' timed out after ' + milliseconds + 'ms')), milliseconds)
    cancel = () => resolve(signal.reason instanceof Error ? signal.reason : Error(String(signal.reason ?? program + ' cancelled')))
    signal?.addEventListener('abort', cancel, {once: true})
    if (signal?.aborted) cancel()
  })
  try {
    result = await Promise.race([owned.exit, interrupted])
    if (result instanceof Error) failure = result
    else if (result.error || result.code !== 0) failure = result.error ?? Error(program + ' exited ' + result.code + ' ' + (result.signal ?? ''))
    else if (owned.members().length) failure = Error(program + ' exited with active owned descendants')
  } finally {
    clearTimeout(timer); signal?.removeEventListener('abort', cancel)
    try { await owned.cleanup() } catch (error) { failure = combine(failure, error, 'cleanup') }
  }
  if (!failure && signal?.aborted) failure = signal.reason ?? Error(program + ' cancelled')
  if (!failure && requireCompleteOutput && (owned.stdout.truncated || owned.stderr.truncated)) failure = Error(program + ' identity output exceeded ' + outputLimit + ' bytes')
  if (failure) {
    failure.message += '\nstdout tail' + (owned.stdout.truncated ? ' (truncated)' : '') + ':\n' + owned.stdout.text() + '\nstderr tail' + (owned.stderr.truncated ? ' (truncated)' : '') + ':\n' + owned.stderr.text()
    throw failure
  }
  return owned.stdout.text()
}

// Only the lifecycle-bound command capability may start consumer children. Closing
// it is synchronous; a stalled callback may settle later but cannot start more work.
export async function runProducerSession(program, argv, {
  fixture, logPath, signal, runConsumers, milliseconds = 240000,
  readinessMs = 45000, handshakeMs = 10000,
  termWaitMs = 1500, killWaitMs = 1500, outputWaitMs = 1000,
  onProcessEvent = () => {}, ...options
}) {
  signal?.throwIfAborted()
  const deadline = now() + milliseconds
  const cleanupBudget = termWaitMs + killWaitMs + outputWaitMs
  const workDeadline = deadline - handshakeMs - 2 * cleanupBudget - 100
  if (workDeadline <= now()) throw Error('session budget cannot cover bounded teardown')
  const lifecycle = new AbortController(), pending = new Set(), events = []
  const record = event => { events.push(event); onProcessEvent(event) }
  const owned = startOwnedProcess(program, argv, {termWaitMs, killWaitMs, outputWaitMs, ...options, onProcessEvent: record})
  let failure, completed = false, closed = false, timer, cancel
  let interrupt
  const interrupted = new Promise(resolve => { interrupt = resolve })
  const closeAuthority = reason => {
    if (closed) return
    closed = true
    lifecycle.abort(reason)
  }
  const stopWork = reason => { closeAuthority(reason); interrupt(reason) }
  timer = setTimeout(() => stopWork(Error('bounded consumer runner deadline expired')), Math.max(0, workDeadline - now()))
  cancel = () => stopWork(signal.reason instanceof Error ? signal.reason : Error(String(signal.reason ?? 'producer session cancelled')))
  signal?.addEventListener('abort', cancel, {once: true})
  if (signal?.aborted) cancel()
  const remaining = () => {
    lifecycle.signal.throwIfAborted()
    const left = workDeadline - now()
    if (left <= 0) throw Error('bounded consumer runner deadline expired')
    return left
  }
  const checkAlive = () => {
    remaining()
    if (owned.result) throw Error('producer ended before stop handshake: ' + JSON.stringify(owned.result))
  }
  const command = (program, argv, commandOptions = {}) => {
    if (closed) return Promise.reject(Error('consumer spawn authority is closed'))
    const work = runCommand(program, argv, {...commandOptions,
      termWaitMs, killWaitMs, outputWaitMs, signal: lifecycle.signal,
      milliseconds: Math.min(commandOptions.milliseconds ?? 90000, remaining()), onProcessEvent: record})
    pending.add(work)
    // Retain settled failures too: an unawaited rejection cannot disappear before join.
    work.catch(() => {})
    return work
  }
  try {
    const work = (async () => {
      const startup = now() + Math.min(readinessMs, remaining())
      while (!existsSync(fixture)) {
        checkAlive()
        if (now() >= startup) throw Error('producer readiness deadline expired')
        await delay(Math.min(20, Math.max(1, startup - now())))
      }
      checkAlive()
      await runConsumers({remaining, checkAlive, command, signal: lifecycle.signal})
      checkAlive()
      return null
    })()
    const result = await Promise.race([work, interrupted])
    if (result instanceof Error) throw result
    completed = true
  } catch (error) { failure = error }
  finally {
    closeAuthority(failure ?? Error('producer consumer work completed'))
    clearTimeout(timer); signal?.removeEventListener('abort', cancel)
    // Every admitted command is retained until its owned cleanup has settled.
    const joined = await Promise.allSettled([...pending])
    for (const result of joined) if (result.status === 'rejected' && result.reason !== lifecycle.signal.reason) failure = combine(failure, result.reason, 'consumer cleanup')
    try {
      writeFileSync(fixture + '.stop', 'stop\n', {mode: 0o600})
      const missing = Symbol('missing handshake')
      if (await within(owned.close, Math.max(0, Math.min(handshakeMs, deadline - now() - cleanupBudget)), missing) === missing) throw Error('producer failed explicit stop handshake; bounded own-PID termination required')
      if (owned.result?.error || owned.result?.code !== 0 || !owned.stdout.text().includes('consumer stop handshake received')) throw Error('producer did not pass explicit stop handshake')
      if (owned.stdout.truncated || owned.stderr.truncated) throw Error('producer handshake output exceeded capture limit')
      if (owned.members().length) throw Error('producer exited with active owned descendants')
    } catch (error) { failure = combine(failure, error, 'producer stop') }
    try { await owned.cleanup() }
    catch (error) { failure = combine(failure, error, 'producer cleanup') }
    const log = (owned.stdout.truncated ? '[stdout tail truncated]\n' : '') + owned.stdout.text() + (owned.stderr.truncated ? '[stderr tail truncated]\n' : '') + owned.stderr.text()
      + '\n[process lifecycle] ' + JSON.stringify(events) + '\n'
    if (logPath) writeFileSync(logPath, log, {mode: 0o600})
    if (failure) failure.message += '\nproducer log tail:\n' + log
    else process.stdout.write(log)
  }
  if (failure) throw failure
  if (!completed) throw Error('producer consumer callback did not complete')
}
