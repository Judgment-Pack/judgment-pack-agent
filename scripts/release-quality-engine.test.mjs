// Uses a built real engine with hand-authored provider fixtures. No network/spend.
import test from 'node:test'
import assert from 'node:assert/strict'
import {readFileSync} from 'node:fs'
import {digest,loadSlice,transport,replayRelease,reportFor,validateReport} from './release-quality.mjs'
const root=new URL('..',import.meta.url).pathname
const c={kind:'release-quality-config',authorized:true,model:'offline-test-model',credentialEnv:'QUALITY_TEST_KEY',endpoint:'https://example.invalid/v1/chat/completions',priorRef:'v0.9.1',priorCommit:'a'.repeat(40),candidateCommit:'b'.repeat(40),sliceSha256:digest(readFileSync(new URL('../quality/release-slice.json',import.meta.url))),maxCallsPerCase:4,maxInputTokens:32768,maxOutputTokens:512,maxTotalTokens:532480,maxNanoUsd:1000000000,inputNanoUsdPerToken:100,cachedInputNanoUsdPerToken:50,outputNanoUsdPerToken:200,timeoutMs:1000,maxResponseBytes:10000,changeThreshold:0}
const key='offline-fixture-key'
function fixtureWire({omitTool=false}={}){
 const turns=omitTool?[{say:'Public policy explanation.'},{say:'Schema omitted.'}]:[
  {say:'Public policy explanation.'},{tool:'get_schema'},{say:'The recorded schema is recorded.'},
  {say:'Changed public policy explanation.'},{tool:'get_schema'},{say:'The recorded schema is recorded.'}
 ]
 let at=0
 return transport(c,key,{provenance:'offline-test',fetchImpl:async()=>{
  const turn=turns[at++];assert(turn,'unexpected extra provider call')
  const message=turn.tool?{role:'assistant',content:null,tool_calls:[{id:`fixture_${at}`,type:'function',function:{name:turn.tool,arguments:'{}'}}]}:{role:'assistant',content:turn.say}
  return new Response(JSON.stringify({id:`offline_${at}`,object:'chat.completion',created:0,model:c.model,choices:[{index:0,message,finish_reason:turn.tool?'tool_calls':'stop'}],usage:{prompt_tokens:12,completion_tokens:4,total_tokens:16}}),{headers:{'content-type':'application/json'}})
 }})
}
test('built engine replays fixed public slice and exposes changed answer evidence as offline',async()=>{
 const slice=loadSlice(root,new URL('../quality/release-slice.json',import.meta.url),c.sliceSha256),wire=fixtureWire()
 const prior=await replayRelease(root,c,slice,wire,key),candidate=await replayRelease(root,c,slice,wire,key)
 const report=reportFor(c,{prior:{commit:c.priorCommit,artifactSha256:'d'.repeat(64)},candidate:{commit:c.candidateCommit,artifactSha256:'e'.repeat(64)}},prior,candidate,'offline-test')
 assert.equal(wire.rows.length,6);assert.equal(report.changed,1);assert.equal(report.share,0.5);assert.equal(report.totalTokens,96);assert.equal(report.costUsd,'0.000012000');assert.equal(report.status,'decision-required')
 assert.equal(report.rows[0].prior.answers.at(-1),'Public policy explanation.')
 assert.equal(report.rows[0].candidate.answers.at(-1),'Changed public policy explanation.')
 assert.throws(()=>validateReport(c,report),/live evidence/)
})
test('built engine cannot complete evidence by omitting a fixed tool fixture',async()=>{
 const slice=loadSlice(root,new URL('../quality/release-slice.json',import.meta.url),c.sliceSha256)
 await assert.rejects(replayRelease(root,c,slice,fixtureWire({omitTool:true}),key),/partial or unsafe engine answer/)
})
