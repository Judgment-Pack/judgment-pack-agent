#!/usr/bin/env node
// Required cross-repository consumer proof. Never substitutes synthetic receipts.
import {createHash} from 'node:crypto'
import {mkdtempSync,readFileSync,realpathSync,statfsSync,writeFileSync} from 'node:fs'
import {dirname,join,resolve} from 'node:path'
import {fileURLToPath} from 'node:url'
import {runCommand,runProducerSession} from './gateway-receipt-process.mjs'

const root=resolve(dirname(fileURLToPath(import.meta.url)),'..')
const sha=v=>createHash('sha256').update(v).digest('hex')
const args=process.argv.slice(2)
let source=process.env.JP_AGENT_GATEWAY_SOURCE,developmentManifest
while(args.length){const arg=args.shift();if(arg==='--gateway-source')source=args.shift();else if(arg==='--development-manifest')developmentManifest=args.shift();else throw Error('unknown argument '+arg)}
if(!source)throw Error('JP_AGENT_GATEWAY_SOURCE must name the explicit Gateway checkout')
source=realpathSync(source)
const abort=new AbortController()
for(const name of ['SIGINT','SIGTERM'])process.once(name,()=>abort.abort(Error('runner interrupted by '+name)))
async function command(program,argv,cwd,env=process.env,milliseconds=300000){
 return runCommand(program,argv,{cwd,env,milliseconds,signal:abort.signal})
}
// Identity queries also have bounded cancellation and exact (never truncated) output.
async function git(argv){
 return runCommand('git',['--no-pager',...argv],{cwd:source,milliseconds:10000,signal:abort.signal,quiet:true,outputLimit:16*1024*1024,requireCompleteOutput:true})
}
const head=(await git(['rev-parse','HEAD'])).trim()
if(realpathSync((await git(['rev-parse','--show-toplevel'])).trim())!==source)throw Error('Gateway source must be the checkout root')
let identity
if(developmentManifest){
 if(process.env.CI)throw Error('development producer manifests are forbidden in CI')
 const manifest=JSON.parse(readFileSync(developmentManifest,'utf8'))
 if(manifest.head!==head||!Array.isArray(manifest.files)||manifest.files.length===0)throw Error('development Gateway manifest identity mismatch')
 for(const entry of manifest.files){if(typeof entry.path!=='string'||entry.path.split('/').some(p=>!p||p==='.'||p==='..')||sha(readFileSync(join(source,entry.path)))!==entry.sha256)throw Error('development Gateway manifest file mismatch: '+entry.path)}
 if(sha(await git(['diff','--no-ext-diff','--binary']))!==manifest.tracked_diff_sha256)throw Error('development Gateway tracked diff mismatch')
 const changed=(await git(['status','--porcelain=v1','--untracked-files=all'])).split('\n').filter(Boolean).map(line=>line.slice(3)).sort()
 const expected=manifest.files.map(f=>f.path).sort()
 if(JSON.stringify(changed)!==JSON.stringify(expected))throw Error('development Gateway changed-file boundary mismatch')
 identity={basis:'development-frozen-manifest; not immutable CI evidence',head,manifestSha256:sha(readFileSync(developmentManifest)),files:manifest.files}
}else{
 const pin=JSON.parse(readFileSync(join(root,'tests/fixtures/gateway-receipts/source.json'),'utf8'))
 if(pin.version!==1||!/^([0-9a-f]{40}|[0-9a-f]{64})$/.test(pin.commit??''))throw Error('immutable Gateway producer commit pin is pending; coordinator must establish it before stable-batch validation/CI')
 if(head!==pin.commit||(await git(['status','--porcelain=v1','--untracked-files=all'])).trim())throw Error('Gateway source must be clean at the exact immutable producer commit pin')
 identity={basis:'clean immutable producer commit',head}
}
if(!process.env.JPDEV_SCRATCH||!process.env.TMPDIR)throw Error('JPDEV_SCRATCH and its child TMPDIR are required')
const scratch=realpathSync(process.env.JPDEV_SCRATCH),tmp=realpathSync(process.env.TMPDIR)
if(!tmp.startsWith(scratch+'/'))throw Error('TMPDIR must be beneath JPDEV_SCRATCH')
const space=statfsSync(scratch,{bigint:true});if(space.bavail*space.bsize<2576980378n)throw Error('receipt check requires 2576980378 bytes free')
const work=mkdtempSync(join(scratch,'gateway-receipts-')),fixture=join(work,'fixture.json'),prepared=join(work,'prepared.json'),producer=join(work,'producer.test'),consumer=join(work,'consumer.test')
const overlay=join(work,'overlay.json'),producerLog=join(work,'producer.log')
writeFileSync(overlay,JSON.stringify({Replace:{[join(source,'go/agent_consumer_receipts_test.go')]:join(root,'tests/fixtures/gateway-receipts/producer_test.go.txt')}}))
const env={...process.env,JP_AGENT_GATEWAY_PREPARED:prepared,JP_AGENT_GATEWAY_OUTPUT:fixture,JP_AGENT_GATEWAY_FIXTURE:fixture}
// All compilation and dependency setup precedes the live producer lifecycle.
await command('go',['test','-c','-overlay',overlay,'-o',producer,'.'],join(source,'go'))
await command('go',['test','-c','-o',consumer,'./context'],join(root,'go'))
await command('npm',['run','typecheck','--workspace','@judgment-pack/harness'],root)
await command(producer,['-test.run','^TestAgentConsumerReceiptPrepare$','-test.v'],join(source,'go'),env)
writeFileSync(join(work,'identity.json'),JSON.stringify({gateway:identity,producerSha256:sha(readFileSync(producer)),consumerSha256:sha(readFileSync(consumer)),overlaySha256:sha(readFileSync(join(root,'tests/fixtures/gateway-receipts/producer_test.go.txt')))},null,2)+'\n')
console.log('gateway receipts evidence: '+work)
await runProducerSession(producer,['-test.run','^TestAgentConsumerReceiptProducer$','-test.v','-test.timeout=10m30s'],{
 cwd:join(source,'go'),env,fixture,logPath:producerLog,signal:abort.signal,expectedExecutable:producer,
 runConsumers:async({remaining,checkAlive,command:consumerCommand})=>{
  await consumerCommand(consumer,['-test.run','^Test(GatewayReceipt|TaxonomyIntegration)','-test.v','-test.timeout=90s'],{cwd:join(root,'go/context'),env,milliseconds:Math.min(90000,remaining())})
  checkAlive()
  await consumerCommand(join(root,'node_modules/.bin/vitest'),['run','gatewayReceipt.test.ts','taxonomyIntegration.test.ts'],{cwd:join(root,'packages/harness'),env,milliseconds:Math.min(90000,remaining())})
  checkAlive()
 }
})
console.log('gateway receipts: passed actual remote and stdio producer/Agent consumers; '+identity.basis)
