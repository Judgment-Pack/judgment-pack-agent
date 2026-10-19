/**
 * What the certification suite drives an adapter with: the recorded scenario,
 * the recorded runtime, the scripted model and the scripted runtime server.
 * The suite itself is `conformance.test.ts` beside this file, run with
 * `npm run certify -- --adapter <id>`. `recall.ts` is the rule a server
 * applies to find the request text on any request of a run (agent#11).
 */
export * from './scriptedModel'
export * from './scriptedServer'
export { default as scenario } from './scenario.json'
export { default as runtime } from './runtime.json'
export * from './recall'
