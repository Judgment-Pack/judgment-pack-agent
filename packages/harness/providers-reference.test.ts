import {it,expect} from 'vitest'
import {spawnSync} from 'node:child_process'
import {resolve} from 'node:path'
// The generator takes about 20 s by itself, the suite's default limit: under the two workers of the whole
// suite it went over, and the test failed with nothing wrong (agent#285).
it('the generated provider matrix matches the capability records',()=>{
 const root=resolve(process.cwd(),process.cwd().endsWith('harness')?'../..':'.')
 const result=spawnSync(process.execPath,['scripts/gen-reference.mjs','--check'],{cwd:root,encoding:'utf8'})
 expect(result.status,result.stderr).toBe(0)
},180_000)
