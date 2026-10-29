/** Gateway v3 acquisition verification. Trust pins are supplied by the host, never the response. */
import {readDescriptorJSON,readDescriptorSnapshot,descriptorApprovalProblem,type DescriptorApproval} from './descriptorSnapshot'
import type {McpToolResult} from './engine'
export const GATEWAY_RECEIPT_ENVELOPE_BYTES=(8<<20)+4096
/** Omitted adapter pins leave executable identity unverified. */
export interface GatewayReceiptTrust {publicKey:string;authority:string;adapter?:{name:string;version:string;digest:string}}
export interface GatewayExecutionContext {readonly receiptSession:string}
const invocations=new WeakMap<AbortSignal,Readonly<GatewayExecutionContext>>()
/** Read-only executor context; absent outside an admitted Gateway invocation. Never model input. */
export function gatewayExecutionContext(signal:AbortSignal):Readonly<GatewayExecutionContext>|undefined{return invocations.get(signal)}
/** @internal Called only on the distinct bounded signal created for this invocation. */
export function beginGatewayInvocation(signal:AbortSignal):Readonly<GatewayExecutionContext>{
 if(invocations.has(signal))throw Error('Gateway invocation context already exists')
 const nonce=crypto.getRandomValues(new Uint8Array(32))
 const context=Object.freeze({receiptSession:'agent-'+hex(nonce)})
 invocations.set(signal,context);return context
}
/** @internal Package-owned deadline wrappers carry the same immutable context. */
export function inheritGatewayInvocation(parent:AbortSignal,child:AbortSignal):void{const context=invocations.get(parent);if(context)invocations.set(child,context)}
const object=(v:unknown):v is Record<string,unknown>=>typeof v==='object'&&v!==null&&!Array.isArray(v)
const hex=(v:Uint8Array)=>Array.from(v,b=>b.toString(16).padStart(2,'0')).join('')
const unhex=(v:unknown,n:number):Uint8Array<ArrayBuffer>=>{if(typeof v!=='string'||!new RegExp(`^[0-9a-f]{${n*2}}$`).test(v))throw Error('invalid hex');return Uint8Array.from(v.match(/../g)!,x=>parseInt(x,16))}
const bytes=(s:string)=>Uint8Array.from(new TextEncoder().encode(s))
const pointOrder=(a:string,b:string)=>{const l=[...a],r=[...b];for(let i=0;i<Math.min(l.length,r.length);i++){const d=l[i]!.codePointAt(0)!-r[i]!.codePointAt(0)!;if(d)return d}return l.length-r.length}
/** Only values round-trippable through the Gateway integer domain are accepted. */
function canonical(v:unknown):string{
 if(Array.isArray(v))return '['+v.map(canonical).join(',')+']'
 if(object(v))return '{'+Object.keys(v).sort(pointOrder).map(k=>JSON.stringify(k)+':'+canonical(v[k])).join(',')+'}'
 if(typeof v==='number'&&(!Number.isSafeInteger(v)||Object.is(v,-0)))throw Error('unsupported numeric value')
 if(v===null||typeof v==='string'||typeof v==='boolean'||typeof v==='number')return JSON.stringify(v)
 throw Error('unsupported value')
}
async function digest(v:Uint8Array):Promise<string>{return 'sha256:'+hex(new Uint8Array(await crypto.subtle.digest('SHA-256',Uint8Array.from(v))))}
async function commitment(salt:unknown,label:string,value:unknown):Promise<string>{const a=unhex(salt,32),b=bytes(label+canonical(value)),joined=new Uint8Array(a.length+b.length);joined.set(a);joined.set(b,a.length);return digest(joined)}
export function gatewayTrustProblem(trust:GatewayReceiptTrust|undefined):string{
 if(!object(trust)||Object.keys(trust).some(k=>!['publicKey','authority','adapter'].includes(k))||typeof trust.publicKey!=='string'||!/[0-9a-f]{64}/.test(trust.publicKey)||trust.publicKey.length!==64||typeof trust.authority!=='string'||trust.authority.length<1||trust.authority.length>256)return 'invalid pinned Gateway receipt trust'
 if(trust.adapter!==undefined&&(!object(trust.adapter)||Object.keys(trust.adapter).length!==3||Object.keys(trust.adapter).some(k=>!['name','version','digest'].includes(k))||typeof trust.adapter.name!=='string'||trust.adapter.name.length<1||trust.adapter.name.length>2048||typeof trust.adapter.version!=='string'||trust.adapter.version.length>256||typeof trust.adapter.digest!=='string'||!/^sha256:[0-9a-f]{64}$/.test(trust.adapter.digest)))return 'invalid pinned Gateway adapter identity'
 return ''
}
/** Verify the actual Gateway MCP envelope, then return only its signed artifact as the tool result.
 * The receipt authenticates its fields, not the independently approved descriptor taxonomy.
 */
export async function verifyGatewayResult(raw:McpToolResult,trust:GatewayReceiptTrust,approval:DescriptorApproval,snapshotBytes:Uint8Array,tool:string,args:Record<string,unknown>,invocation:Readonly<GatewayExecutionContext>,maxBytes:number,signal:AbortSignal):Promise<{result:McpToolResult;receipt:string}>{
 signal.throwIfAborted()
 if(!Number.isSafeInteger(maxBytes)||maxBytes<=0||maxBytes>GATEWAY_RECEIPT_ENVELOPE_BYTES||!object(invocation)||typeof invocation.receiptSession!=='string'||!/^agent-[0-9a-f]{64}$/.test(invocation.receiptSession)||!object(approval)||!object(args)||typeof tool!=='string'||tool.length<1||tool.length>128)throw Error('invalid Gateway verification input')
 if(gatewayTrustProblem(trust))throw Error('invalid Gateway trust')
 const snapshot=readDescriptorSnapshot(snapshotBytes)
 if(descriptorApprovalProblem(snapshot,approval)||descriptorApprovalProblem(snapshot,approval,tool,approval.toolTaxonomies[tool]))throw Error('unapproved Gateway source')
 if(!object(raw)||Object.keys(raw).some(k=>!['content','structuredContent','isError'].includes(k))||raw.isError===true||!Array.isArray(raw.content)||raw.content.length!==1)throw Error('invalid Gateway envelope')
 const part=raw.content[0]
 if(!object(part)||Object.keys(part).some(k=>!['type','text'].includes(k))||part.type!=='text'||typeof part.text!=='string'||bytes(part.text).length>maxBytes)throw Error('unbounded Gateway envelope')
 boundValue(raw,4*maxBytes+1024)
 boundValue(args,1<<20)
 const complete=JSON.stringify(raw)
 if(bytes(complete).length>4*maxBytes+1024)throw Error('unbounded Gateway wrapper')
 const twin=readDescriptorJSON(JSON.stringify(raw.structuredContent),true)
 const envelope=readDescriptorJSON(part.text,true)
 if(!object(envelope)||Object.keys(envelope).some(k=>!['session','result','receipt','salts'].includes(k))||Object.keys(envelope).length!==4||canonical(envelope)!==canonical(twin))throw Error('Gateway envelope representations differ')
 const receipt=envelope.receipt,salts=envelope.salts,artifact=envelope.result
 // The salts are unsigned: only the two the commitments open are admitted (#242: a verified envelope is retained whole, so any other member would be unverified bytes kept as context).
 if(!object(receipt)||!object(salts)||!object(artifact)||Object.hasOwn(artifact,'receipt')||Object.keys(salts).some(k=>k!=='args'&&k!=='statement'))throw Error('missing signed Gateway evidence')
 if(envelope.session!==invocation.receiptSession||receipt.sessionId!==invocation.receiptSession||receipt.callIndex!==0||receipt.prevSignature!==null)throw Error('receipt is not this invocation')
 if(receipt.receiptVersion!=='3'||receipt.kind!=='acquisition'||Object.hasOwn(receipt,'action')||receipt.authority!==trust.authority||receipt.source!==approval.platform+'/'+approval.operation)throw Error('wrong Gateway receipt source')
 const key=unhex(trust.publicKey,32),signature=unhex(receipt.signature,64)
 if(receipt.keyId!==(await digest(key)).slice(7,39))throw Error('wrong Gateway signing key')
 const unsigned={...receipt};delete unsigned.signature
 const publicKey=await crypto.subtle.importKey('raw',key,{name:'Ed25519'},false,['verify'])
 if(!await crypto.subtle.verify('Ed25519',publicKey,signature,bytes('judgment-pack-gateway/receipt/3:'+canonical(unsigned))))throw Error('invalid Gateway signature')
 if(receipt.argumentsCommitment!==await commitment(salts.args,'args:',{tool,arguments:args})||receipt.resultDigest!==await digest(bytes(canonical(artifact))))throw Error('Gateway receipt does not bind call/result')
 const acquisition=receipt.acquisition
 if(!object(acquisition)||acquisition.shape!=='mcp')throw Error('not an MCP acquisition')
 const adapter=acquisition.adapter
 if(!object(adapter)||Object.keys(adapter).length!==3||typeof adapter.name!=='string'||adapter.name.length<1||adapter.name.length>2048||typeof adapter.version!=='string'||adapter.version.length>256||typeof adapter.digest!=='string'||!/^sha256:[0-9a-f]{64}$/.test(adapter.digest)||Object.keys(adapter).some(k=>!['name','version','digest'].includes(k)))throw Error('invalid signed adapter identity')
 if(trust.adapter!==undefined&&canonical(adapter)!==canonical(trust.adapter))throw Error('Gateway adapter differs from independent pin')
 if(acquisition.statement!==await commitment(salts.statement,'statement:',canonical({tool,arguments:args})))throw Error('Gateway statement differs from call')
 if(approval.endpoint!==undefined){
  if(acquisition.transport!=='http'||acquisition.endpoint!==approval.endpoint||canonical(acquisition.server)!==canonical(snapshot.server))throw Error('Gateway endpoint/server differs from approval')
 }else if(Object.hasOwn(acquisition,'transport')||Object.hasOwn(acquisition,'server'))throw Error('unexpected remote acquisition')
 if(artifact.content!==undefined&&!Array.isArray(artifact.content)||artifact.isError!==undefined&&typeof artifact.isError!=='boolean')throw Error('invalid signed MCP artifact')
 signal.throwIfAborted()
 return {result:structuredClone(artifact) as unknown as McpToolResult,receipt:receipt.signature as string}
}

/** Bound a caller-supplied tree before serialization and refuse cycles/unsupported values. */
function boundValue(value:unknown,remaining:number):void {
 const visit=(v:unknown,depth:number):void=>{
  if(depth>128||--remaining<0)throw Error('unbounded Gateway value')
  if(typeof v==='string')remaining-=v.length
  else if(Array.isArray(v)){for(const item of v)visit(item,depth+1)}
  else if(object(v)){for(const key of Object.keys(v)){remaining-=key.length+3;visit(v[key],depth+1)}}
  else if(v===null||typeof v==='boolean'||typeof v==='number')remaining-=32
  else throw Error('unsupported Gateway value')
  if(remaining<0)throw Error('unbounded Gateway value')
 };visit(value,0)
}
