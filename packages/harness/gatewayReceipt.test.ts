// @vitest-environment node
import {describe,it,expect} from 'vitest'
import {createHash,createPrivateKey,createPublicKey,sign} from 'node:crypto'
import {readFileSync} from 'node:fs'
import {request} from 'node:http'
import vector from '../../contract/testdata/taxonomy/gateway-receipt-v3.json'
import {verifyGatewayResult,gatewayExecutionContext,beginGatewayInvocation,GATEWAY_RECEIPT_ENVELOPE_BYTES,type GatewayReceiptTrust} from './gatewayReceipt'
import {withDeadline} from './engines/contract'
import {declareSources,SourceGate} from './toolSources'
import {CONTEXT_DEFAULTS} from './context/policy'
import {GateViolation} from './toolGate'
import type {DescriptorApproval} from './descriptorSnapshot'
import type {HostTool,McpToolResult} from './engine'
import type {SourceFigures} from './execution/state'

// Independent test canonicalization; the handwritten fixture keys are ASCII.
const canon=(v:unknown):string=>Array.isArray(v)?'['+v.map(canon).join(',')+']':typeof v==='object'&&v!==null?'{'+Object.keys(v).sort().map(k=>JSON.stringify(k)+':'+canon((v as Record<string,unknown>)[k])).join(',')+'}':JSON.stringify(v)
const digest=(v:string|Uint8Array)=>'sha256:'+createHash('sha256').update(v).digest('hex')
const commit=(label:string,v:unknown)=>digest(Buffer.concat([Buffer.alloc(32),Buffer.from(label+canon(v))]))
const key=createPrivateKey({key:Buffer.concat([Buffer.from('302e020100300506032b657004220420','hex'),Buffer.alloc(32)]),format:'der',type:'pkcs8'})
const publicKey=createPublicKey(key).export({format:'der',type:'spki'}).subarray(-32)
const signReceipt=(r:Record<string,unknown>)=>{delete r.signature;r.signature=sign(null,Buffer.from('judgment-pack-gateway/receipt/3:'+canon(r)),key).toString('hex')}
const bytes=(s:string)=>Uint8Array.from(Buffer.from(s))
const owners=()=>({query:{operation:'read',effect:'read-external'},sibling:{operation:'read',effect:'read-external'}} as DescriptorApproval['toolTaxonomies'])
const approval=(remote=true):DescriptorApproval=>({platform:'tickets',binding:'tickets@sha256:'+'a'.repeat(64),operation:'live',...(remote?{endpoint:'https://example.invalid/mcp'}:{}),toolTaxonomies:owners()})
const snapshot=(remote=true)=>bytes(canon({snapshotVersion:'2',operation:'live',platform:'tickets',binding:approval().binding,capturedAt:'2026-10-25T00:00:00Z',policy:1,server:{name:'fixture',version:'1'},...(remote?{endpoint:approval().endpoint}:{}),tools:Object.fromEntries(Object.entries(owners()).map(([name,taxonomy])=>[name,{taxonomy}]))}))
function synthetic(remote=true){
 const call={tool:vector.tool,arguments:vector.args}
 const acquisition:Record<string,unknown>={shape:'mcp',statement:commit('statement:',canon(call)),adapter:vector.adapter,endpoint:null,snapshot:null,peerIdentity:null,schema:null,upstreamToken:null,observedAt:'2026-10-25T00:00:00Z',...(remote?{endpoint:approval().endpoint,transport:'http',server:{name:'fixture',version:'1'}}:{})}
 const receipt:Record<string,unknown>={receiptVersion:'3',sessionId:vector.session,callIndex:0,prevSignature:null,kind:'acquisition',authority:vector.authority,keyId:digest(publicKey).slice(7,39),source:'tickets/live',argumentsCommitment:commit('args:',call),resultDigest:digest(canon(vector.artifact)),acquisition}
 signReceipt(receipt)
 return {envelope:{session:vector.session,result:structuredClone(vector.artifact),receipt,salts:{args:'0'.repeat(64),statement:'0'.repeat(64)}},trust:{publicKey:publicKey.toString('hex'),authority:vector.authority,adapter:structuredClone(vector.adapter)} as GatewayReceiptTrust}
}
const outer=(envelope:unknown):McpToolResult=>({content:[{type:'text',text:canon(envelope)}],structuredContent:structuredClone(envelope) as McpToolResult['structuredContent']})
const signal=()=>new AbortController().signal
const verify=(raw:McpToolResult,trust:GatewayReceiptTrust,a=approval(),s=snapshot(),args=vector.args,session=vector.session,limit=GATEWAY_RECEIPT_ENVELOPE_BYTES,abort=signal())=>verifyGatewayResult(raw,trust,a,s,vector.tool,args,{receiptSession:session},limit,abort)

describe('Gateway v3 signed acquisition consumer',()=>{
 it('verifies remote and stdio signed artifact and independently pinned identity',async()=>{
  for(const remote of [true,false]){const {envelope,trust}=synthetic(remote);expect((await verify(outer(envelope),trust,approval(remote),snapshot(remote))).result).toEqual(vector.artifact)}
 })
 for(const row of vector.cases)it('refuses '+row.name,async()=>{
  const {envelope,trust}=synthetic(),r=envelope.receipt,a=r.acquisition as Record<string,unknown>
  let args=vector.args,limit=GATEWAY_RECEIPT_ENVELOPE_BYTES
  switch(row.change){
   case 'signature':r.signature='0'.repeat(128);break
   case 'key':trust.publicKey='1'.repeat(64);break
   case 'keyId':r.keyId='0'.repeat(32);signReceipt(r);break
   case 'authority':r.authority='gateway:other';signReceipt(r);break
   case 'source':r.source='tickets/write';signReceipt(r);break
   case 'session':r.sessionId='agent-'+'1'.repeat(64);envelope.session=r.sessionId as string;signReceipt(r);break
   case 'index':r.callIndex=1;signReceipt(r);break
   case 'previous':r.prevSignature='1'.repeat(128);signReceipt(r);break
   case 'args':args={id:8};break
   case 'result':envelope.result={content:[{type:'text',text:'other'}],structuredContent:{id:8}};break
   case 'statement':a.statement='sha256:'+'0'.repeat(64);signReceipt(r);break
   case 'endpoint':a.endpoint='https://other.invalid/mcp';signReceipt(r);break
   case 'server':a.server={name:'other',version:'1'};signReceipt(r);break
   case 'adapter':a.adapter={name:'other',version:'0',digest:vector.adapter.digest};signReceipt(r);break
   case 'action':r.kind='action';signReceipt(r);break
   case 'number':envelope.result.structuredContent.id=9007199254740992;break
   case 'oversize':limit=10;break
  }
  const raw=outer(envelope)
  if(row.change==='twins')raw.structuredContent={different:true}
  if(row.change==='duplicate')raw.content=[{type:'text',text:canon(envelope).replace('"session":','"session":"duplicate","session":')}]
  await expect(verify(raw,trust,approval(),snapshot(),args,vector.session,limit)).rejects.toThrow()
 })
 it('refuses malformed public inputs, invalid limits, depth and cancelled verification',async()=>{
  const {envelope,trust}=synthetic(),raw=outer(envelope)
  for(const value of [undefined,null,{}, {toolTaxonomies:null}])await expect(verifyGatewayResult(raw,trust,value as unknown as DescriptorApproval,snapshot(),vector.tool,vector.args,{receiptSession:vector.session},GATEWAY_RECEIPT_ENVELOPE_BYTES,signal())).rejects.toThrow()
  for(const limit of [0,-1,Infinity,NaN,Number.MAX_SAFE_INTEGER,GATEWAY_RECEIPT_ENVELOPE_BYTES+1])await expect(verify(raw,trust,approval(),snapshot(),vector.args,vector.session,limit)).rejects.toThrow()
  for(const session of ['', 'agent-','unbound'])await expect(verify(raw,trust,approval(),snapshot(),vector.args,session)).rejects.toThrow()
  const cyclic:Record<string,unknown>={};cyclic.cycle=cyclic
  await expect(verify({...raw,structuredContent:cyclic},trust)).rejects.toThrow()
  const controller=new AbortController();controller.abort();await expect(verify(raw,trust,approval(),snapshot(),vector.args,vector.session,GATEWAY_RECEIPT_ENVELOPE_BYTES,controller.signal)).rejects.toThrow()
 })
 it('keeps immutable invocation metadata off parent signal and through deadline wrappers',async()=>{
  const parent=signal();expect(gatewayExecutionContext(parent)).toBeUndefined()
  const sessions=await Promise.all([1,2].map(()=>withDeadline(async bounded=>{const context=beginGatewayInvocation(bounded);expect(Object.isFrozen(context)).toBe(true);return withDeadline(async child=>{expect(gatewayExecutionContext(child)).toBe(context);return context.receiptSession},bounded,1000,()=>Error('deadline'))},parent,1000,()=>Error('deadline'))))
  expect(sessions[0]).not.toBe(sessions[1]);expect(gatewayExecutionContext(parent)).toBeUndefined()
 })
 it('refuses acquisition-only verifier on action declarations',async()=>{
  const {trust}=synthetic();const t:HostTool={name:'query',taxonomy:{operation:'act',effect:'act'},description:'act',inputSchema:{type:'object'},execute:async()=>({content:[]}),source:{kind:'mcp',id:'tickets',trust:'connected',toolListDigest:digest(snapshot()),descriptorSnapshot:snapshot(),descriptorApproval:approval(),currentDescriptorSnapshot:()=>snapshot(),gatewayReceipt:trust}}
  await expect(declareSources([t],[])).rejects.toThrow('requires read-external')
 })
})

interface LiveFixture {url:string;snapshotText:string;snapshotDigest:string;tool:string;trust:GatewayReceiptTrust;approval:DescriptorApproval;stdio?:LiveFixture}
function rpc(url:string,body:unknown,transport=''):Promise<{body:Record<string,unknown>;transport:string}>{return new Promise((resolve,reject)=>{const req=request(url,{method:'POST',headers:{'content-type':'application/json',...(transport?{'Mcp-Session-Id':transport}:{})}},res=>{const chunks:Buffer[]=[];let n=0;res.on('data',(b:Buffer)=>{n+=b.length;if(n>4*GATEWAY_RECEIPT_ENVELOPE_BYTES){req.destroy(Error('oversized frontend'));return}chunks.push(b)});res.on('end',()=>{try{if(res.statusCode!==200&&res.statusCode!==202)throw Error('frontend status '+res.statusCode+': '+Buffer.concat(chunks));resolve({body:n?JSON.parse(Buffer.concat(chunks).toString()):{},transport:String(res.headers['mcp-session-id']??transport)})}catch(e){reject(e)}})});req.on('error',reject);req.setTimeout(3000,()=>req.destroy(Error('frontend timeout')));req.end(JSON.stringify(body))})}
async function callProducer(f:LiveFixture,args:Record<string,unknown>,session=''):Promise<McpToolResult>{const init=await rpc(f.url,{jsonrpc:'2.0',id:1,method:'initialize',params:{protocolVersion:'2025-06-18',capabilities:{},clientInfo:{name:'agent-receipt-test',version:'1'}}});await rpc(f.url,{jsonrpc:'2.0',method:'notifications/initialized'},init.transport);const out=await rpc(f.url,{jsonrpc:'2.0',id:2,method:'tools/call',params:{name:f.approval.platform+'.'+f.tool,arguments:args,...(session?{_meta:{'io.judgment-pack/session':session}}:{})}},init.transport);return out.body.result as McpToolResult}
async function liveGate(f:LiveFixture,run:HostTool['execute'],bounds=CONTEXT_DEFAULTS){
 const t:HostTool={name:f.tool,taxonomy:f.approval.toolTaxonomies[f.tool],description:'approved',inputSchema:{type:'object'},execute:run,source:{kind:'mcp',id:'tickets',trust:'connected',toolListDigest:f.snapshotDigest,descriptorSnapshot:bytes(f.snapshotText),descriptorApproval:f.approval,gatewayReceipt:f.trust,currentDescriptorSnapshot:()=>bytes(f.snapshotText),verifyReceipt:()=>{throw Error('custom fallback must not run')}}}
 const admitted=await declareSources([t],[]);expect(admitted.hostTools).toHaveLength(1);const rows:SourceFigures[]=[]
 return {gate:new SourceGate(admitted.entries,admitted.sources,bounds,'marker',(name,detail)=>new GateViolation(name,detail),()=>rows),tool:admitted.hostTools[0]!,rows}
}
it.skipIf(!process.env.JP_AGENT_GATEWAY_FIXTURE)('verifies actual frozen Gateway frontend through SourceGate and refuses replay/swaps/missing session',async()=>{
 const fixtures=JSON.parse(readFileSync(process.env.JP_AGENT_GATEWAY_FIXTURE!,'utf8')) as LiveFixture;expect(fixtures.stdio).toBeDefined()
 for(const f of [fixtures,fixtures.stdio!]){const parent=signal();let old:McpToolResult|undefined
 const run:HostTool['execute']=async(args,bounded)=>{const invocation=gatewayExecutionContext(bounded!);expect(invocation).toBeDefined();old=await callProducer(f,args,invocation!.receiptSession);return old}
 const {gate,tool,rows}=await liveGate(f,run)
 expect((await gate.call(tool,{id:7},parent,tool.execute)).content).toBeDefined();expect(gatewayExecutionContext(parent)).toBeUndefined()
 const captured=(old!.structuredContent as unknown as {session:string}).session
 for(const kind of ['key','binding','args','result']){const trust=structuredClone(f.trust),a=structuredClone(f.approval);let given={id:7},raw=structuredClone(old!)
  if(kind==='key')trust.publicKey='1'.repeat(64)
  if(kind==='binding')a.binding='other@sha256:'+'0'.repeat(64)
  if(kind==='args')given={id:8}
  if(kind==='result'){const part=raw.content?.[0];if(!part||part.type!=='text'||typeof part.text!=='string')throw Error('actual producer text envelope missing');const e=JSON.parse(part.text);e.result={content:[{type:'text',text:'changed'}]};raw=outer(e)}
  await expect(verifyGatewayResult(raw,trust,a,bytes(f.snapshotText),f.tool,given,{receiptSession:captured},GATEWAY_RECEIPT_ENVELOPE_BYTES,parent)).rejects.toThrow()
 }

 await expect(gate.call(tool,{id:7},parent,async()=>old!)).rejects.toMatchObject({failure:{code:'tool-failed',outcome:'unknown'}})
 await expect(gate.call(tool,{id:7},parent,args=>callProducer(f,args))).rejects.toThrow('receipt verification failed')
 expect(rows.find(row=>row.source==='tickets')).toMatchObject({calls:3,refused:2})
 const deadline=await liveGate(f,async()=>{await new Promise(resolve=>setTimeout(resolve,20));return old!},{...CONTEXT_DEFAULTS,sourceCallMilliseconds:5})
 await expect(deadline.gate.call(deadline.tool,{id:7},parent,deadline.tool.execute)).rejects.toThrow()
 }
})

it.skipIf(!process.env.JP_AGENT_GATEWAY_FIXTURE)('refuses actual producer concurrent same-args receipt swap',async()=>{const f=JSON.parse(readFileSync(process.env.JP_AGENT_GATEWAY_FIXTURE!,'utf8')) as LiveFixture,parent=signal();const run:HostTool['execute']=async()=>({content:[]})
 const concurrent=await liveGate(f,run),held:McpToolResult[]=[],sessions:string[]=[];let arrivals=0;let release!:()=>void;const both=new Promise<void>(resolve=>{release=resolve})
 const outcomes=await Promise.allSettled([0,1].map(i=>concurrent.gate.call(concurrent.tool,{id:7},parent,async(args,bounded)=>{const invocation=gatewayExecutionContext(bounded)!;sessions[i]=invocation.receiptSession;held[i]=await callProducer(f,args,invocation.receiptSession);if(++arrivals===2)release();await both;return held[1-i]!})))
 expect(sessions[0]).not.toBe(sessions[1]);for(const outcome of outcomes){expect(outcome.status).toBe('rejected');expect((outcome as PromiseRejectedResult).reason).toMatchObject({failure:{code:'tool-failed',outcome:'unknown'}})}
})

it('holds full approved snapshot beyond selected subset and refuses restored acquisition-only actions',async()=>{
 const {trust}=synthetic();let current=snapshot(),calls=0
 const t:HostTool={name:'query',taxonomy:{operation:'read',effect:'read-external'},description:'approved',inputSchema:{type:'object'},execute:async()=>{calls++;return {content:[]}},source:{kind:'mcp',id:'tickets',trust:'connected',toolListDigest:digest(snapshot()),descriptorSnapshot:snapshot(),descriptorApproval:approval(),currentDescriptorSnapshot:()=>current,gatewayReceipt:trust}}
 const admitted=await declareSources([t],[]),rows:SourceFigures[]=[],gate=new SourceGate(admitted.entries,admitted.sources,CONTEXT_DEFAULTS,'marker',(name,detail)=>new GateViolation(name,detail),()=>rows),offered=admitted.hostTools[0]!
 const text=Buffer.from(current).toString();const changed=text.replace('"sibling":{"taxonomy":{"effect":"read-external","operation":"read"}}','"sibling":{"taxonomy":{"effect":"act","operation":"act"}}');expect(changed).not.toBe(text);current=bytes(changed)
 await expect(gate.call(offered,{},signal(),offered.execute)).rejects.toThrow();expect(calls).toBe(0)
 current=snapshot();const restored=await declareSources([t],[]),entry=restored.entries.get(offered.name)!;Object.assign(entry,{effectClass:'act',approval:'required'});entry.declared.taxonomy={operation:'act',effect:'act'}
 const restoredGate=new SourceGate(restored.entries,restored.sources,CONTEXT_DEFAULTS,'marker',(name,detail)=>new GateViolation(name,detail),()=>rows)
 await expect(restoredGate.call(restored.hostTools[0]!,{},signal(),offered.execute)).rejects.toThrow('requires read-external');expect(calls).toBe(0)
 expect(await restoredGate.approved({name:offered.name,source:entry.record,callId:'restored',tool:'query',args:{},state:'approved'},signal(),offered.execute)).toMatchObject({ran:false});expect(calls).toBe(0)
})
