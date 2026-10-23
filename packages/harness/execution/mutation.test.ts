import {describe,it,expect} from 'vitest'
import {join,resolve} from 'node:path'
import {readFileSync,readdirSync,mkdtempSync,writeFileSync} from 'node:fs'
import {tmpdir} from 'node:os'
import {API,type Snapshot} from 'typescript/unstable/sync'
import * as ts from 'typescript/unstable/ast'
import {CHECKPOINT_MEMBERS,CheckpointIncrement,CheckpointMutation} from './mutation'
import type {ExecutionCheckpoint} from './state'

const seed=():ExecutionCheckpoint=>({version:1,id:'mutation',requestKey:'a'.repeat(64),phase:'work',status:'running',unit:'model-requests',used:0,limit:20,grants:[],plan:[],explanation:'',operations:[],context:{scope:'mutation',entries:[]}})
const files=(root:string):string[]=>readdirSync(root,{withFileTypes:true}).flatMap(entry=>entry.isDirectory()&&!['node_modules','dist','.git'].includes(entry.name)?files(join(root,entry.name)):entry.isFile()&&entry.name.endsWith('.ts')&&!entry.name.endsWith('.test.ts')?[join(root,entry.name)]:[])
function checkpointWrites(paths:string[],snapshot:Snapshot):string[]{
 const found:string[]=[]
 for(const path of paths){
  if(path.endsWith('/execution/mutation.ts'))continue
  const project=snapshot.getDefaultProjectForFile(path)??snapshot.getProjects()[0],file=project?.program?.getSourceFile(path),checker=project?.checker
  if(!file||!checker)continue
  const aggregate=(expression:ts.Expression):boolean=>{
   const type=checker.getTypeAtLocation(expression),member=type&&checker.getPropertyOfType(type,'requestKey')
   return member?.declarations.some(d=>d.resolve()?.getSourceFile().fileName.endsWith('/execution/state.ts'))??false
  }
  const rooted=(expression:ts.Expression):boolean=>{
   if(aggregate(expression))return true
   if(ts.isPropertyAccessExpression(expression)||ts.isElementAccessExpression(expression)||ts.isNonNullExpression(expression)||ts.isAsExpression(expression)||ts.isParenthesizedExpression(expression))return rooted(expression.expression)
   return false
  }
  const record=(expression:ts.Expression)=>{if(rooted(expression))found.push(`${path}:${file.getLineAndCharacterOfPosition(expression.getStart(file)).line+1}`)}
  const visit=(node:ts.Node)=>{
   if(ts.isBinaryExpression(node)&&node.operatorToken.kind>=ts.SyntaxKind.FirstAssignment&&node.operatorToken.kind<=ts.SyntaxKind.LastAssignment)record(node.left)
   if(ts.isDeleteExpression(node))record(node.expression)
   if((ts.isPrefixUnaryExpression(node)||ts.isPostfixUnaryExpression(node))&&(node.operator===ts.SyntaxKind.PlusPlusToken||node.operator===ts.SyntaxKind.MinusMinusToken))record(node.operand)
   if(ts.isCallExpression(node)&&ts.isPropertyAccessExpression(node.expression)){
    if(['push','pop','shift','unshift','splice','sort','reverse','fill','copyWithin','set','delete','clear'].includes(node.expression.name.text))record(node.expression.expression)
    if(node.expression.expression.getText(file)==='Object'&&['assign','defineProperty','defineProperties'].includes(node.expression.name.text)&&node.arguments[0])record(node.arguments[0])
   }
   node.forEachChild(visit)
  };visit(file)
 }
 return found
}
describe('canonical checkpoint mutation',()=>{
 it('commits one revision per changed patch and detaches reads, nested members and validation',()=>{
  const store=new CheckpointMutation(),input=seed()
  store.apply(input);expect(store.revision).toBe(1)
  input.context.scope='outside';expect(store.read().context.scope).toBe('mutation')
  store.apply({used:0});expect(store.revision).toBe(1)
  const steers={queued:[{text:'continue',bytes:8}],delivered:[]}
  store.apply({steers,used:1});expect(store.revision).toBe(2)
  steers.queued[0]!.text='changed';const read=store.read();read.steers!.queued.splice(0);read.context.scope='changed'
  expect(store.read().steers!.queued[0]!.text).toBe('continue')
  const nested=store.read().steers!;nested.queued[0]!.text='next';nested.queued[0]!.bytes=4
  store.apply({steers:nested});expect(store.revision).toBe(3)
  expect(()=>store.apply({used:2},candidate=>{candidate.context.scope='leaked';throw Error('invalid')})).toThrow('invalid')
  expect(store.read().context.scope).toBe('mutation')
  store.apply({context:{entries:[],scope:'mutation'}});expect(store.revision).toBe(3)
  for(const patch of [{unknownMember:1},{version:new CheckpointIncrement(1)},{used:Object.assign(new CheckpointIncrement(1),{amount:NaN})},{used:new CheckpointIncrement(Number.MAX_SAFE_INTEGER)}]){expect(()=>store.apply(patch as never)).toThrow();expect(store.revision).toBe(3)}
  expect(store.revision).toBe(3);expect(store.read().used).toBe(1);expect(store.read().context.scope).toBe('mutation')
  store.apply({reason:''});expect(Object.hasOwn(store.read(),'reason')).toBe(true)
  store.apply({reason:undefined});expect(Object.hasOwn(store.read(),'reason')).toBe(false)
 })
 it('keeps every production aggregate writer behind the API',()=>{
  const root=process.env.JP_CHECKPOINT_GUARD_ROOT?join(process.env.JP_CHECKPOINT_GUARD_ROOT,'packages/harness'):resolve(process.cwd())
  const paths=files(root),api=new API({cwd:process.cwd()}),snapshot=api.updateSnapshot({openFiles:paths})
  try{expect(checkpointWrites(paths,snapshot)).toEqual([])}finally{snapshot.dispose();api.close()}
 })
 it('catches a newly introduced writer, aliases, computed members and nested writes',()=>{
  const directory=mkdtempSync(join(tmpdir(),'checkpoint-guard-')),path=join(directory,'new-writer.ts')
  writeFileSync(path,`import type {ExecutionCheckpoint} from ${JSON.stringify(join(resolve(process.cwd()),'execution/state'))}
export function newWriter(state:ExecutionCheckpoint){
 state.reason='bypass'
 const alias=state;alias['testCalls']=1
 state.context.entries.push({} as never)
 state.approval!.state='answered'
 const unrelated={reason:''};unrelated.reason='allowed'
}`)
  const api=new API({cwd:process.cwd()}),snapshot=api.updateSnapshot({openFiles:[path]})
  try{expect(checkpointWrites([path],snapshot)).toHaveLength(4)}finally{snapshot.dispose();api.close()}
 })
})

import {ExecutionController} from './controller'
import {ContextLedger} from '../context/ledger'
import {CONTEXT_DEFAULTS} from '../context/policy'
import {canonicalJSON} from '../context/chat'
import {readExecution} from './state'
import {writeRecord,CHECKPOINT_SCHEMA,recordDigest} from '../records'
import type {EngineSession} from '../engine'

const contract=resolve(process.cwd(),'../../contract')
const memberSchema=JSON.parse(readFileSync(join(contract,'schemas/checkpoint.json'),'utf8'))
const memberVector=JSON.parse(readFileSync(join(contract,'vectors/checkpoint/members.json'),'utf8'))
const memberSeed=()=>structuredClone(memberVector.items[0]) as ExecutionCheckpoint&{handoff:unknown}
function inventory(schema:typeof memberSchema,owners:Record<string,string>,value:ExecutionCheckpoint){
 expect([Object.keys(owners).sort(),Object.keys(value).sort()]).toEqual([Object.keys(schema.properties).sort(),Object.keys(schema.properties).sort()])
 expect(new Set(Object.values(owners))).toEqual(new Set(['controller','envelope','delegation']))
}
const memberSession=(execution?:ExecutionCheckpoint):EngineSession=>({prompt:'checkpoint request',testPrompt:'',tools:[],hostTools:[],callTool:async()=>({content:[]}),signal:new AbortController().signal,model:{family:'gemini',model:'fixture',call:async()=>new Response('{}')},context:{execution}})
async function memberController(execution?:ExecutionCheckpoint){
 const session=memberSession(execution),ledger=new ContextLedger(CONTEXT_DEFAULTS),controller=new ExecutionController(session,ledger)
 await controller.initialize();return controller
}
describe('checkpoint member omission and interruption/resume',()=>{
 it('derives its coverage inventory from every top-level schema member',()=>{
  inventory(memberSchema,memberVector.probeOwners,memberSeed())
  expect([...CHECKPOINT_MEMBERS].sort()).toEqual(Object.keys(memberSchema.properties).sort())
  expect(()=>inventory({...memberSchema,properties:{...memberSchema.properties,newMember:{type:'string'}}},memberVector.probeOwners,memberSeed())).toThrow()
 })
 it('preserves every controller member through an interrupted snapshot and actual initialization',async()=>{
  const fixture=memberSeed(),first=await memberController(fixture)
  const interrupted=first.snapshot('stopped')
  const resumed=await memberController(JSON.parse(JSON.stringify(interrupted)))
  const continued=resumed.snapshot('running')
  for(const key of Object.keys(memberVector.probeOwners)){
   if(memberVector.probeOwners[key]!=='controller'||key==='status')continue
   expect(canonicalJSON((continued as any)[key]),key).toBe(canonicalJSON((fixture as any)[key]))
  }
  // Interruption and initialization deliberately project status; its running value resumes exactly.
  expect(interrupted.status).toBe('stopped');expect(continued.status).toBe(fixture.status)
  // TypeScript retains opaque coordinator state whole, including handoff.
  expect(canonicalJSON((continued as any).handoff)).toBe(canonicalJSON(fixture.handoff))
 })
 it('keeps optional omissions distinct from allowed empty/zero defaults through resume',async()=>{
  for(const key of Object.keys(memberSchema.properties).filter(key=>!memberSchema.required.includes(key)&&memberVector.probeOwners[key]==='controller')){
   const fixture=memberSeed();delete (fixture as any)[key]
   const resumed=await memberController(fixture)
   expect(Object.hasOwn(resumed.state,key),key).toBe(false)
  }
  const resumed=await memberController(memberSeed())
  expect(resumed.state.reason).toBe('');expect(resumed.state.testCalls).toBe(0)
  expect(Object.hasOwn(resumed.state.policy!['models.author.reasoning']!,'value')).toBe(false)
  for(const key of Object.keys(memberSchema.properties)){
   if(memberSchema.properties[key].type==='null')continue
   const fixture=memberSeed();(fixture as any)[key]=null
   // Open/opaque members are retained by the TS reader; only its existing closed fields refuse null.
   if(['handoff','crossProvider','schema','digest','retainUntil','records'].includes(key))continue
   expect(()=>readExecution(fixture),key).toThrow()
  }
 })
 it('uses the owning snapshot writer for envelope members across interruption/resume',async()=>{
  const first=await memberController(memberSeed())
  const interrupted=first.snapshot('stopped'),resumed=await memberController(interrupted),continued=resumed.snapshot('running')
  expect(continued.schema).toBe(CHECKPOINT_SCHEMA)
  expect(continued.retainUntil).toBe(memberSeed().retainUntil)
  expect(continued.records).toEqual({redactions:{}})
  expect(continued.digest).toBe(recordDigest(continued))
  const fixed=writeRecord(CHECKPOINT_SCHEMA,{...memberSeed(),status:'stopped'},{retainDays:1,redactOnStore:false,exportFormat:'json'},undefined,new Date('2026-10-01T00:00:00Z'),memberSeed().retainUntil)
  expect(fixed.retainUntil).toBe(memberSeed().retainUntil)
  expect(fixed.digest).toBe(recordDigest(fixed))
  const legacy=memberSeed();for(const key of ['schema','digest','retainUntil','records'])delete (legacy as any)[key]
  const restoredLegacy=await memberController(legacy)
  for(const key of ['schema','digest','retainUntil','records'])expect(Object.hasOwn(restoredLegacy.state,key),key).toBe(false)
  const renewed=restoredLegacy.snapshot('stopped');for(const key of ['schema','digest','retainUntil','records'])expect(Object.hasOwn(renewed,key),key).toBe(true)
  expect(renewed.digest).toBe(recordDigest(renewed))
 })
})

it('applies concurrent counter producers at the canonical boundary',async()=>{const state=new CheckpointMutation();state.apply(seed());await Promise.all(Array.from({length:100},async()=>{state.apply({used:new CheckpointIncrement(1)})}));expect(state.read().used).toBe(100);expect(state.revision).toBe(101)})

it('carries validated open members without admitting new unknown patch members',async()=>{const saved={...memberSeed(),opaque:{nested:[null,false,0]}};const controller=await memberController(saved);expect((controller.state as any).opaque).toEqual(saved.opaque);expect(()=>controller.mutate({newOpaque:1} as never)).toThrow(/Unknown checkpoint member/)})

it('detaches the validated initial checkpoint',()=>{const initial=seed(),store=new CheckpointMutation(initial);initial.context.scope='changed';expect(store.read().context.scope).toBe('mutation')})
