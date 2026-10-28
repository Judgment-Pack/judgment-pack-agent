#!/usr/bin/env node
// Current report2 graph. Published report1 generation/bytes remain independently frozen.
import {readFileSync,writeFileSync} from 'node:fs'
const schemas=new Map(),definitions={},building=new Set()
function schema(file){if(!schemas.has(file))schemas.set(file,JSON.parse(readFileSync('contract/schemas/'+file,'utf8')));return schemas.get(file)}
function target(ref,file){const [next,pointer='']=ref.split('#');return [next||file,pointer]}
function node(file,pointer){let held=schema(file);for(const key of pointer.split('/').slice(1))held=held[key.replace(/~1/g,'/').replace(/~0/g,'~')];return held}
function link(ref,file){const [next,pointer]=target(ref,file),key=next+'#'+pointer;if(!Object.hasOwn(definitions,key)&&!building.has(key)){building.add(key);definitions[key]=copy(node(next,pointer),next);building.delete(key)}return key}
function copy(value,file){if(Array.isArray(value))return value.map(v=>copy(v,file));if(!value||typeof value!=='object')return value;return Object.fromEntries(Object.entries(value).filter(([key])=>!['$schema','$id','description','title'].includes(key)).map(([key,held])=>[key,key==='$ref'?link(held,file):copy(held,file)]))}
const root=copy(schema('run-report-v2.json'),'run-report-v2.json')
const text=JSON.stringify({version:2,root,definitions})+'\n'
for(const path of ['packages/harness/contract/run-report-v2.json','go/context/run_report_v2.json']){if(process.argv.includes('--check')){if(readFileSync(path,'utf8')!==text)throw Error(path+' is stale')}else writeFileSync(path,text)}
