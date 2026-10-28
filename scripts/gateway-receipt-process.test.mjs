import test from 'node:test'
import assert from 'node:assert/strict'
import {existsSync, mkdtempSync, readFileSync} from 'node:fs'
import {tmpdir} from 'node:os'
import {join} from 'node:path'
import {fileURLToPath} from 'node:url'
import {setTimeout as delay} from 'node:timers/promises'
import {runCommand, runProducerSession} from './gateway-receipt-process.mjs'

const fixture = fileURLToPath(new URL('../tests/fixtures/gateway-receipts/process-child.mjs', import.meta.url))
const events = []
const onProcessEvent = event => events.push(event)
test.after(() => console.log('exact disposable process lifecycle: ' + JSON.stringify(events)))
const fast = {quiet: true, termWaitMs: 80, killWaitMs: 150, outputWaitMs: 100, onProcessEvent}
function gone(pid) {
  try { return readFileSync(`/proc/${pid}/stat`, 'utf8').split(') ')[1].startsWith('Z ') }
  catch (error) { if (error.code === 'ENOENT') return true; throw error }
}
async function fixtureCommand(mode, options = {}) {
  return runCommand(process.execPath, [fixture, mode], {...fast, ...options})
}
function producerOptions(mode = 'producer') {
  const work = mkdtempSync(join(tmpdir(), 'receipt-process-test-'))
  return {fixture: join(work, 'ready.json'), logPath: join(work, 'producer.log'), mode,
    milliseconds: 3500, readinessMs: 1000, handshakeMs: 120,
    termWaitMs: 80, killWaitMs: 150, outputWaitMs: 100, onProcessEvent, expectedExecutable: process.execPath}
}
async function session(options) {
  return runProducerSession(process.execPath, [fixture, options.mode, options.fixture], options)
}

test('normal success and nonzero exit preserve actual command diagnostics', async () => {
  assert.equal(await fixtureCommand('success'), 'normal success\n')
  await assert.rejects(fixtureCommand('failure'), error => error.message.includes('exited 7') && error.message.includes('deliberate child failure'))
})
test('timeout escalates for a SIGTERM-ignoring child and never becomes success', async () => {
  const start = Date.now()
  let pid
  await assert.rejects(fixtureCommand('ignores-term', {milliseconds: 350}), error => {
    pid = Number(error.message.match(/ignoring SIGTERM pid=(\d+)/)?.[1])
    return error.message.includes('timed out after 350ms') && pid > 0
  })
  assert.ok(Date.now() - start < 1500, 'bounded timeout must settle after SIGKILL escalation')
  assert.ok(gone(pid), 'timed out child must be gone')
})
test('cancellation tears down a SIGTERM-ignoring child and preserves reason', async () => {
  const controller = new AbortController()
  const cancel = setTimeout(() => controller.abort(Error('fixture cancellation')), 350)
  let pid
  try {
    await assert.rejects(fixtureCommand('ignores-term', {signal: controller.signal}), error => {
      pid = Number(error.message.match(/ignoring SIGTERM pid=(\d+)/)?.[1])
      return error.message.includes('fixture cancellation') && pid > 0
    })
  } finally { clearTimeout(cancel) }
  assert.ok(gone(pid), 'cancelled child must be gone')
})
test('already cancelled commands never spawn', async () => {
  const controller = new AbortController(); controller.abort(Error('already cancelled'))
  await assert.rejects(fixtureCommand('success', {signal: controller.signal}), /already cancelled/)
})
test('orphaned descendants are accounted for and prevent successful commands', async () => {
  let pid
  await assert.rejects(fixtureCommand('descendant'), error => {
    pid = Number(error.message.match(/descendant pid=(\d+)/)?.[1])
    return error.message.includes('active owned descendants') && pid > 0
  })
  assert.ok(gone(pid), 'owned descendant must be gone after its parent exits')
})
test('diagnostics retain bounded tails; exact identity output refuses truncation', async () => {
  const output = await fixtureCommand('output', {outputLimit: 128})
  assert.equal(Buffer.byteLength(output), 128)
  assert.ok(output.endsWith('TAIL'))
  await assert.rejects(fixtureCommand('output', {outputLimit: 128, requireCompleteOutput: true}), /identity output exceeded 128 bytes/)
})
test('producer handshake is checked after consumer success', async () => {
  const options = producerOptions()
  await session({...options, runConsumers: async ({checkAlive}) => checkAlive()})
  assert.ok(existsSync(options.fixture + '.stop'))
  assert.match(readFileSync(options.logPath, 'utf8'), /consumer stop handshake received/)
  assert.ok(gone(JSON.parse(readFileSync(options.fixture)).pid))
})
test('consumer failure still writes producer stop and preserves original evidence', async () => {
  const options = producerOptions()
  await assert.rejects(session({...options, runConsumers: async () => { throw Error('consumer assertion failed') }}), /consumer assertion failed/)
  assert.ok(existsSync(options.fixture + '.stop'))
  assert.ok(gone(JSON.parse(readFileSync(options.fixture)).pid))
})
test('producer ignoring stop cannot pass and is killed within bounded waits', async () => {
  const options = producerOptions('producer-ignores-stop'), start = Date.now()
  await assert.rejects(session({...options, runConsumers: async () => {}}), /failed explicit stop handshake/)
  assert.ok(Date.now() - start < 1500, 'producer teardown must be bounded')
  assert.ok(existsSync(options.fixture + '.stop'))
  assert.ok(gone(JSON.parse(readFileSync(options.fixture)).pid))
})
test('cancellation during consumer work still enters producer finally cleanup', async () => {
  const options = producerOptions('producer-ignores-stop'), controller = new AbortController()
  // Cancellation is exercised during consumer work after a real readiness handshake.
  await assert.rejects(session({...options, signal: controller.signal, runConsumers: async () => {
    controller.abort(Error('session cancelled')); await delay(1)
  }}), /session cancelled/)
  assert.ok(existsSync(options.fixture + '.stop'))
  assert.ok(gone(JSON.parse(readFileSync(options.fixture)).pid))
})


test('separate-group descendants remain owned through the dedicated session', async () => {
  const begin = events.length, work = mkdtempSync(join(tmpdir(), 'receipt-process-test-'))
  const identityPath = join(work, 'descendant.json')
  let pid
  try {
    await assert.rejects(fixtureCommand('separate-group', {env: {...process.env, RECEIPT_TEST_DESCENDANT_IDENTITY: identityPath}}), error => {
      pid = Number(error.message.match(/descendant pid=(\d+)/)?.[1])
      return error.message.includes('active owned descendants') && pid > 0
    })
    const child = events.slice(begin).find(event => event.event === 'observed' && event.member.pid === pid)
    assert.ok(child, 'separate-group child must be recorded by exact identity')
    assert.notEqual(child.member.group, child.root.group, 'fixture must actually leave the original process group')
    assert.equal(child.member.session, child.root.session, 'controlled fixture stays in the owned session')
    assert.ok(gone(pid), 'separate-group child must be gone')
  } finally {
    // This independent fixture-only guard still cleans our exact descendant when
    // the production session-discovery safeguard is deliberately removed in scratch.
    const expected = JSON.parse(readFileSync(identityPath, 'utf8'))
    if (!gone(expected.pid)) {
      const text = readFileSync(`/proc/${expected.pid}/stat`, 'utf8')
      const fields = text.slice(text.lastIndexOf(')') + 2).split(' ')
      assert.equal(fields[19], expected.start, 'fixture cleanup must verify PID start')
      assert.equal(Number(fields[2]), expected.group, 'fixture cleanup must verify group')
      assert.equal(Number(fields[3]), expected.session, 'fixture cleanup must verify session')
      process.kill(expected.pid, 'SIGKILL')
      for (let attempts = 0; attempts < 30 && !gone(expected.pid); attempts++) await delay(10)
    }
    console.log('exact fixture descendant outcome: ' + JSON.stringify({...expected, gone: gone(expected.pid)}))
    assert.ok(gone(expected.pid), 'disposable descendant must be gone even after a safeguard removal')
  }
})

test('handshake waits for inherited pipe delivery after the producer exit event', async () => {
  const options = producerOptions('producer-delayed-handshake')
  await assert.doesNotReject(session({...options, handshakeMs: 400, runConsumers: async () => {}}), 'handshake must wait for complete post-exit pipe delivery')
  const log = readFileSync(options.logPath, 'utf8')
  const pipeChild = JSON.parse(readFileSync(options.fixture + '.pipe-child.json', 'utf8'))
  console.log('exact delayed pipe child outcome: ' + JSON.stringify({...pipeChild, gone: gone(pipeChild.pid)}))
  assert.ok(gone(pipeChild.pid), 'post-exit pipe writer must be gone')
  assert.match(log, /marker delivered after producer exit/)
  assert.match(log, /consumer stop handshake received/)
  assert.ok(gone(JSON.parse(readFileSync(options.fixture)).pid))
})

test('independent deadline closes late spawn authority despite a stalled async callback', async () => {
  const options = producerOptions(), start = Date.now()
  let release, lateResult
  const gate = new Promise(resolve => { release = resolve })
  const late = new Promise(resolve => { lateResult = resolve })
  // A finite fallback makes safeguard removal fail an assertion, never a test timeout.
  const fallback = setTimeout(release, 1500)
  try {
    await assert.rejects(session({...options, milliseconds: 1300, runConsumers: async ({command}) => {
      await gate
      try { await command(process.execPath, [fixture, 'success'], {quiet: true}); lateResult(null) }
      catch (error) { lateResult(error) }
    }}), /bounded consumer runner deadline expired/)
    const elapsed = Date.now() - start
    const starts = events.filter(event => event.event === 'start').length
    release()
    assert.match((await late)?.message ?? '', /consumer spawn authority is closed/, 'late callback must lose spawn authority')
    assert.equal(events.filter(event => event.event === 'start').length, starts, 'closed authority must never spawn')
    assert.ok(elapsed < 1000, 'independent deadline must settle before the finite stalled callback fallback')
    assert.ok(gone(JSON.parse(readFileSync(options.fixture)).pid))
  } finally { clearTimeout(fallback); release() }
})

test('deadline cancels and joins a tracked SIGTERM-ignoring consumer', async () => {
  const options = producerOptions(), begin = events.length, start = Date.now()
  await assert.rejects(session({...options, milliseconds: 1300, runConsumers: async ({command}) => {
    await command(process.execPath, [fixture, 'ignores-term'], {quiet: true})
  }}), /bounded consumer runner deadline expired/)
  const observed = events.slice(begin)
  const consumer = observed.find(event => event.event === 'start' && event.argv.includes('ignores-term'))
  assert.ok(consumer?.root, 'consumer must actually spawn')
  assert.ok(observed.some(event => event.event === 'signal' && event.root.pid === consumer.root.pid && event.signal === 'SIGKILL'), 'consumer must require escalation')
  assert.ok(observed.some(event => event.event === 'settled' && event.root.pid === consumer.root.pid && event.active.length === 0), 'session must join tracked consumer cleanup')
  assert.ok(gone(consumer.root.pid), 'consumer must be gone before session returns')
  assert.ok(Date.now() - start < 1300, 'cleanup must fit inside the absolute session budget')
})

test('returned consumer callback closes authority and joins unawaited owned work', async () => {
  const options = producerOptions(), begin = events.length
  await session({...options, runConsumers: async ({command}) => {
    const ready = options.fixture + '.consumer-ready'
    command(process.execPath, [fixture, 'ignores-term', ready], {quiet: true})
    while (!existsSync(ready)) await delay(10)
  }})
  const started = events.slice(begin).filter(event => event.event === 'start')
  assert.equal(started.length, 2)
  for (const event of started) assert.ok(gone(event.root.pid), 'all admitted owned children must settle before success')
})

test('readiness cancellation independently stops a producer that never creates its fixture', async () => {
  const options = producerOptions('producer-no-ready'), controller = new AbortController(), begin = events.length
  const cancel = setTimeout(() => controller.abort(Error('readiness cancelled')), 150)
  try { await assert.rejects(session({...options, signal: controller.signal, runConsumers: async () => assert.fail('consumer must not run before readiness')}), /readiness cancelled/) }
  finally { clearTimeout(cancel) }
  assert.equal(existsSync(options.fixture), false)
  assert.ok(existsSync(options.fixture + '.stop'))
  assert.ok(gone(events.slice(begin).find(event => event.event === 'start').root.pid))
})

test('handshake requires its complete untruncated marker and successful exit', async () => {
  await assert.rejects(session({...producerOptions('producer-bad-handshake'), runConsumers: async () => {}}), /did not pass explicit stop handshake/)
  await assert.rejects(session({...producerOptions('producer-chatty'), outputLimit: 128, runConsumers: async () => {}}), /handshake output exceeded capture limit/)
})


test('cancellation propagates to an active consumer before joining its cleanup', async () => {
  const options = producerOptions(), controller = new AbortController(), start = Date.now(), begin = events.length
  await assert.rejects(session({...options, signal: controller.signal, runConsumers: async ({command}) => {
    const ready = options.fixture + '.consumer-ready'
    const running = command(process.execPath, [fixture, 'ignores-term', ready], {quiet: true})
    while (!existsSync(ready)) await delay(10)
    controller.abort(Error('active consumer cancelled'))
    await running
  }}), /active consumer cancelled/)
  assert.ok(Date.now() - start < 1000, 'shared cancellation must stop active consumer without awaiting its independent command deadline')
  for (const event of events.slice(begin).filter(event => event.event === 'start')) assert.ok(gone(event.root.pid))
})

test('settled unawaited consumer failure remains part of the session outcome', async () => {
  const options = producerOptions()
  await assert.rejects(session({...options, runConsumers: async ({command}) => {
    command(process.execPath, [fixture, 'failure'], {quiet: true})
    await delay(150)
  }}), /exited 7/)
})


test('a primitive cancellation reason still refuses a successful producer session', async () => {
  const options = producerOptions(), controller = new AbortController()
  await assert.rejects(session({...options, signal: controller.signal, runConsumers: async () => {
    controller.abort('primitive cancellation')
    await delay(1)
  }}), /primitive cancellation/)
})
