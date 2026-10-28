import {canonicalJSON} from '../context/chat'
import {sha256} from '../records'
export const HISTORY_OBJECT_BYTES=4*1024*1024,HISTORY_TOTAL_BYTES=64*1024*1024,HISTORY_MAX_ROWS=20000,HISTORY_MAX_EVENTS=10000
export class HistoryInputInvalid extends Error{constructor(){super('HistoryInputInvalid: the retained history input is invalid.');this.name='HistoryInputInvalid'}}
export class HistoryChanged extends Error{constructor(){super('HistoryChanged: the retained history view changed.');this.name='HistoryChanged'}}
export const historyText=(bytes:Uint8Array)=>new TextDecoder('utf-8',{fatal:true,ignoreBOM:true}).decode(bytes)
export const historyJSONBytes=(value:unknown)=>new TextEncoder().encode(canonicalJSON(value))
export const historyJSONDigest=(value:unknown)=>`sha256:${sha256(canonicalJSON(value))}`
export const historyDigestPattern=/^sha256:[a-f0-9]{64}$/
export interface HistoryObjectRef{digest:string;bytes:number}
export interface HistoryObject extends HistoryObjectRef{value:Uint8Array}
const typedArrayPrototype=Object.getPrototypeOf(Uint8Array.prototype)
const typedTag=Object.getOwnPropertyDescriptor(typedArrayPrototype,Symbol.toStringTag)!.get!
const typedByteLength=Object.getOwnPropertyDescriptor(typedArrayPrototype,'byteLength')!.get!
const typedByteOffset=Object.getOwnPropertyDescriptor(typedArrayPrototype,'byteOffset')!.get!
const typedBuffer=Object.getOwnPropertyDescriptor(typedArrayPrototype,'buffer')!.get!
export function copyHistoryBytes(value:Uint8Array,max=HISTORY_OBJECT_BYTES):Uint8Array{
 try{
  if(!ArrayBuffer.isView(value)||typedTag.call(value)!=='Uint8Array')throw new HistoryInputInvalid()
  const length=typedByteLength.call(value) as number
  if(length<1||length>max)throw new HistoryInputInvalid()
  const copy=new Uint8Array(length)
  copy.set(new Uint8Array(typedBuffer.call(value) as ArrayBuffer,typedByteOffset.call(value) as number,length))
  return copy
 }catch{throw new HistoryInputInvalid()}
}

export function validHistoryRef(value:unknown):value is HistoryObjectRef{
 if(!value||typeof value!=='object'||Array.isArray(value)||Object.getOwnPropertySymbols(value).length)return false
 const descriptors=Object.getOwnPropertyDescriptors(value),keys=Object.keys(descriptors)
 if(keys.length!==2||!['digest','bytes'].every(key=>descriptors[key]&&'value'in descriptors[key]&&descriptors[key].enumerable))return false
 const digest=descriptors.digest!.value,bytes=descriptors.bytes!.value
 return typeof digest==='string'&&historyDigestPattern.test(digest)&&Number.isSafeInteger(bytes)&&bytes>0&&bytes<=HISTORY_OBJECT_BYTES
}

export async function historyObject(value:Uint8Array):Promise<HistoryObject>{
 const bytes=copyHistoryBytes(value)
 return {digest:historyBytesDigest(bytes),bytes:bytes.length,value:bytes}
}
export interface HistoryChunkManifest{version:1;bytes:number;digest:string;parts:HistoryObjectRef[]}
export async function chunkHistoryValue(value:Uint8Array):Promise<{manifest:HistoryObject;objects:HistoryObject[]}>{
 const bytes=copyHistoryBytes(value,HISTORY_TOTAL_BYTES)
 const objects:HistoryObject[]=[]
 for(let offset=0;offset<bytes.length;offset+=HISTORY_OBJECT_BYTES)objects.push(await historyObject(bytes.slice(offset,offset+HISTORY_OBJECT_BYTES)))
 const manifest:HistoryChunkManifest={version:1,bytes:bytes.length,digest:historyBytesDigest(bytes),parts:objects.map(({digest,bytes})=>({digest,bytes}))}
 return {manifest:await historyObject(historyJSONBytes(manifest)),objects}
}
export function readHistoryChunkManifest(bytes:Uint8Array):HistoryChunkManifest{
 try{
  const v=readCanonicalHistoryJSON(bytes,HISTORY_OBJECT_BYTES) as HistoryChunkManifest
  if(!v||Object.keys(v).some(k=>!['version','bytes','digest','parts'].includes(k))||v.version!==1||!Number.isSafeInteger(v.bytes)||v.bytes<1||v.bytes>HISTORY_TOTAL_BYTES||!historyDigestPattern.test(v.digest)||!Array.isArray(v.parts)||v.parts.length<1||v.parts.length>16||!v.parts.every(validHistoryRef)||v.parts.reduce((sum,p)=>sum+p.bytes,0)!==v.bytes||v.parts.some((p,i)=>i<v.parts.length-1&&p.bytes!==HISTORY_OBJECT_BYTES))throw new HistoryInputInvalid()
  return v
 }catch{throw new HistoryInputInvalid()}
}

export function historyBytesDigest(bytes:Uint8Array):string{
 const bits=bytes.length*8,padded=new Uint8Array(((bytes.length+9+63)>>6)<<6)
 padded.set(bytes);padded[bytes.length]=0x80
 const view=new DataView(padded.buffer);view.setUint32(padded.length-4,bits>>>0);view.setUint32(padded.length-8,Math.floor(bits/0x1_0000_0000))
 const k=[0x428a2f98,0x71374491,0xb5c0fbcf,0xe9b5dba5,0x3956c25b,0x59f111f1,0x923f82a4,0xab1c5ed5,0xd807aa98,0x12835b01,0x243185be,0x550c7dc3,0x72be5d74,0x80deb1fe,0x9bdc06a7,0xc19bf174,0xe49b69c1,0xefbe4786,0x0fc19dc6,0x240ca1cc,0x2de92c6f,0x4a7484aa,0x5cb0a9dc,0x76f988da,0x983e5152,0xa831c66d,0xb00327c8,0xbf597fc7,0xc6e00bf3,0xd5a79147,0x06ca6351,0x14292967,0x27b70a85,0x2e1b2138,0x4d2c6dfc,0x53380d13,0x650a7354,0x766a0abb,0x81c2c92e,0x92722c85,0xa2bfe8a1,0xa81a664b,0xc24b8b70,0xc76c51a3,0xd192e819,0xd6990624,0xf40e3585,0x106aa070,0x19a4c116,0x1e376c08,0x2748774c,0x34b0bcb5,0x391c0cb3,0x4ed8aa4a,0x5b9cca4f,0x682e6ff3,0x748f82ee,0x78a5636f,0x84c87814,0x8cc70208,0x90befffa,0xa4506ceb,0xbef9a3f7,0xc67178f2]
 const h=[0x6a09e667,0xbb67ae85,0x3c6ef372,0xa54ff53a,0x510e527f,0x9b05688c,0x1f83d9ab,0x5be0cd19],w=new Uint32Array(64),rr=(x:number,n:number)=>(x>>>n)|(x<<(32-n))
 for(let offset=0;offset<padded.length;offset+=64){for(let i=0;i<16;i++)w[i]=view.getUint32(offset+i*4);for(let i=16;i<64;i++){const a=w[i-15]!,b=w[i-2]!,s0=rr(a,7)^rr(a,18)^(a>>>3),s1=rr(b,17)^rr(b,19)^(b>>>10);w[i]=(w[i-16]!+s0+w[i-7]!+s1)>>>0}let [a,b,c,d,e,f,g,z]=h;for(let i=0;i<64;i++){const s1=rr(e!,6)^rr(e!,11)^rr(e!,25),ch=(e!&f!)^(~e!&g!),t1=(z!+s1+ch+k[i]!+w[i]!)>>>0,s0=rr(a!,2)^rr(a!,13)^rr(a!,22),maj=(a!&b!)^(a!&c!)^(b!&c!),t2=(s0+maj)>>>0;z=g;g=f;f=e;e=(d!+t1)>>>0;d=c;c=b;b=a;a=(t1+t2)>>>0}for(let i=0;i<8;i++)h[i]=(h[i]!+[a,b,c,d,e,f,g,z][i]!)>>>0}
 return 'sha256:'+h.map(n=>n.toString(16).padStart(8,'0')).join('')
}
/** New portable envelopes require canonical bytes. This detects duplicate names and lossy numeric
 * spellings by byte comparison, after an iterative depth/node/domain check and before recursion. */
export function readCanonicalHistoryJSON(bytes:Uint8Array,max=HISTORY_TOTAL_BYTES,numbers:'integer'|'finite'='integer'):unknown{
 try{
  const text=historyText(copyHistoryBytes(bytes,max)),root:unknown=JSON.parse(text),stack=[{value:root,depth:0}];let nodes=0
  while(stack.length){
   const {value,depth}=stack.pop()!
   if(++nodes>bytes.length||depth>64)throw new HistoryInputInvalid()
   if(typeof value==='number'&&!(numbers==='integer'?Number.isSafeInteger(value):Number.isFinite(value)))throw new HistoryInputInvalid()
   if(typeof value==='string'&&/[\uD800-\uDBFF](?![\uDC00-\uDFFF])|(?<![\uD800-\uDBFF])[\uDC00-\uDFFF]/u.test(value))throw new HistoryInputInvalid()
   if(Array.isArray(value)){for(const child of value)stack.push({value:child,depth:depth+1})}
   else if(value&&typeof value==='object')for(const [key,child] of Object.entries(value)){if(/[\uD800-\uDBFF](?![\uDC00-\uDFFF])|(?<![\uD800-\uDBFF])[\uDC00-\uDFFF]/u.test(key))throw new HistoryInputInvalid();stack.push({value:child,depth:depth+1})}
  }
  if(canonicalJSON(root)!==text)throw new HistoryInputInvalid()
  return root
 }catch{throw new HistoryInputInvalid()}
}

/** Opaque prompt source uses JSON.stringify order/numbers, with portable Unicode and bounded depth. */
export function readHistorySourceJSON(source:string):unknown{
 try{const bytes=new TextEncoder().encode(source);if(bytes.length<1||bytes.length>HISTORY_TOTAL_BYTES)throw new HistoryInputInvalid();const root:unknown=JSON.parse(source),stack=[{value:root,depth:0}];let nodes=0
 while(stack.length){const {value,depth}=stack.pop()!;if(++nodes>bytes.length||depth>64)throw new HistoryInputInvalid();if(typeof value==='number'&&!Number.isFinite(value))throw new HistoryInputInvalid();if(typeof value==='string'&&/[\uD800-\uDBFF](?![\uDC00-\uDFFF])|(?<![\uD800-\uDBFF])[\uDC00-\uDFFF]/u.test(value))throw new HistoryInputInvalid();if(Array.isArray(value)){for(const child of value)stack.push({value:child,depth:depth+1})}else if(value&&typeof value==='object')for(const [key,child]of Object.entries(value)){if(/[\uD800-\uDBFF](?![\uDC00-\uDFFF])|(?<![\uD800-\uDBFF])[\uDC00-\uDFFF]/u.test(key))throw new HistoryInputInvalid();stack.push({value:child,depth:depth+1})}}
 if(JSON.stringify(root)!==source)throw new HistoryInputInvalid();return root
 }catch{throw new HistoryInputInvalid()}
}

/** Retained request text is opaque lexical JSON, independently of canonical outer envelopes. */
export function readHistoryRequestJSON(source:string):unknown{
 let at=0,nodes=0
 const bytes=new TextEncoder().encode(source).length,invalid=():never=>{throw new HistoryInputInvalid()}
 if(bytes<1||bytes>HISTORY_TOTAL_BYTES)invalid()
 const space=()=>{while(/[\x20\t\n\r]/.test(source[at]??'\0'))at++}
 const string=()=>{const start=at++;while(at<source.length){const ch=source[at++]!;if(ch==='\\')at++;else if(ch==='"'){const value:unknown=JSON.parse(source.slice(start,at));if(typeof value!=='string'||/[\uD800-\uDBFF](?![\uDC00-\uDFFF])|(?<![\uD800-\uDBFF])[\uDC00-\uDFFF]/u.test(value))return invalid();return value}}return invalid()}
 const value=(depth:number):unknown=>{
  if(depth>64||++nodes>bytes)return invalid()
  space();const ch=source[at]
  if(ch==='"')return string()
  if(ch==='{'||ch==='['){at++;space();const array=ch==='[',end=array?']':'}',out:unknown[]|Record<string,unknown>=array?[]:Object.create(null)
   if(source[at]===end){at++;return out}
   for(;;){space();let key='';if(!array){if(source[at]!=='"')return invalid();key=string();if(Object.hasOwn(out,key))return invalid();space();if(source[at++]!==':')return invalid()}
    const child=value(depth+1);if(array)(out as unknown[]).push(child);else(out as Record<string,unknown>)[key]=child
    space();const token=source[at++];if(token===end)return out;if(token!==',')return invalid()
   }
  }
  for(const [token,result]of [['true',true],['false',false],['null',null]]as const)if(source.startsWith(token,at)){at+=token.length;return result}
  const match=/^-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?/.exec(source.slice(at));if(!match||!Number.isFinite(Number(match[0])))return invalid();at+=match[0].length;return Number(match[0])
 }
 try{const out=value(0);space();if(at!==source.length)return invalid();return out}catch{return invalid()}
}
