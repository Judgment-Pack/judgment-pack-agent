#!/usr/bin/env node
// Separate from ADR-0022 offline replay; no shared record format is introduced.
import {readFileSync,readdirSync,statSync,openSync,closeSync,ftruncateSync,writeSync,fsyncSync} from 'node:fs'
import {resolve,join,relative} from 'node:path'
import {pathToFileURL} from 'node:url'
import {createHash} from 'node:crypto'
import {execFileSync} from 'node:child_process'

export const digest=value=>createHash('sha256').update(typeof value==='string'||Buffer.isBuffer(value)?value:JSON.stringify(value)).digest('hex')
const fail=message=>{throw Error(message)}
const integer=(n,min=0)=>Number.isSafeInteger(n)&&n>=min
const read=path=>JSON.parse(readFileSync(path,'utf8'))
const same=(a,b)=>JSON.stringify(a)===JSON.stringify(b)
const usd=n=>`${Math.floor(n/1e9)}.${String(n%1e9).padStart(9,'0')}`
export function validateConfig(c){
 if(c?.kind!=='release-quality-config'||c.authorized!==true)fail('explicit concrete live configuration authorization required')
 for(const k of ['model','credentialEnv','priorRef'])if(typeof c[k]!=='string'||!c[k]||c[k].includes('SET_'))fail('explicit model, credential and prior release required')
 if(!/^[A-Z][A-Z0-9_]*$/.test(c.credentialEnv))fail('invalid credential environment name')
 if(c.maxTotalTokens>532480||c.maxNanoUsd>1000000000)fail('fixed replay ceiling exceeded')
 const url=new URL(c.endpoint)
 if(url.protocol!=='https:'||url.username||url.password||url.search||url.hash||!url.pathname.endsWith('/chat/completions'))fail('explicit HTTPS chat completions endpoint required')
 for(const k of ['priorCommit','candidateCommit','sliceSha256'])if(!/^[0-9a-f]{40,64}$/.test(c[k]??''))fail('exact identities and slice digest required')
 if(c.priorCommit===c.candidateCommit||!/^v\d+\.\d+\.\d+$/.test(c.priorRef))fail('distinct candidate and prior release required')
 for(const k of ['maxCallsPerCase','maxInputTokens','maxOutputTokens','maxTotalTokens','maxNanoUsd','inputNanoUsdPerToken','cachedInputNanoUsdPerToken','outputNanoUsdPerToken','timeoutMs','maxResponseBytes'])if(!integer(c[k],1))fail('positive integer bounds and confirmed rates required')
 if(c.maxCallsPerCase>4||c.maxInputTokens>32768||c.maxOutputTokens>512||c.timeoutMs>120000||c.maxResponseBytes>1048576)fail('bounded replay limits exceeded')
 if(c.cachedInputNanoUsdPerToken>c.inputNanoUsdPerToken||!Number.isFinite(c.changeThreshold)||c.changeThreshold<0||c.changeThreshold>1)fail('invalid rates or threshold')
 for(const n of [c.maxTotalTokens,c.maxNanoUsd,c.maxInputTokens*c.inputNanoUsdPerToken+c.maxOutputTokens*c.outputNanoUsdPerToken])if(!integer(n,1))fail('budget arithmetic overflow')
 return c
}
export function cost(c,u){
 const n=(u.input-u.cached)*c.inputNanoUsdPerToken+u.cached*c.cachedInputNanoUsdPerToken+u.output*c.outputNanoUsdPerToken
 if(!integer(n))fail('cost overflow')
 return n
}
export function parseCompletion(c,body){
 if(body?.model!==c.model||!Array.isArray(body.choices)||body.choices.length!==1)fail('response model or choice scope differs')
 const choice=body.choices[0],u=body.usage
 if(!['stop','tool_calls'].includes(choice.finish_reason)||!choice.message)fail('truncated or incomplete answer')
 const input=u?.prompt_tokens,output=u?.completion_tokens,cached=u?.prompt_tokens_details?.cached_tokens??0
 if(!integer(input,1)||!integer(output,1)||!integer(cached)||cached>input||u.total_tokens!==input+output||input>c.maxInputTokens||output>c.maxOutputTokens)fail('absent, invalid or out-of-bounds usage')
 // Unexpected modality/billing classes cannot silently acquire zero cost.
 if((u.completion_tokens_details?.audio_tokens??0)!==0||(u.prompt_tokens_details?.audio_tokens??0)!==0)fail('unsupported token billing class')
 return {input,output,cached}
}
export function transport(c,key,{fetchImpl=globalThis.fetch,provenance='live'}={}){
 let total=0,nanoUsd=0,calls=0,uncertain=false
 const rows=[]
 return {
  rows,summary:()=>({calls,totalTokens:total,nanoUsd,costUsd:usd(nanoUsd),uncertain}),
  async call(request){
   if(uncertain)fail('earlier provider attempt is incomplete')
   if(request?.suffix!=='chat/completions')fail('unexpected endpoint suffix')
   const body=structuredClone(request.body)
   if(body.model!==c.model)fail('request model differs')
   body.stream=false;delete body.stream_options;delete body.max_tokens
   body.max_completion_tokens=c.maxOutputTokens
   // Text/tool-only JSON: UTF-8 bytes plus 1024 framing tokens is a conservative
   // upper bound, deliberately costlier than tokenizer estimates. No image/audio inputs.
   const wire=JSON.stringify(body)
   if(/"(?:image_url|input_audio|audio|file)"\s*:/.test(wire)||Buffer.byteLength(wire)+1024>c.maxInputTokens)fail('input token upper bound exceeded or nontext input')
   const reserve=c.maxInputTokens+c.maxOutputTokens,reserveCost=c.maxInputTokens*c.inputNanoUsdPerToken+c.maxOutputTokens*c.outputNanoUsdPerToken
   if(total+reserve>c.maxTotalTokens||nanoUsd+reserveCost>c.maxNanoUsd)fail('remaining global token or cost budget cannot reserve this request')
   calls++;uncertain=true
   try{
    const response=await fetchImpl(c.endpoint,{method:'POST',headers:{'content-type':'application/json',authorization:`Bearer ${key}`},body:wire,redirect:'error',signal:AbortSignal.timeout(c.timeoutMs)})
    if(!response.ok)fail('provider HTTP failure')
    const reader=response.body?.getReader();if(!reader)fail('missing provider body')
    let bytes=0;const chunks=[]
    try{while(true){const {done,value}=await reader.read();if(done)break;bytes+=value.byteLength;if(bytes>c.maxResponseBytes)fail('provider body exceeds bound');chunks.push(Buffer.from(value))}}finally{await reader.cancel().catch(()=>{})}
    const raw=Buffer.concat(chunks).toString('utf8'),parsed=JSON.parse(raw),usage=parseCompletion(c,parsed)
    if(raw.includes(key))fail('credential reflected by provider')
    total+=usage.input+usage.output;nanoUsd+=cost(c,usage)
    rows.push({...usage,nanoUsd:cost(c,usage),provenance});uncertain=false
    return new Response(raw,{headers:{'content-type':'application/json'}})
   }catch{fail('provider attempt failed; usage may be incomplete; raw provider details withheld')}
  }
 }
}
function git(root,args){return execFileSync('git',['-C',root,...args],{encoding:'utf8',stdio:['ignore','pipe','ignore']}).trim()}
function filesDigest(root){
 const rows=[]
 const walk=dir=>{for(const name of readdirSync(dir).sort()){const path=join(dir,name);if(statSync(path).isDirectory())walk(path);else rows.push([relative(root,path),digest(readFileSync(path))])}}
 walk(root);return digest(rows)
}
export function inspectRelease(root,commit,ref){
 if(git(root,['rev-parse','HEAD'])!==commit||git(root,['status','--porcelain','--untracked-files=normal']))fail('release checkout must be clean at exact commit')
 if(ref&&git(root,['rev-parse',`${ref}^{commit}`])!==commit)fail('prior release ref differs')
 const pkg=read(join(root,'packages/harness/package.json'))
 if(ref&&`v${pkg.version}`!==ref)fail('previous release package version differs')
 return {commit,version:pkg.version,artifactSha256:filesDigest(join(root,'packages/harness/dist'))}
}
export function loadSlice(root,path,expected){
 const bytes=readFileSync(path)
 if(digest(bytes)!==expected)fail('fixed slice digest differs')
 const slice=JSON.parse(bytes)
 if(slice.kind!=='release-quality-slice'||!Array.isArray(slice.cases)||slice.cases.length!==2)fail('fixed two-case model slice required')
 const ids=['model-policy-refusal','model-recorded-tool-result']
 for(const [i,row] of slice.cases.entries()){
  if(row.id!==ids[i]||typeof row.prompt!=='string'||!row.prompt||row.prompt.length>2048)fail('corpus case scope differs')
  const dir=join(root,'corpus',row.id)
  if(filesDigest(dir)!==row.corpusSha256)fail('fixed corpus content differs')
 }
 return slice
}
export async function replayRelease(root,c,slice,wire,key){
 const {configureHarness}=await import(pathToFileURL(join(root,'packages/harness/dist/host.js')))
 // This host owns only the reviewed public corpus and the in-memory driver.
 // Its synthetic relay session never authenticates with the live provider;
 // the separately bounded transport owns the explicitly authorized API key.
 const unavailable=()=>fail('quality replay host has no file, image or external runtime capability')
 configureHarness({
  assistantLanguageInstructions:()=> '\n\nReply in English. Preserve source quotations, tool names and schema keys.',
  currentLanguage:()=> 'en',sessionBearer:async()=> 'quality-public-synthetic-session',
  sessionEnded:()=>null,whenSessionEnds:()=>()=>{},forgetSession:()=>{},
  NoSession:class extends Error {},NO_SESSION_MESSAGE:'Quality replay session unavailable',
  refusalCode:response=>response.headers.get('X-Jpack-Desk-Refusal'),
  discardBody:async response=>{await response.body?.cancel().catch(()=>{})},
  chassisUrl:(path,params={})=>{const query=new URLSearchParams(params).toString();return query?`${path}?${query}`:path},
  deskHeaders:()=>({}),aiHeaders:()=>({}),deskSocketURL:unavailable,
  socketURL:unavailable,socketProtocols:unavailable,DeskWebSocketTransport:class{constructor(){unavailable()}},
  deskFetch:unavailable,answer:unavailable,readFile:unavailable,dialectAPI:unavailable,
  endpointCapabilities:()=>({}),renderImageToText:unavailable,modelsFor:()=>({}),
  brief:()=>undefined,memoryFor:()=>undefined,projectInstructions:()=>undefined
 })
 const {runOnModelEngine}=await import(pathToFileURL(join(root,'packages/harness/dist/testing/scenarioDriver.js')))
 const cases=[]
 for(const row of slice.cases){
  const fixtures=read(join(root,'corpus',row.id,'tool-results.json'));let calls=0,fixtureAt=0,toolFailure=false
  const start=wire.rows.length
  const result=await runOnModelEngine(async request=>{
   if(++calls>c.maxCallsPerCase)fail('case call bound exceeded')
   return wire.call({suffix:'chat/completions',body:request.body})
  },{session:{prompt:row.prompt,critic:'off',model:{model:c.model}},handle:call=>{
   const fixture=fixtures[fixtureAt++]
   if(!fixture||call.name!==fixture.name||!same(call.args,fixture.args)){toolFailure=true;fail('tool call outside recorded scope')}
   return structuredClone(fixture.result)
  }})
  const answers=result.events.filter(e=>e.type==='message').map(e=>e.text)
  if(toolFailure||fixtureAt!==fixtures.length||result.events.filter(e=>e.type==='tool_call').length!==fixtures.length||!calls||!answers.length||answers.some(a=>typeof a!=='string'||!a.trim()||a.includes(key))||result.events.some(e=>e.type==='error')||result.events.at(-1)?.type!=='end')fail('partial or unsafe engine answer')
  // Only public synthetic fixture inputs are admitted. The engine output guard has
  // already run; retain final host-visible prose, never reasoning/provider bodies.
  cases.push({id:row.id,answers,usage:wire.rows.slice(start)})
 }
 return cases
}
export function reportFor(c,identities,prior,candidate,provenance='live'){
 if(prior.length!==2||candidate.length!==2)fail('partial comparison')
 const rows=prior.map((left,i)=>{
  const right=candidate[i]
  if(left.id!==right.id)fail('case identity differs')
  return {id:left.id,changed:!same(left.answers,right.answers),prior:left,candidate:right}
 })
 const changed=rows.filter(row=>row.changed).length
 const usage=rows.flatMap(row=>[...row.prior.usage,...row.candidate.usage])
 const totalTokens=usage.reduce((n,u)=>n+u.input+u.output,0),nanoUsd=usage.reduce((n,u)=>n+cost(c,u),0)
 return {kind:'release-quality-report',provenance,configuration:c,configSha256:digest(c),identities,rows,changed,share:changed/2,totalTokens,nanoUsd,costUsd:usd(nanoUsd),status:changed/2>c.changeThreshold?'decision-required':'within-threshold'}
}
export function validateReport(c,r){
 validateConfig(c)
 if(r?.kind!=='release-quality-report'||r.provenance!=='live'||r.configSha256!==digest(c))fail('live evidence with exact configuration required')
 if(r.identities?.prior?.commit!==c.priorCommit||r.identities?.candidate?.commit!==c.candidateCommit)fail('report release identities differ')
 for(const label of ['prior','candidate'])if(!/^[0-9a-f]{64}$/.test(r.identities[label].artifactSha256??''))fail('built artifact digest missing')
 const ids=['model-policy-refusal','model-recorded-tool-result']
 let count=0
 for(const [i,row] of (r.rows??[]).entries()){
  if(row.id!==ids[i])fail('report corpus scope differs')
  for(const label of ['prior','candidate']){
   const item=row[label]
   if(item?.id!==row.id||!Array.isArray(item.answers)||!item.answers.length||item.answers.some(a=>typeof a!=='string'||!a.trim())||!Array.isArray(item.usage)||!item.usage.length||item.usage.length>c.maxCallsPerCase)fail('partial answer evidence')
   for(const u of item.usage){count++;if(u.provenance!=='live'||!integer(u.input,1)||!integer(u.output,1)||!integer(u.cached)||u.cached>u.input||u.input>c.maxInputTokens||u.output>c.maxOutputTokens||u.nanoUsd!==cost(c,u))fail('invalid usage evidence')}
  }
 }
 if(count>16)fail('report call scope differs')
 const expected=reportFor(c,r.identities,r.rows.map(row=>row.prior),r.rows.map(row=>row.candidate))
 if(!same(r,expected)||r.totalTokens>c.maxTotalTokens||r.nanoUsd>c.maxNanoUsd)fail('report totals or budget differ')
 return r
}
async function main(args){
 const [command,configPath,...rest]=args,c=validateConfig(read(configPath))
 if(command==='validate'){
  if(rest.length!==1)fail('usage: validate CONFIG REPORT')
  const r=validateReport(c,read(rest[0]));console.log(`quality replay: validated ${r.status}; ${r.costUsd} USD`);return r.status==='decision-required'?2:0
 }
 if(command!=='run'||rest.length!==4)fail('usage: run CONFIG SLICE PRIOR_CHECKOUT CANDIDATE_CHECKOUT REPORT')
 const [slicePath,priorRoot,candidateRoot,reportPath]=rest.map(p=>resolve(p)),key=process.env[c.credentialEnv]
 if(!key||key.length<8)fail('configured provider credential is absent')
 const identities={prior:inspectRelease(priorRoot,c.priorCommit,c.priorRef),candidate:inspectRelease(candidateRoot,c.candidateCommit)}
 const slice=loadSlice(candidateRoot,slicePath,c.sliceSha256)
 // Same reviewed corpus slice in both source versions; model/protocol shared.
 loadSlice(priorRoot,slicePath,c.sliceSha256)
 const reportFd=openSync(reportPath,'wx',0o600)
 const writeReport=value=>{ftruncateSync(reportFd,0);writeSync(reportFd,JSON.stringify(value,null,2)+'\n',0,'utf8');fsyncSync(reportFd)}
 writeReport({kind:'release-quality-incomplete',provenance:'live',configSha256:digest(c),identities,status:'started'})
 const wire=transport(c,key)
 try{
  const prior=await replayRelease(priorRoot,c,slice,wire,key),candidate=await replayRelease(candidateRoot,c,slice,wire,key)
  if(wire.summary().uncertain)fail('incomplete provider usage')
  const report=validateReport(c,reportFor(c,identities,prior,candidate))
  // Check source/artifact identity again after the complete run.
  if(!same(identities,{prior:inspectRelease(priorRoot,c.priorCommit,c.priorRef),candidate:inspectRelease(candidateRoot,c.candidateCommit)}))fail('release changed during replay')
  writeReport(report)
  console.log(`quality replay: ${report.status}; ${report.changed}/2 changed; ${report.totalTokens} tokens; ${report.costUsd} USD`)
  return report.status==='decision-required'?2:0
 }catch{
  writeReport({kind:'release-quality-incomplete',provenance:'live',configSha256:digest(c),identities,usage:wire.rows,summary:wire.summary(),status:'incomplete'})
  fail('live replay incomplete; retained known usage; no release evidence accepted')
 }finally{closeSync(reportFd)}
}
if(process.argv[1]&&resolve(process.argv[1])===resolve(new URL(import.meta.url).pathname))main(process.argv.slice(2)).then(code=>{process.exitCode=code}).catch(()=>{console.error('quality replay refused: invalid configuration, scope, budget, provider or evidence; no raw provider details emitted');process.exitCode=1})
