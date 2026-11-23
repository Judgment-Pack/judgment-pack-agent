// @vitest-environment node
// Spec RFC 0030's amendment: ES256 delegation statements (§2, Signing with a P-256 key) and a chain bound to one
// scope identifier (§2, Binding a chain to one scope identifier; §3, clause 9), against the specification's frozen
// cases and the library's own scope-binding vectors.
import {describe,it,expect} from 'vitest'
import {createHash} from 'node:crypto'
import {readFileSync} from 'node:fs'
import citation from '../../contract/testdata/principal/spec-citation.json'
import scope from '../../contract/vectors/principal/scope-binding.json'
import {verifyChain,pinKeys,p256Point,scopeIdentifier,openPrincipal,admitCall,DelegationRefused,PrincipalInputInvalid,type AdmissionRead,type PrincipalCall} from './principal'

type Case={name:string;table?:number;kind:string;chain:unknown;expect:string;link?:number;keys?:Record<string,unknown[]>;pin?:unknown;pinExpect?:string}
const copy=(name:string)=>readFileSync(new URL('../../contract/testdata/principal/'+name,import.meta.url))
const outcome=async(chain:unknown,keys:Record<string,unknown>)=>{try{await verifyChain(JSON.stringify(chain),await pinKeys(keys));return {name:'ok'}}catch(error){if(error instanceof DelegationRefused)return {name:error.refusal.name,message:error.message};throw error}}

describe('the specification cases, copied byte for byte',()=>{
 for(const file of citation.files)it(file.copy+' is the bytes the specification states',()=>{
  const bytes=copy(file.copy)
  expect(createHash('sha256').update(bytes).digest('hex')).toBe(file.sha256)
  const parsed=JSON.parse(bytes.toString('utf8')) as {cases?:unknown[];vectors?:unknown[]}
  expect((parsed.cases??parsed.vectors)!.length).toBe(file.cases)
 })
})

const es256=JSON.parse(copy('spec-es256-statements.json').toString('utf8')) as {keys:Record<string,unknown[]>;cases:Case[]}
describe('ES256 statements: tables 1 and 2, steps 1 to 7',()=>{
 for(const c of es256.cases)it(c.name,async()=>{
  if(c.pinExpect==='refused')await expect(pinKeys({desk:[c.pin]})).rejects.toBeInstanceOf(PrincipalInputInvalid)
  const got=await outcome(c.chain,c.keys??es256.keys)
  expect(got.name).toBe(c.expect)
  if(c.link!==undefined)expect(got.message).toContain(`link ${c.link} `)
 })
 it('reads the algorithm from the pin, never from a key\'s length or form',async()=>{
  const p256=(es256.keys.desk as unknown[]).find(k=>typeof k==='object') as {key:string}
  // A compressed point given as a bare string is an Ed25519 pin, which its form does not meet: refused, not read as P-256.
  await expect(pinKeys({desk:[p256.key]})).rejects.toBeInstanceOf(PrincipalInputInvalid)
  // An Ed25519 key given as ES256 is refused too.
  await expect(pinKeys({desk:[{alg:'ES256',key:es256.keys.desk![0]}]})).rejects.toBeInstanceOf(PrincipalInputInvalid)
  await expect(pinKeys({desk:[{alg:'Ed25519',key:es256.keys.desk![0]}]})).rejects.toBeInstanceOf(PrincipalInputInvalid)
  await expect(pinKeys({desk:[{alg:'ES256',key:p256.key,extra:true}]})).rejects.toBeInstanceOf(PrincipalInputInvalid)
 })
 it('decompresses a point of either parity to the one on the curve, and refuses the uncompressed form',()=>{
  const even=p256Point('026ff03b949241ce1dadd43519e6960e0a85b41a69a05c328103aa2bce1594ca16'),odd=p256Point('0307810ea974cea5773e63b897f37e3be9a09e7a5fe9b971a44d1065ac2a3a9311')
  expect(even).toBeInstanceOf(Uint8Array);expect(odd).toBeInstanceOf(Uint8Array)
  expect((even as Uint8Array)[64]!&1).toBe(0);expect((odd as Uint8Array)[64]!&1).toBe(1)
  expect(p256Point('04'+'00'.repeat(64))).toBe('key-malformed')
  expect(p256Point('02ffffffff00000001000000000000000000000000ffffffffffffffffffffffff')).toBe('key-not-canonical')
  expect(p256Point('020000000000000000000000000000000000000000000000000000000000000001')).toBe('key-not-on-curve')
 })
})

const ed=JSON.parse(copy('spec-ed25519-statements.json').toString('utf8')) as {keys:Record<string,string[]>;vectors:Case[]}
describe('Ed25519 statements, unchanged',()=>{
 for(const c of ed.vectors)it(c.name,async()=>{expect((await outcome(c.chain,ed.keys)).name).toBe(c.expect)})
})

const chainOf=(name:string)=>scope.statements.find(s=>s.name===name)!.chain
describe('the scope binding: statements (step 3, clause 9)',()=>{
 for(const c of scope.statements as unknown as Case[])it(c.name,async()=>{
  const got=await outcome(c.chain,scope.keys)
  expect(got.name).toBe(c.expect)
  if(c.link!==undefined)expect(got.message).toContain(`link ${c.link} `)
 })
 it('binds the verified chain to the root\'s scope identifier, and an unbound chain to none',async()=>{
  const keys=await pinKeys(scope.keys)
  expect((await verifyChain(JSON.stringify(chainOf('bound-chain')),keys)).scopeId).toBe('scope-a')
  expect(Object.hasOwn(await verifyChain(JSON.stringify(chainOf('unbound-root')),keys),'scopeId')).toBe(false)
 })
 it('holds a scope identifier to its form, as written',()=>{
  for(const v of ['scope-a','Scope-A é','s'.repeat(128),'é'.repeat(64)])expect(scopeIdentifier(v)).toBe(true)
  for(const v of ['','unscoped','é'.repeat(65),'a\tb','a\nb','a​b','a‮b','ab','a﷐b','a͸b','a\u{2ebf0}b','\ud800',7,null])expect(scopeIdentifier(v)).toBe(false)
 })
})

describe('the scope binding: requests (before step 8 and any admission read)',()=>{
 for(const r of scope.requests)it(r.name,async()=>{
  const reads:AdmissionRead[]=[]
  const chain=chainOf(r.chain)
  const at=(r as {at?:string}).at??'2026-10-30T01:00:00.000Z'
  const principal=await openPrincipal({chain:JSON.stringify(chain),keys:scope.keys as never,software:scope.software,audience:scope.audience,clock:()=>Date.parse(at),admission:read=>{reads.push(read);return {admission:'admit'}}})
  const refusal=await admitCall(principal,r.call as unknown as PrincipalCall,true)
  expect(refusal===undefined?'admit':refusal.name).toBe(r.expect)
  expect(reads.length).toBe(r.reads)
 })
})
