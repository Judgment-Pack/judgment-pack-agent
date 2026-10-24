#!/usr/bin/env node
// Current shapes are generated; the historical inventory is a frozen published contract.
import {readFileSync,writeFileSync,readdirSync} from 'node:fs'
const keys=new Set(['$ref','type','const','enum','properties','patternProperties','additionalProperties','items','oneOf','anyOf','allOf','required','$defs'])
function prune(x){if(Array.isArray(x))return x.map(prune);if(!x||typeof x!=='object')return x;return Object.fromEntries(Object.entries(x).filter(([k])=>keys.has(k)).map(([k,v])=>[k,['properties','patternProperties','$defs'].includes(k)?Object.fromEntries(Object.entries(v).map(([n,s])=>[n,prune(s)])):prune(v)]))}
const schemas=Object.fromEntries(readdirSync('contract/schemas').filter(f=>f.endsWith('.json')).sort().map(f=>[f,prune(JSON.parse(readFileSync('contract/schemas/'+f,'utf8')))]))
const historical=JSON.parse(readFileSync('contract/records/historical-shapes.json','utf8'))
const text=JSON.stringify({historical,current:schemas})+'\n'
for(const path of ['packages/harness/contract/record-shapes.json','go/contract/record_shapes.json']){if(process.argv.includes('--check')){if(readFileSync(path,'utf8')!==text)throw Error(path+' is stale')}else writeFileSync(path,text)}
