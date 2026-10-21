#!/usr/bin/env node
import {readFileSync,readdirSync} from 'node:fs'
import {resolve,join} from 'node:path'

const projection=events=>{
 const gateDecisions=[],outcomes=[],refusals=[];let proposalDigest,critic
 for(const event of events){if(event.type==='guardrail'){const row={tool:event.tool,action:event.action,...(event.reason===undefined?{}:{reason:event.reason})};gateDecisions.push(row);if(event.action==='refused')refusals.push(row)}else if(event.type==='tool_result')outcomes.push({type:event.type,name:event.name,isError:event.isError});else if(event.type==='error')outcomes.push({type:event.type,code:event.failure?.code??null,outcome:event.failure?.outcome??null});else if(event.type==='proposal'){outcomes.push({type:event.type});proposalDigest=event.digest;if(event.critique)critic={verdict:event.critique.refuted,reasons:event.critique.checks}}}
 return {gateDecisions,outcomes,...(proposalDigest===undefined?{}:{proposalDigest}),...(critic===undefined?{}:{critic}),refusals}
}
const omit=(value,pointer)=>{const parts=pointer.slice(1).split('/');let at=value;for(const part of parts.slice(0,-1)){if(!at||typeof at!=='object')return;at=at[part]}if(at&&typeof at==='object')delete at[parts.at(-1)]}
const compare=(entry)=>{const expected=structuredClone(entry.expected),actual=projection(entry.events);for(const pointer of entry.manifest.tolerance){omit(expected,pointer);omit(actual,pointer)};if(JSON.stringify(expected)!==JSON.stringify(actual))throw Error(`corpus ${entry.manifest.id}: compared members differ`)}

const root=resolve(process.argv[2]??'corpus')
let passed=0
for(const name of readdirSync(root,{withFileTypes:true}).filter(row=>row.isDirectory()).map(row=>row.name).sort()){
 const dir=join(root,name),read=file=>JSON.parse(readFileSync(join(dir,file),'utf8'))
 const manifest=read('manifest.json')
 if(manifest.id!==name)throw Error(`${name}: manifest id differs from directory`)
 const entry={manifest,requests:read(manifest.files.requests),toolResults:read(manifest.files.toolResults),events:read(manifest.files.events),checkpoint:read(manifest.files.checkpoint),expected:read(manifest.expected.file)}
 compare(entry)
 console.log(`PASS corpus ${name}`);passed++
}
console.log(`corpus: ${passed} passed, 0 failed`)
