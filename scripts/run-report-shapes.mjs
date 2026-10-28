#!/usr/bin/env node
// Report v1 is frozen at its published v0.10.0 source; future report fields require a new version.
import {readFileSync,writeFileSync} from 'node:fs'
import {createHash} from 'node:crypto'
const text=readFileSync('contract/records/run-report-v1.json','utf8')
const expectedSHA256='7ed7b399297b0ec5132e6f27163951cc0559c712f12be586ff9b133974730d3d'
if(createHash('sha256').update(text).digest('hex')!==expectedSHA256)throw Error('Published report v1 graph digest differs.')
if(JSON.parse(text).source!=='e00d2b11d11af4dabb0a4de6c1a5e7322bd8527e')throw Error('Published report v1 source identity differs.')
for(const path of ['packages/harness/contract/run-report-v1.json','go/context/run_report_v1.json']){
 if(process.argv.includes('--check')){if(readFileSync(path,'utf8')!==text)throw Error(path+' is stale')}
 else writeFileSync(path,text)
}
