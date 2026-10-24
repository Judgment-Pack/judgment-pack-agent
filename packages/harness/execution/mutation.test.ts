import {describe,it,expect} from 'vitest'
import {join,resolve} from 'node:path'
import {readFileSync,readdirSync,mkdtempSync,mkdirSync,writeFileSync} from 'node:fs'
import {tmpdir} from 'node:os'
import {API,SymbolFlags,type Snapshot} from 'typescript/unstable/sync'
import * as ts from 'typescript/unstable/ast'
import {CHECKPOINT_MEMBERS,CheckpointIncrement,CheckpointMutation,mutateCheckpoint} from './mutation'
import type {ExecutionCheckpoint} from './state'

const seed=():ExecutionCheckpoint=>({version:1,id:'mutation',requestKey:'a'.repeat(64),phase:'work',status:'running',unit:'model-requests',used:0,limit:20,grants:[],plan:[],explanation:'',operations:[],context:{scope:'mutation',entries:[]}})
const files=(root:string):string[]=>readdirSync(root,{withFileTypes:true}).flatMap(entry=>entry.isDirectory()&&!['node_modules','dist','.git'].includes(entry.name)?files(join(root,entry.name)):entry.isFile()&&entry.name.endsWith('.ts')&&!entry.name.endsWith('.test.ts')?[join(root,entry.name)]:[])
function checkpointWrites(paths:string[],snapshot:Snapshot,root=resolve(process.cwd())):string[]{
 const found:string[]=[]
 for(const path of paths){
  if(path===join(root,'execution/mutation.ts'))continue
  const project=snapshot.getDefaultProjectForFile(path)??snapshot.getProjects()[0],file=project?.program?.getSourceFile(path),checker=project?.checker
  if(!file||!checker)continue
  const aggregate=(expression:ts.Expression):boolean=>{
   const type=checker.getTypeAtLocation(expression),member=type&&checker.getPropertyOfType(type,'requestKey')
   return member?.declarations.some(d=>d.resolve()?.getSourceFile().fileName.endsWith('/execution/state.ts'))??false
  }
  // A detached book is published only by its originating controller/aggregate.
  // Local receiver aliases retain identity; equal member types do not identify an owner.
  type Origin={member:string,book:unknown,owner:string|undefined}
  const aliases=new Map<unknown,Origin>(),scopes=new Map<ts.Node,ts.Node>()
  const receiverAliases=new Map<unknown,string>(),aggregateOwners=new Map<unknown,string>(),parameterProperties=new Map<number,string>()
  const symbolIDs=new Map<unknown,number>(),reassigned=new Set<unknown>()
  const symbolKey=(symbol:unknown):string|undefined=>{if(!symbol)return undefined;if(!symbolIDs.has(symbol))symbolIDs.set(symbol,symbolIDs.size+1);return String(symbolIDs.get(symbol))}
  const findReassignments=(node:ts.Node)=>{
   if(ts.isBinaryExpression(node)&&node.operatorToken.kind>=ts.SyntaxKind.FirstAssignment&&node.operatorToken.kind<=ts.SyntaxKind.LastAssignment&&ts.isIdentifier(node.left))reassigned.add(checker.getSymbolAtLocation(node.left))
   node.forEachChild(findReassignments)
  };findReassignments(file)
  const instances=new Map<ts.Node,string>()
  const markInstances=(node:ts.Node,instance:string|undefined=undefined)=>{
   if(ts.isClassDeclaration(node)||ts.isClassExpression(node)){const key=symbolKey(node.name?checker.getSymbolAtLocation(node.name):checker.getTypeAtLocation(node)?.getSymbol());instance=key?'this:'+key:undefined}
   if(instance)instances.set(node,instance)
   node.forEachChild(child=>markInstances(child,instance))
  };markInstances(file)
  const identity=(expression:ts.Expression):string|undefined=>{
   if(ts.isIdentifier(expression)){
    const symbol=checker.getSymbolAtLocation(expression)
    if(reassigned.has(symbol))return undefined
    const parameter=symbol?.declarations.map(d=>d.resolve()).find(d=>d&&ts.isParameterDeclaration(d))
    return receiverAliases.get(symbol)??(parameter&&parameterProperties.get(parameter.getStart(file)))??symbolKey(symbol)
   }
   if(ts.isThisExpression(expression))return instances.get(expression)
   if(ts.isNonNullExpression(expression)||ts.isAsExpression(expression)||ts.isParenthesizedExpression(expression))return identity(expression.expression)
   if(ts.isPropertyAccessExpression(expression)){const root=identity(expression.expression);return root&&root+'.'+expression.name.text}
   if(ts.isElementAccessExpression(expression)&&expression.argumentExpression&&ts.isStringLiteral(expression.argumentExpression)){const root=identity(expression.expression);return root&&root+'.'+expression.argumentExpression.text}
   return undefined
  }
  const aggregateOwner=(expression:ts.Expression):string|undefined=>{
   if(ts.isNonNullExpression(expression)||ts.isAsExpression(expression)||ts.isParenthesizedExpression(expression))return aggregateOwner(expression.expression)
   if(ts.isIdentifier(expression)){const symbol=checker.getSymbolAtLocation(expression);return reassigned.has(symbol)?undefined:aggregateOwners.get(symbol)??identity(expression)}
   if(ts.isPropertyAccessExpression(expression)&&expression.name.text==='state'){
    const member=checker.getSymbolAtLocation(expression.name)
    if(member?.declarations.some(d=>d.resolve()?.getSourceFile().fileName===join(root,'execution/controller.ts')))return identity(expression.expression)
   }
   if(ts.isCallExpression(expression)&&ts.isPropertyAccessExpression(expression.expression)&&['read','state'].includes(expression.expression.name.text))return identity(expression.expression.expression)
   return identity(expression)
  }
  const origin=(expression:ts.Expression):Origin|undefined=>{
   if(ts.isIdentifier(expression))return aliases.get(checker.getSymbolAtLocation(expression))
   if(ts.isPropertyAccessExpression(expression)||ts.isElementAccessExpression(expression)){
    if(aggregate(expression.expression))return {member:ts.isPropertyAccessExpression(expression)?expression.name.text:expression.argumentExpression?.getText(file).replace(/^['"]|['"]$/g,'')??'',book:undefined,owner:aggregateOwner(expression.expression)}
    return origin(expression.expression)
   }
   if(ts.isNonNullExpression(expression)||ts.isAsExpression(expression)||ts.isParenthesizedExpression(expression))return origin(expression.expression)
   if(ts.isBinaryExpression(expression)&&expression.operatorToken.kind===ts.SyntaxKind.QuestionQuestionToken)return origin(expression.left)??origin(expression.right)
   if(ts.isCallExpression(expression)&&ts.isPropertyAccessExpression(expression.expression))return origin(expression.expression.expression)
   return undefined
  }
  const bindParameter=(name:ts.BindingName,held:Origin)=>{
   if(ts.isIdentifier(name)){const symbol=checker.getSymbolAtLocation(name);if(symbol)aliases.set(symbol,held)}
   else for(const element of name.elements)if(ts.isBindingElement(element)&&element.name)bindParameter(element.name,held)
  }
  const collect=(node:ts.Node,scope:ts.Node,immediate=false,instance:string|undefined=undefined)=>{
   if(ts.isClassDeclaration(node)||ts.isClassExpression(node)){const key=node.name&&symbolKey(checker.getSymbolAtLocation(node.name));instance=key?'this:'+key:undefined}
   if(ts.isFunctionLikeDeclaration(node)&&!immediate)scope=node
   scopes.set(node,scope)
   // Parameter properties alias this.run in SkillGate's constructor; other instances
   // retain their own receiver identity when invoking the exact source-book publisher.
   if(ts.isConstructorDeclaration(node)&&instance)for(const parameter of node.parameters){
    if(ts.isIdentifier(parameter.name)&&parameter.modifiers?.some(modifier=>[ts.SyntaxKind.PrivateKeyword,ts.SyntaxKind.PublicKeyword,ts.SyntaxKind.ProtectedKeyword,ts.SyntaxKind.ReadonlyKeyword].includes(modifier.kind))){
     const symbol=checker.getSymbolAtLocation(parameter.name),owner=instance+'.'+parameter.name.text;if(symbol)receiverAliases.set(symbol,owner);parameterProperties.set(parameter.getStart(file),owner)
    }
   }
   if(ts.isVariableDeclaration(node)&&ts.isIdentifier(node.name)&&node.initializer){
    const held=origin(node.initializer),symbol=checker.getSymbolAtLocation(node.name),receiver=identity(node.initializer)
    if(symbol&&receiver)receiverAliases.set(symbol,receiver)
    if(symbol&&aggregate(node.initializer)){const owner=aggregateOwner(node.initializer);if(owner)aggregateOwners.set(symbol,owner)}
    if(held&&symbol)aliases.set(symbol,{...held,book:held.book??symbol})
   }
   let callback:ts.Expression|undefined
   if(ts.isCallExpression(node)&&ts.isPropertyAccessExpression(node.expression)&&checker.isArrayLikeType(checker.getTypeAtLocation(node.expression.expression)!)&&['map','forEach','filter','reduce'].includes(node.expression.name.text)){
    const held=origin(node.expression.expression),candidate=node.arguments[0]
    if(candidate&&(ts.isArrowFunction(candidate)||ts.isFunctionExpression(candidate))){
     if(!candidate.modifiers?.some(modifier=>modifier.kind===ts.SyntaxKind.AsyncKeyword))callback=candidate
     if(held){
      const collection={...held,book:held.book??candidate},reduce=node.expression.name.text==='reduce'
      const element=candidate.parameters[reduce?1:0],array=candidate.parameters[reduce?3:2]
      if(element)bindParameter(element.name,collection)
      if(array)bindParameter(array.name,collection)
      if(reduce&&candidate.parameters[0]){const initial=node.arguments[1],accumulator=initial?origin(initial):collection;if(accumulator)bindParameter(candidate.parameters[0].name,accumulator)}
     }
    }
   }
   node.forEachChild(child=>collect(child,scope,child===callback,instance))
  };collect(file,file)
  const publications:{scope:ts.Node|undefined,member:string,book:unknown,owner:string|undefined,position:number}[]=[]
  const publish=(node:ts.Node)=>{
   if(ts.isCallExpression(node)){
    const selected=checker.getSymbolAtLocation(node.expression)
    const symbol=selected&&(selected.flags&SymbolFlags.Alias?checker.getAliasedSymbol(selected):selected)
    const name=ts.isPropertyAccessExpression(node.expression)?node.expression.name.text:symbol?.name??node.expression.getText(file)
    const patch=node.arguments[name==='mutateCheckpoint'?1:0]
    const owner=symbol?.declarations.some(d=>{const source=d.resolve()?.getSourceFile().fileName;return source===join(root,'execution/mutation.ts')||source===join(root,'execution/controller.ts')||(name==='commitSources'&&source===join(root,'skills.ts'))})
    const receiver=ts.isPropertyAccessExpression(node.expression)?identity(node.expression.expression):undefined
    const publishingOwner=name==='mutateCheckpoint'&&node.arguments[0]?identity(node.arguments[0]):name==='commitSources'&&receiver?receiver+'.run':receiver
    if(owner&&name==='commitSources'&&patch){const held=origin(patch);if(held?.book&&held.member==='sources')publications.push({scope:scopes.get(node),member:'sources',book:held.book,owner:publishingOwner,position:node.getStart(file)})}
    if(owner&&['mutate','apply','mutateCheckpoint'].includes(name)&&patch&&ts.isObjectLiteralExpression(patch))for(const property of patch.properties){
     if(!ts.isShorthandPropertyAssignment(property)&&!ts.isPropertyAssignment(property))continue
     const held=ts.isShorthandPropertyAssignment(property)?aliases.get(checker.getShorthandAssignmentValueSymbol(property)):origin(property.initializer)
     const member=property.name.getText(file).replace(/^['"]|['"]$/g,'')
     if(held?.book)publications.push({scope:scopes.get(node),member,book:held.book,owner:publishingOwner,position:node.getStart(file)})
    }
   }
   node.forEachChild(publish)
  };publish(file)
  const rooted=(expression:ts.Expression):boolean=>{
   if(aggregate(expression))return true
   if(ts.isPropertyAccessExpression(expression)||ts.isElementAccessExpression(expression)||ts.isNonNullExpression(expression)||ts.isAsExpression(expression)||ts.isParenthesizedExpression(expression))return rooted(expression.expression)
   return false
  }
  const record=(expression:ts.Expression)=>{
   const held=origin(expression)
   const committed=held?.book&&held.owner!==undefined&&publications.some(p=>p.scope===scopes.get(expression)&&p.member===held.member&&p.book===held.book&&p.owner===held.owner&&p.position>expression.getStart(file))
   if(rooted(expression)||(held&&!committed))found.push(`${path}:${file.getLineAndCharacterOfPosition(expression.getStart(file)).line+1}`)
  }
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
 it('isolates a successful validator from the candidate that is committed',()=>{
  const store=new CheckpointMutation(seed())
  store.apply({used:2},candidate=>{candidate.used=9})
  expect(store.read().used).toBe(2)
  expect(store.revision).toBe(2)
 })
 it('keeps every production aggregate writer behind the API',()=>{
  const root=process.env.JP_CHECKPOINT_GUARD_ROOT?join(process.env.JP_CHECKPOINT_GUARD_ROOT,'packages/harness'):resolve(process.cwd())
  const paths=files(root),api=new API({cwd:process.cwd()}),snapshot=api.updateSnapshot({openFiles:paths})
  try{expect(checkpointWrites(paths,snapshot,root)).toEqual([])}finally{snapshot.dispose();api.close()}
 })
 it('catches a newly introduced writer, aliases, computed members and nested writes',()=>{
  const directory=mkdtempSync(join(tmpdir(),'checkpoint-guard-')),path=join(directory,'new-writer.ts')
  writeFileSync(path,`import type {ExecutionCheckpoint} from ${JSON.stringify(join(resolve(process.cwd()),'execution/state'))}
export function newWriter(state:ExecutionCheckpoint){
 state.reason='bypass'
 const alias=state;alias['testCalls']=1
 state.context.entries.push({} as never)
 state.approval!.state='answered'
 const approval=state.approval!;approval.state='answered'
 const context=state.context;context.scope='lost'
 const operations=state.operations;operations.push({} as never)
 const unrelated={reason:''};unrelated.reason='allowed'
}`)
  const api=new API({cwd:process.cwd()}),snapshot=api.updateSnapshot({openFiles:[path]})
  try{expect(checkpointWrites([path],snapshot)).toHaveLength(7)}finally{snapshot.dispose();api.close()}
 })
 it('allows a member copy only when its own book is explicitly published under that member',()=>{
  const directory=mkdtempSync(join(tmpdir(),'checkpoint-publication-')),path=join(directory,'execution','mutation.ts')
  mkdirSync(join(directory,'execution'))
  writeFileSync(path,`import {ExecutionController} from ${JSON.stringify(join(resolve(process.cwd()),'execution/controller'))}
export function committed(c:ExecutionController){const approval=c.state.approval!;approval.state='answered';c.mutate({approval})}
export function wrongMember(c:ExecutionController){const approval=c.state.approval!;approval.state='answered';c.mutate({critic:approval as never})}
export function otherBook(c:ExecutionController){const approval=c.state.approval!;approval.state='answered';const other=c.state.approval;c.mutate({approval:other})}
export function otherScope(c:ExecutionController){const approval=c.state.approval!;approval.state='answered';const later=()=>c.mutate({approval});void later}
export function tooEarly(c:ExecutionController){const approval=c.state.approval!;c.mutate({approval});approval.state='answered'}
export function committedArray(c:ExecutionController){const operations=c.state.operations;operations.forEach(operation=>{operation.status='complete'});c.mutate({operations})}
`)
  const api=new API({cwd:process.cwd()}),snapshot=api.updateSnapshot({openFiles:[path]})
  try{expect(checkpointWrites([path],snapshot)).toEqual([3,4,5,6].map(line=>`${path}:${line}`))}finally{snapshot.dispose();api.close()}
 })

 it('requires publication by the originating controller or raw aggregate',()=>{
  const directory=mkdtempSync(join(tmpdir(),'checkpoint-owner-')),path=join(directory,'owner.ts')
  writeFileSync(path,`import {ExecutionController} from ${JSON.stringify(join(resolve(process.cwd()),'execution/controller'))}
import {CheckpointMutation,mutateCheckpoint} from ${JSON.stringify(join(resolve(process.cwd()),'execution/mutation'))}
import type {ExecutionCheckpoint} from ${JSON.stringify(join(resolve(process.cwd()),'execution/state'))}
export function wrongOwner(c:ExecutionController,d:ExecutionController){const approval=c.state.approval!;approval.state='answered';d.mutate({approval})}
export function receiverAlias(c:ExecutionController){const publisher=c;const state=c.state;const view=state;const approval=view.approval!;approval.state='answered';publisher.mutate({approval})}
export function wrongAlias(c:ExecutionController,d:ExecutionController){const publisher=d;const approval=c.state.approval!;approval.state='answered';publisher.mutate({approval})}
export function rawOwner(state:ExecutionCheckpoint){const approval=state.approval!;approval.state='answered';mutateCheckpoint(state,{approval})}
export function wrongRawOwner(state:ExecutionCheckpoint,other:ExecutionCheckpoint){const approval=state.approval!;approval.state='answered';mutateCheckpoint(other,{approval})}
export function storeAlias(c:CheckpointMutation){const publisher=c;const state=c.read();const approval=state.approval!;approval.state='answered';publisher.apply({approval})}
export function wrongStore(c:CheckpointMutation,d:CheckpointMutation){const approval=c.read().approval!;approval.state='answered';d.apply({approval})}
export function fieldOwner(pair:{first:ExecutionController;second:ExecutionController}){const publisher=pair.first;const approval=publisher.state.approval!;approval.state='answered';pair.first.mutate({approval})}
export function wrongFieldOwner(pair:{first:ExecutionController;second:ExecutionController}){const approval=pair.first.state.approval!;approval.state='answered';pair.second.mutate({approval})}
export function reassignedReceiver(c:ExecutionController,d:ExecutionController){let publisher=c;const approval=c.state.approval!;approval.state='answered';publisher=d;publisher.mutate({approval})}
export function reassignedAggregate(c:ExecutionController,d:ExecutionController){let state=c.state;state=d.state;const approval=state.approval!;approval.state='answered';c.mutate({approval})}
`)
  const api=new API({cwd:process.cwd()}),snapshot=api.updateSnapshot({openFiles:[path]})
  // The reassigned aggregate itself and its later unpublished member edit are separate sites.
  try{expect(checkpointWrites([path],snapshot)).toEqual([4,6,8,10,12,13,14,14].map(line=>`${path}:${line}`))}finally{snapshot.dispose();api.close()}
 })
 it('requires owning publication for synchronous collection callback parameters',()=>{
  const directory=mkdtempSync(join(tmpdir(),'checkpoint-callback-')),path=join(directory,'callback.ts')
  writeFileSync(path,`import {ExecutionController} from ${JSON.stringify(join(resolve(process.cwd()),'execution/controller'))}
export function unpublished(c:ExecutionController){const operations=c.state.operations;operations.forEach(operation=>{operation.status='complete'})}
export function wrongOwner(c:ExecutionController,d:ExecutionController){const operations=c.state.operations;operations.forEach(operation=>{operation.status='complete'});d.mutate({operations})}
export function committed(c:ExecutionController){const publisher=c;const operations=c.state.operations;const alias=operations;alias.forEach(operation=>{const held=operation;held.status='complete'});publisher.mutate({operations})}
export function unpublishedMap(c:ExecutionController){const operations=c.state.operations;operations.map(operation=>{operation.status='complete';return operation})}
export function unpublishedFilter(c:ExecutionController){const operations=c.state.operations;operations.filter(operation=>{operation.status='complete';return true})}
export function unpublishedReduce(c:ExecutionController){const operations=c.state.operations;operations.reduce((count,operation)=>{operation.status='complete';return count+1},0)}
export function committedReduce(c:ExecutionController){const operations=c.state.operations;operations.reduce((count,operation)=>{operation.status='complete';return count+1},0);c.mutate({operations})}
export function arrayParameter(c:ExecutionController){const operations=c.state.operations;operations.forEach((_operation,_index,rows)=>{rows[0]!.status='complete'})}
export function directGetter(c:ExecutionController){c.state.operations.forEach(operation=>{operation.status='complete'})}
export function unrelated(){const operations=[{status:'running'}];operations.forEach(operation=>{operation.status='complete'})}
export function asyncCallback(c:ExecutionController){const operations=c.state.operations;operations.forEach(async operation=>{await Promise.resolve();operation.status='complete'});c.mutate({operations})}
`)
  const api=new API({cwd:process.cwd()}),snapshot=api.updateSnapshot({openFiles:[path]})
  try{expect(checkpointWrites([path],snapshot)).toEqual([2,3,5,6,7,9,10,12].map(line=>`${path}:${line}`))}finally{snapshot.dispose();api.close()}
 })

})

import {ExecutionController} from './controller'
import {ContextLedger} from '../context/ledger'
import {CONTEXT_DEFAULTS} from '../context/policy'
import {canonicalJSON} from '../context/chat'
import {readExecution} from './state'
import {UnknownRecordMember} from '../recordReader'
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
const memberSession=(execution?:ExecutionCheckpoint):EngineSession=>({prompt:'checkpoint request',testPrompt:'',tools:[],hostTools:[],callTool:async()=>({content:[]}),signal:new AbortController().signal,model:{family:'gemini',model:'fixture',call:async()=>new Response('{}')},context:{execution,...(execution?.requestCap===undefined?{}:{requestCap:execution.requestCap})}})
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
   if(key==='version'){expect(fixture.version).toBe(2);expect(interrupted.version).toBe(2);expect(continued.version).toBe(2);continue}
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

it('refuses unknown structural members on read and on patch',async()=>{const saved={...memberSeed(),opaque:{nested:[null,false,0]}};await expect(memberController(saved)).rejects.toThrow(UnknownRecordMember);const controller=await memberController(memberSeed());expect(()=>controller.mutate({newOpaque:1} as never)).toThrow(/Unknown checkpoint member/)})

it('detaches the validated initial checkpoint',()=>{const initial=seed(),store=new CheckpointMutation(initial);initial.context.scope='changed';expect(store.read().context.scope).toBe('mutation')})

describe('declared arbitrary checkpoint payloads',()=>{
 it('preserves an own __proto__ member as serialized data through unrelated patches',()=>{
  const fixture=memberSeed()
  delete fixture.policy;delete fixture.completions
  const args:Record<string,unknown>={}
  Object.defineProperty(args,'__proto__',{value:{probe:'opaque'},enumerable:true,writable:true,configurable:true})
  fixture.operations=[{id:'opaque-call',name:'read',args,status:'complete',result:{content:[]}}]
  const accepted=readExecution(JSON.parse(JSON.stringify(fixture)))!
  expect(Object.hasOwn(accepted.operations[0]!.args as object,'__proto__')).toBe(true)
  const store=new CheckpointMutation(accepted),target=structuredClone(accepted)
  const prototype=Object.getPrototypeOf(target.operations[0]!.args)
  store.apply({explanation:'unrelated'})
  mutateCheckpoint(target,{explanation:'unrelated'})
  for(const value of [store.read(),target]){
   const payload=value.operations[0]!.args as Record<string,unknown>
   expect(Object.hasOwn(payload,'__proto__')).toBe(true)
   expect(JSON.parse(JSON.stringify(payload))['__proto__']).toEqual({probe:'opaque'})
   expect(Object.getPrototypeOf(payload)).toBe(prototype)
   expect(payload.probe).toBeUndefined()
  }
  expect(store.revision).toBe(2)
 })
})
