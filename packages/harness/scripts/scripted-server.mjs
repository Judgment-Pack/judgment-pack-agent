#!/usr/bin/env node
// The certification's scripted model and recorded runtime behind one HTTP server on the loopback
// interface, for an adapter that does not run in this process: `node scripts/scripted-server.mjs`.
//
// It serves the same modules the TypeScript certification imports (certification/scriptedModel.ts,
// certification/scriptedServer.ts, certification/scenario.json), loaded through Vite's module
// runner, so an adapter in another language is certified against the same answers, the same
// refusals and the same records as the TypeScript adapters. Nothing here decides a step: each
// request is handed to `scriptedModel(...).fetch` and each JSON-RPC frame to
// `recordedRuntime().answer`, unchanged.
//
// The protocol, all JSON:
//
// - On start, one line on stdout: {"listening": "http://127.0.0.1:<port>"}. The server ends when
//   its stdin closes, so it never outlives the process that started it.
// - POST /sessions {api, answerAs, thinking?, refuted?, signatures?, toolCalls?}: one scripted
//   endpoint and one recorded runtime, as `scriptedModel(options)` and `recordedRuntime()`.
//   Answers {id, relay, mcp}: the paths of the session's relay and runtime.
// - Any method on /s/<id>/api/assistant/relay/v1/<suffix>: the relay. The stub is handed the
//   address from /api/assistant/relay/v1 on, as the TypeScript legs hand it, and the request's
//   headers less those the HTTP transport itself writes (TRANSPORT_HEADERS).
// - POST /s/<id>/mcp: one JSON-RPC message; the reply frame, or 202 with no body for a
//   notification.
// - GET /s/<id>/observations: {requests, seen}, the scripted model's RecordedRequest list and the
//   runtime's ServerObservation list, in arrival order.
// - GET /scenario: certification/scenario.json.
import { createServer } from 'node:http'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { runnerImport } from 'vite'

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const load = async (path) =>
  (await runnerImport(resolve(root, path), { configFile: false, root, logLevel: 'silent' })).module

const { scriptedModel } = await load('certification/scriptedModel.ts')
const { recordedRuntime } = await load('certification/scriptedServer.ts')
const scenario = (await load('certification/scenario.json')).default

const RELAY = '/api/assistant/relay/v1/'

// Written by an HTTP client's transport rather than by the code that makes the request: a fetch
// stub in the TypeScript legs never sees them, so they are not recorded here either.
const TRANSPORT_HEADERS = new Set([
  'host',
  'connection',
  'keep-alive',
  'content-length',
  'transfer-encoding',
  'accept-encoding',
  'user-agent'
])

const ENDPOINT_OPTIONS = ['api', 'answerAs', 'thinking', 'refuted', 'signatures', 'toolCalls']

/** @type {Map<string, {model: ReturnType<typeof scriptedModel>, runtime: ReturnType<typeof recordedRuntime>}>} */
const sessions = new Map()
let next = 0

function readBody(request) {
  return new Promise((done, fail) => {
    const chunks = []
    request.on('data', (chunk) => chunks.push(chunk))
    request.on('end', () => done(Buffer.concat(chunks).toString('utf8')))
    request.on('error', fail)
  })
}

function json(response, status, value) {
  response.writeHead(status, { 'content-type': 'application/json' })
  response.end(JSON.stringify(value))
}

async function handle(request, response) {
  const url = new URL(request.url ?? '/', 'http://scripted.invalid')
  if (request.method === 'GET' && url.pathname === '/scenario') return json(response, 200, scenario)
  if (request.method === 'POST' && url.pathname === '/sessions') {
    const asked = JSON.parse((await readBody(request)) || '{}')
    const options = {}
    for (const name of ENDPOINT_OPTIONS) if (asked[name] !== undefined) options[name] = asked[name]
    if (!['openai-compatible', 'anthropic', 'gemini'].includes(options.api)) {
      return json(response, 400, { error: `unknown api: ${String(options.api)}` })
    }
    if (!['stream', 'whole'].includes(options.answerAs)) {
      return json(response, 400, { error: `answerAs must be stream or whole: ${String(options.answerAs)}` })
    }
    const id = String(++next)
    sessions.set(id, { model: scriptedModel(options), runtime: recordedRuntime() })
    return json(response, 201, { id, relay: `/s/${id}/api/assistant/relay/v1`, mcp: `/s/${id}/mcp` })
  }
  const matched = /^\/s\/([0-9]+)(\/.*)$/.exec(url.pathname)
  const session = matched ? sessions.get(matched[1]) : undefined
  if (!matched || !session) return json(response, 404, { error: `no such route: ${url.pathname}` })
  const rest = matched[2]

  if (request.method === 'GET' && rest === '/observations') {
    return json(response, 200, { requests: session.model.requests, seen: session.runtime.seen })
  }
  if (request.method === 'POST' && rest === '/mcp') {
    const reply = session.runtime.answer(JSON.parse(await readBody(request)))
    if (reply === null) {
      response.writeHead(202)
      return response.end()
    }
    return json(response, 200, reply)
  }
  if (rest.startsWith(RELAY)) {
    const headers = {}
    for (const [name, value] of Object.entries(request.headers)) {
      if (!TRANSPORT_HEADERS.has(name)) headers[name] = Array.isArray(value) ? value.join(', ') : String(value)
    }
    const body = request.method === 'GET' ? undefined : await readBody(request)
    const answered = await session.model.fetch(rest + url.search, { method: request.method, headers, body })
    response.writeHead(answered.status, {
      'content-type': answered.headers.get('content-type') ?? 'application/octet-stream'
    })
    return response.end(await answered.text())
  }
  return json(response, 404, { error: `no such route: ${url.pathname}` })
}

const server = createServer((request, response) => {
  handle(request, response).catch((error) => {
    if (!response.headersSent) json(response, 500, { error: String(error?.message ?? error) })
    else response.end()
  })
})
server.listen(0, '127.0.0.1', () => {
  const { port } = server.address()
  process.stdout.write(JSON.stringify({ listening: `http://127.0.0.1:${port}` }) + '\n')
})
process.stdin.on('end', () => {
  server.closeAllConnections()
  server.close(() => process.exit(0))
})
process.stdin.on('error', () => process.exit(0))
process.stdin.resume()
