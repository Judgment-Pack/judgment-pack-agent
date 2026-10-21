#!/usr/bin/env node
import {spawnSync} from 'node:child_process'
import {resolve} from 'node:path'

const root=resolve(process.argv[2]??'../../corpus')
const result=spawnSync(process.execPath,['../../node_modules/vitest/vitest.mjs','run','certification/corpus.test.ts'],{
 cwd:new URL('..',import.meta.url),stdio:'inherit',env:{...process.env,JP_CORPUS_ROOT:root}
})
if(result.error)throw result.error
process.exitCode=result.status??1
