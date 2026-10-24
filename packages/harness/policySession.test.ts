import {describe,it,expect,vi} from 'vitest'
import {join,resolve} from 'node:path'
import {tmpdir} from 'node:os'
import {mkdtempSync,mkdirSync,readdirSync,rmSync,writeFileSync} from 'node:fs'
import {API,type Snapshot,type Type} from 'typescript/unstable/sync'
import * as ts from 'typescript/unstable/ast'
import {contextualSession,requestSession} from './context/session'
import {codex} from './engines/codex'
import {withDelegation} from './delegation'
import type {AgentRun} from './agent'
import type {AgentAssistantSession,AssistantEvent,EngineSession,RequestSession} from './engine'
import type {PolicyMembers,ReasoningValue} from './policy'
import collisions from '../../contract/vectors/policy/legacy-collisions.json'
import requestValuesVector from '../../contract/vectors/policy/request-session.json'
import {RequestValues} from './context/requestValues'
import {CAPABILITY_MEMBERS} from './capabilities'
import {configureHarness,endpointCapabilities} from './host'
import omission from '../../contract/vectors/providers/reasoning-omission.json'

const base=():AgentAssistantSession=>({prompt:'Draft a pack.',testPrompt:'Review the pack.',tools:[],hostTools:[],callTool:vi.fn(),signal:new AbortController().signal,allowConversation:true,agent:{model:'connected',run:vi.fn()}})

function requestBoundaryTypeCases(session:RequestSession){
 // @ts-expect-error RequestSession has no raw direct effort.
 void session.effort
 const alias=session
 // @ts-expect-error An alias cannot restore raw effort.
 void alias.effort
 // @ts-expect-error Destructuring cannot restore raw effort.
 const {effort}=session
 void effort
 const key='effort' as const
 // @ts-expect-error A computed key cannot restore raw effort.
 void session[key]
 const spread={...session}
 // @ts-expect-error A spread cannot restore raw effort.
 void spread.effort
 const rawHelper=(_raw:EngineSession)=>undefined
 // @ts-expect-error A request boundary cannot be passed to a raw-session helper.
 rawHelper(session)
}
void requestBoundaryTypeCases

const sourceFiles=(root:string):string[]=>readdirSync(root,{withFileTypes:true}).flatMap(entry=>entry.isDirectory()?sourceFiles(join(root,entry.name)):entry.name.endsWith('.ts')&&!entry.name.endsWith('.test.ts')?[join(root,entry.name)]:[])
function rawReasoningReads(harness:string,files:string[],snapshot:Snapshot):string[]{
 const unexpected:string[]=[]
 for(const path of files){
  const project=snapshot.getDefaultProjectForFile(path)??snapshot.getProjects()[0],program=project?.program,checker=project?.checker,file=program?.getSourceFile(path)
  if(!file||!checker)continue
  const rawMember=(expression:ts.Expression,name:string):boolean=>{
   const type=checker.getTypeAtLocation(expression),symbol=type&&checker.getPropertyOfType(type,name)
   if(symbol?.declarations.some(declaration=>{
    let owner:ts.Node|undefined=declaration.resolve()
    while(owner){
     if((ts.isInterfaceDeclaration(owner)||ts.isTypeAliasDeclaration(owner))&&['AssistantSession','AgentAssistantSession','ContextInput'].includes(owner.name.text))return true
     owner=owner.parent
    }
    return false
   }))return true
   if(ts.isObjectLiteralExpression(expression))return expression.properties.some(property=>ts.isSpreadAssignment(property)&&rawMember(property.expression,name))
   if(ts.isParenthesizedExpression(expression)||ts.isAsExpression(expression)||ts.isNonNullExpression(expression))return rawMember(expression.expression,name)
   return false
  }
  // The checker retains literal keys through annotations, unions and const aliases.
  const literalKeys=(type:Type|undefined):string[]=>type?.isStringLiteralType()?[type.value]:type?.isUnionType()?type.getTypes().flatMap(literalKeys):[]
  const enclosingFunction=(node:ts.Node):string=>{
   let parent:ts.Node|undefined=node
   while(parent){
    if(ts.isFunctionDeclaration(parent)&&parent.name)return parent.parent===file?parent.name.text:''
    if(ts.isConstructorDeclaration(parent)){const owner=parent.parent;return ts.isClassDeclaration(owner)&&owner.parent===file?(owner.name?.text??'')+'.constructor':''}
    if(ts.isMethodDeclaration(parent)&&ts.isIdentifier(parent.name)){
     const owner=parent.parent
     if(ts.isClassDeclaration(owner))return owner.parent===file?owner.name?.text+'.'+parent.name.text:''
     // Only the returned engine's start method belongs to these top-level factories.
     if(!ts.isObjectLiteralExpression(owner))return ''
     let returned:ts.Node=owner
     while(returned.parent&&(ts.isAsExpression(returned.parent)||ts.isParenthesizedExpression(returned.parent)))returned=returned.parent
     const statement=returned.parent
     if(!statement||!ts.isReturnStatement(statement))return ''
     const body=statement.parent,factory=body?.parent
     return body&&ts.isBlock(body)&&factory&&ts.isFunctionDeclaration(factory)&&factory.parent===file&&factory.name?factory.name.text+'.'+parent.name.text:''
    }
    parent=parent.parent
   }
   return ''
  }
  const boundaryType=(node:ts.Node,owners=['RequestBoundary','RequestValueView']):boolean=>{
   const type=checker.getTypeAtLocation(node)
   return !!type&&checker.getPropertiesOfType(type).some(symbol=>symbol.declarations.some(reference=>{
    let owner:ts.Node|undefined=reference.resolve()
    while(owner){if((ts.isTypeAliasDeclaration(owner)||ts.isInterfaceDeclaration(owner)||ts.isClassDeclaration(owner))&&owner.name&&owners.includes(owner.name.text))return true;owner=owner.parent}return false
   }))
  }
  const rawOperational=(base:ts.Expression,name:string):boolean=>{
   const operational:Record<string,string[]>={
    AssistantSession:['input','record','corpus','prompt','testPrompt','purpose','replyLanguage','allowConversation','interactive','callTool','signal','context'],
    ContextInput:['currentRequest','currentState','phase','objective','artifactProposals','authorize','checkpoint','execution','approval','chat','runtime','memory','documents','acceptedDocuments','plan','link','delegationReuse']
   }
   const type=checker.getTypeAtLocation(base),symbol=type&&checker.getPropertyOfType(type,name)
   return symbol?.declarations.every(reference=>{
    const owner=reference.resolve()?.parent
    // Only a direct operational declaration is exempt; nested value fields stay protected.
    if(owner&&(ts.isInterfaceDeclaration(owner)||ts.isTypeAliasDeclaration(owner))&&['AssistantSession','AgentAssistantSession','ContextInput'].includes(owner.name.text))return operational[owner.name.text]?.includes(name)??false
    return false
   })??false
  }
  const record=(node:ts.Node,name:string,base:ts.Expression):void=>{
   if(!rawMember(base,name)||rawOperational(base,name))return
   const fn=enclosingFunction(node)
   const relative=path.slice(harness.length+1)
   const admission:Record<string,string[]>={
    'context/session.ts':['resolveSessionPolicy','requestSession','contextualSession','delegatedRoleSession','delegatedProjection','SessionContext.constructor','SessionContext.askHost','SessionContext.connection','SessionContext.models','SessionContext.fallback','SessionContext.family','fallbackRequest'],
    'corpus.ts':['recordSession'],
    'delegation.ts':['withDelegation.start'],
    'execution/controller.ts':['ExecutionController.constructor','ExecutionController.initialize','ExecutionController.useConnection'],
    'planning/policy.ts':['planningSession'],
    'testing/afterPolicy.ts':['afterPolicy.start']
   }
   const allowed=admission[relative]?.includes(fn)&&(!['effort','thinking','criticReasoning'].includes(name)||relative==='context/session.ts'&&['resolveSessionPolicy','requestSession'].includes(fn))
   if(!allowed)unexpected.push(`${relative}:${file.getLineAndCharacterOfPosition(node.getStart(file)).line+1}:${name}`)
  }
  const visit=(node:ts.Node):void=>{
   if(ts.isAsExpression(node)||ts.isNewExpression(node)){
    // A narrowing cast of an already branded view is not another conversion.
    const boundary=boundaryType(node)&&!(ts.isAsExpression(node)&&boundaryType(node.expression))
    const values=ts.isNewExpression(node)&&boundaryType(node,['RequestValues'])
    const relative=path.slice(harness.length+1),fn=enclosingFunction(node)
    const allowed=relative==='context/session.ts'&&(boundary?fn==='requestSession':values&&['requestSession','SessionContext.constructor','contextualSession'].includes(fn))
    if((boundary||values)&&!allowed)unexpected.push(`${relative}:${file.getLineAndCharacterOfPosition(node.getStart(file)).line+1}:request-construction`)
   }
   if(ts.isPropertyAccessExpression(node))record(node,node.name.text,node.expression)
   else if(ts.isElementAccessExpression(node)){
    for(const name of literalKeys(checker.getTypeAtLocation(node.argumentExpression)))record(node,name,node.expression)
   }else if(ts.isBindingElement(node)&&ts.isObjectBindingPattern(node.parent)){
    const named=node.propertyName??node.name,declaration=node.parent.parent,initializer=ts.isVariableDeclaration(declaration)?declaration.initializer:undefined
    if(initializer&&named){
     const names=ts.isComputedPropertyName(named)?literalKeys(checker.getTypeAtLocation(named.expression)):(ts.isIdentifier(named)||ts.isStringLiteralLikeNode(named))?[named.text]:[]
     for(const name of names)record(node,name,initializer)
    }
   }
   node.forEachChild(visit)
  }
  visit(file)
 }
 return unexpected
}
describe('ADR policy table review',()=>{
 it('keeps raw session reasoning reads inside resolution and conversion',()=>{
  const root=process.env.JP_REASONING_GUARD_ROOT??process.cwd()
	 const harness=process.env.JP_REASONING_GUARD_ROOT?join(root,'packages','harness'):root
	 const files=sourceFiles(harness)
	 const api=new API({cwd:process.cwd()})
	 const snapshot=process.env.JP_REASONING_GUARD_ROOT?api.updateSnapshot({openFiles:files}):api.updateSnapshot({openProjects:[join(process.cwd(),'tsconfig.json')]})
	 try{
   expect(rawReasoningReads(harness,files,snapshot)).toEqual([])
  }finally{snapshot.dispose();api.close()}
 })
 it('catches typed raw reasoning keys while allowing resolved and unrelated members',()=>{
  const harness=resolve(process.cwd()),directory=mkdtempSync(join(tmpdir(),'reasoning-guard-')),path=join(directory,'probe.ts')
  const source=`import type {AgentAssistantSession,AssistantSession} from ${JSON.stringify(join(harness,'engine'))}
export function probe(raw:AgentAssistantSession,assistant:AssistantSession,key:'thinking',union:'effort'|'criticReasoning',mixed:'effort'|'prompt'){
 const constant='effort' as const
 const alias=constant
 void raw[constant]
 void raw[alias]
 void assistant[key]
 void raw[union]
 void raw[mixed]
 void assistant['thinking']
 const resolved:{effort?:string;thinking?:string;criticReasoning?:string}={}
 void resolved[constant]
 void resolved[key]
 void resolved[union]
 void {effort:'high'}[alias]
 void raw.prompt
 const {[alias]:effort}=raw
 void effort
 const {[alias]:resolvedEffort}=resolved
 void resolvedEffort
}`
  writeFileSync(path,source)
  writeFileSync(join(directory,'tsconfig.json'),JSON.stringify({compilerOptions:{strict:true,skipLibCheck:true,target:'ES2022',module:'ESNext',moduleResolution:'bundler'},files:['probe.ts']}))
  const api=new API({cwd:directory}),snapshot=api.updateSnapshot({openProjects:[join(directory,'tsconfig.json')]})
  try{
   const project=snapshot.getProjects()[0]
   expect(project).toBeDefined()
   expect(project!.program.getSyntacticDiagnostics(path)).toEqual([])
   expect(project!.program.getSemanticDiagnostics(path)).toEqual([])
   expect(rawReasoningReads(directory,[path],snapshot).sort()).toEqual([
    'probe.ts:5:effort','probe.ts:6:effort','probe.ts:7:thinking',
    'probe.ts:8:criticReasoning','probe.ts:8:effort','probe.ts:9:effort','probe.ts:10:thinking','probe.ts:17:effort'
   ].sort())
  }finally{snapshot.dispose();api.close();rmSync(directory,{recursive:true,force:true})}
 })
 it('binds conversion exemptions to canonical declarations',()=>{
  const harness=resolve(process.cwd()),directory=mkdtempSync(join(tmpdir(),'request-owner-')),context=join(directory,'context'),path=join(context,'session.ts')
  mkdirSync(context)
  writeFileSync(path,`import type {EngineSession,RequestSession} from ${JSON.stringify(join(harness,'engine'))}
import {RequestValues} from ${JSON.stringify(join(harness,'context/requestValues'))}
export function requestSession(raw:EngineSession) {
 void ('model' in raw ? raw.model.model : raw.agent.model)
 new RequestValues({})
 return {} as RequestSession
}
export function unrelatedReviewWriter(raw:EngineSession) {
 function requestSession() {
  void ('model' in raw ? raw.model.model : raw.agent.model)
  new RequestValues({})
  return {} as RequestSession
 }
 return requestSession()
}
export class SessionContext { constructor() { new RequestValues({}) } models(raw:EngineSession) { return 'model' in raw?raw.model.model:raw.agent.model } }
export function unrelatedClass() {
 class SessionContext { constructor() { new RequestValues({}) } models(raw:EngineSession) { return 'model' in raw?raw.model.model:raw.agent.model } }
 return new SessionContext()
}
export function unrelated(raw:{model:{model:string}}) { return raw.model.model }
export function projection(session:RequestSession) { return session as RequestSession }
`)
  writeFileSync(join(directory,'tsconfig.json'),JSON.stringify({compilerOptions:{strict:true,skipLibCheck:true,target:'ES2022',module:'ESNext',moduleResolution:'bundler'},files:['context/session.ts']}))
  const api=new API({cwd:directory}),snapshot=api.updateSnapshot({openProjects:[join(directory,'tsconfig.json')]})
  try{
   const project=snapshot.getProjects()[0]!
   expect(project.program.getSyntacticDiagnostics(path)).toEqual([])
   expect(project.program.getSemanticDiagnostics(path)).toEqual([])
   expect(rawReasoningReads(directory,[path],snapshot).sort()).toEqual([
    'context/session.ts:10:agent',...Array(3).fill('context/session.ts:10:model'),
    'context/session.ts:11:request-construction','context/session.ts:12:request-construction',
    'context/session.ts:18:request-construction','context/session.ts:18:agent',...Array(3).fill('context/session.ts:18:model')
   ].sort())
  }finally{snapshot.dispose();api.close();rmSync(directory,{recursive:true,force:true})}
 })
 it('owns only start methods returned directly by the canonical top-level factory',()=>{
  const harness=resolve(process.cwd()),directory=mkdtempSync(join(tmpdir(),'request-factory-')),path=join(directory,'delegation.ts')
  writeFileSync(path,`import type {EngineSession} from ${JSON.stringify(join(harness,'engine'))}
export function withDelegation() {
 const unrelated={start(raw:EngineSession){return 'model' in raw?raw.model.model:raw.agent.model}}
 void unrelated
 return ({start(raw:EngineSession){return 'model' in raw?raw.model.model:raw.agent.model}} as {start(raw:EngineSession):string})
}
export function ordinary() {return {start(raw:EngineSession){return 'model' in raw?raw.model.model:raw.agent.model}}}
export function nested() {
 function withDelegation() {return {start(raw:EngineSession){return 'model' in raw?raw.model.model:raw.agent.model}}}
 return withDelegation()
}
`)
  writeFileSync(join(directory,'tsconfig.json'),JSON.stringify({compilerOptions:{strict:true,skipLibCheck:true,target:'ES2022',module:'ESNext',moduleResolution:'bundler'},files:['delegation.ts']}))
  const api=new API({cwd:directory}),snapshot=api.updateSnapshot({openProjects:[join(directory,'tsconfig.json')]})
  try{
   const project=snapshot.getProjects()[0]!
   expect(project.program.getSyntacticDiagnostics(path)).toEqual([])
   expect(project.program.getSemanticDiagnostics(path)).toEqual([])
   expect(rawReasoningReads(directory,[path],snapshot).sort()).toEqual([
    'delegation.ts:3:agent',...Array(3).fill('delegation.ts:3:model'),
    'delegation.ts:7:agent',...Array(3).fill('delegation.ts:7:model'),
    'delegation.ts:9:agent',...Array(3).fill('delegation.ts:9:model')
   ].sort())
  }finally{snapshot.dispose();api.close();rmSync(directory,{recursive:true,force:true})}
 })
 it('keeps raw reasoning members out of declared request session types',()=>{
  const harness=resolve(process.cwd()),directory=mkdtempSync(join(tmpdir(),'request-boundary-')),path=join(directory,'probe.ts')
  writeFileSync(path,`import type {AgentAssistantSession,RequestAssistantSession,RequestAgentAssistantSession,RequestSession} from ${JSON.stringify(join(harness,'engine'))}
export function probe(raw:AgentAssistantSession,assistant:RequestAssistantSession,agent:RequestAgentAssistantSession,session:RequestSession) {}`)
  writeFileSync(join(directory,'tsconfig.json'),JSON.stringify({compilerOptions:{strict:true,skipLibCheck:true,target:'ES2022',module:'ESNext',moduleResolution:'bundler'},files:['probe.ts']}))
  const api=new API({cwd:directory}),snapshot=api.updateSnapshot({openProjects:[join(directory,'tsconfig.json')]})
  try{
   const project=snapshot.getProjects()[0]
   expect(project).toBeDefined()
   const {program,checker}=project!,file=program.getSourceFile(path)!
   expect(program.getSyntacticDiagnostics(path)).toEqual([])
   expect(program.getSemanticDiagnostics(path)).toEqual([])
   const probe=file.statements.find(ts.isFunctionDeclaration)!
   expect(probe.parameters).toHaveLength(4)
   const constituents=(type:Type):Type[]=>type.isUnionType()?type.getTypes().flatMap(constituents):[type]
   for(const parameter of probe.parameters){
    const type=checker.getTypeAtLocation(parameter.name)!
    // Positive controls ensure imports resolve to the real session declarations.
    expect(checker.getPropertyOfType(type,'prompt'),parameter.name.getText(file)).toBeDefined()
    if(parameter.name.getText(file)==='raw')expect(checker.getPropertyOfType(type,'effort')).toBeDefined()
    else for(const constituent of constituents(type)){
     // Union-wide lookups omit members exposed by only one constituent.
     expect(checker.getPropertyOfType(constituent,'prompt'),parameter.name.getText(file)).toBeDefined()
     expect(checker.getPropertyOfType(constituent,'values'),parameter.name.getText(file)).toBeDefined()
     for(const member of ['effort','thinking','criticReasoning','model','agent','tools','hostTools','policy','policyProject','policyConnections','policyRequests','taskClass','paceUsed','critic','criticProject','skills','subagents','childDefinition'])expect(checker.getPropertyOfType(constituent,member),`${parameter.name.getText(file)} exposes raw ${member}`).toBeUndefined()
    }
   }
  }finally{snapshot.dispose();api.close();rmSync(directory,{recursive:true,force:true})}
 })
 it('carries the complete shared request book, including compactor and a future member',()=>{
  for(const members of requestValuesVector.items){const view=new RequestValues(members as PolicyMembers);for(const [name,member]of Object.entries(members))expect(view.member(name),name).toEqual(member);expect(view.reasoning('author')).toBeUndefined();expect(view.reasoning('compactor')).toBeUndefined()}
 })
 it('keeps admitted binding and tool data detached',async()=>{
  const original=base(),selected={model:'admitted',run:vi.fn()},host={name:'read_evidence',description:'Read evidence.',inputSchema:{type:'object'},execute:vi.fn(async()=>({}))}
  const session:AgentAssistantSession={...original,hostTools:[host],policy:{models:{author:{provider:'selected',model:'selected'}}},policyConnections:{selected:{agent:selected}}}
  const opened=await contextualSession(session,'codex')
  try{expect(opened.session.values.allowsTool('read_evidence','host','critic')).toBe(false);expect(opened.session.values.allowsTool('read_evidence','host','reformat')).toBe(false);selected.model='changed raw';host.inputSchema.type='array';expect(opened.session.values.agent.model).toBe('admitted');const tools=opened.session.values.hostTools;expect(tools.find(t=>t.name==='read_evidence')!.inputSchema).toEqual({type:'object'});tools[0]!.name='changed copy';expect(opened.session.values.hostTools[0]!.name).toBe('read_evidence')}finally{opened.context.close()}
 })
 it('resolves a role capability only when that admitted role is consumed',async()=>{
  const raw={prompt:'Draft.',testPrompt:'',tools:[],hostTools:[],callTool:vi.fn(),signal:new AbortController().signal,model:{family:'openai-compatible' as const,model:'fixture',call:vi.fn()}}
  const opened=await contextualSession(raw,'vercel'),previous=endpointCapabilities,asked=vi.fn(()=>({nativeToolCalls:false}))
  configureHarness({endpointCapabilities:asked})
  try{const critic=opened.session.values.forRole('critic');expect(asked).not.toHaveBeenCalled();expect(critic.capabilities.nativeToolCalls).toBe(false);expect(asked).toHaveBeenCalledOnce()}finally{configureHarness({endpointCapabilities:previous});opened.context.close()}
 })
 it('rejects raw reads of every request value in the compiler, including copy and helper shapes',()=>{
  const harness=resolve(process.cwd()),directory=mkdtempSync(join(tmpdir(),'request-values-')),path=join(directory,'probe.ts')
  const fields=['model','agent','tools','hostTools','thinking','effort','criticReasoning','policy','policyProject','policyConnections','policyRequests','taskClass','paceUsed','skills','subagents','critic','criticProject']
  const body=fields.map((field,index)=>`export function probe${index}(session:RequestSession){
 void session.${field}
 const alias=session;void alias.${field}
 const {${field}:held}=session;void held
 const key='${field}' as const;void session[key]
 const copied={...session};void copied.${field}
}`).join('\n')
  const capabilityCases=CAPABILITY_MEMBERS.map(name=>`void model.values.model.capabilities.${name}`).join('\n')
  writeFileSync(path,`import type {RequestSession,RequestAssistantSession,RequestAgentAssistantSession} from ${JSON.stringify(join(harness,'engine'))}\n${body}\nexport function capabilities(model:RequestAssistantSession,agent:RequestAgentAssistantSession){${capabilityCases}\nvoid agent.values.agent.effort}`)
  writeFileSync(join(directory,'tsconfig.json'),JSON.stringify({compilerOptions:{strict:true,skipLibCheck:true,target:'ES2022',module:'ESNext',moduleResolution:'bundler'},files:['probe.ts']}))
  const api=new API({cwd:directory}),snapshot=api.updateSnapshot({openProjects:[join(directory,'tsconfig.json')]})
  try{const program=snapshot.getProjects()[0]!.program;expect(program.getSyntacticDiagnostics(path)).toEqual([]);const errors=program.getSemanticDiagnostics(path);expect(errors).toHaveLength(fields.length*5+CAPABILITY_MEMBERS.length+1);expect(errors.every(error=>[2339,7053].includes(error.code))).toBe(true)}finally{snapshot.dispose();api.close();rmSync(directory,{recursive:true,force:true})}
 })
 it('protects an added raw field and a new writer without adding its name to the guard',()=>{
  const harness=resolve(process.cwd()),directory=mkdtempSync(join(tmpdir(),'request-new-field-')),path=join(directory,'newWriter.ts'),engine=JSON.stringify(join(harness,'engine'))
  writeFileSync(path,`import type {AssistantSession,RequestSession} from ${engine};import {RequestValues} from ${JSON.stringify(join(harness,'context/requestValues'))}
declare module ${engine}{interface AssistantSession{addedResolvedRequest?:string;memory?:string;nestedResolved?:{prompt:string}}}
export function newWriter(raw:AssistantSession){
 const key='addedResolvedRequest' as const
 void raw[key]
 const alias=raw;void alias.addedResolvedRequest
 const copied={...raw};void copied.addedResolvedRequest
 raw.addedResolvedRequest='raw write'
 void raw.memory;raw.memory='raw write';const nested=raw.nestedResolved;if(nested)void nested.prompt;if(raw.context)void raw.context.memory;const unrelated={addedResolvedRequest:'allowed',memory:'allowed'};void unrelated.addedResolvedRequest;void unrelated.memory
}
export function boundary(session:RequestSession){
 // @ts-expect-error A new raw declaration does not extend the operational allowlist.
 void session.addedResolvedRequest
}
export function newConversion(){const Values=RequestValues;new Values({});return {} as unknown as RequestSession}`)
  writeFileSync(join(directory,'tsconfig.json'),JSON.stringify({compilerOptions:{strict:true,skipLibCheck:true,target:'ES2022',module:'ESNext',moduleResolution:'bundler'},files:['newWriter.ts']}))
  const api=new API({cwd:directory}),snapshot=api.updateSnapshot({openProjects:[join(directory,'tsconfig.json')]})
  try{expect(snapshot.getProjects()[0]!.program.getSemanticDiagnostics(path)).toEqual([]);expect(rawReasoningReads(directory,[path],snapshot)).toEqual([...([5,6,7,8].map(line=>`newWriter.ts:${line}:addedResolvedRequest`)), 'newWriter.ts:9:memory','newWriter.ts:9:memory','newWriter.ts:9:nestedResolved','newWriter.ts:9:prompt','newWriter.ts:15:request-construction','newWriter.ts:15:request-construction'])}finally{snapshot.dispose();api.close();rmSync(directory,{recursive:true,force:true})}
  const raw={...base(),addedResolvedRequest:'raw'},members={'addedResolvedRequest':{value:{enabled:true},layer:'run' as const,source:'layer' as const}}
  const converted=requestSession(raw,members)
  expect(converted).not.toHaveProperty('addedResolvedRequest')
  expect(converted.values.member('addedResolvedRequest')).toEqual(members.addedResolvedRequest)
  ;(converted.values.member('addedResolvedRequest')!.value as {enabled:boolean}).enabled=false
  expect(converted.values.member('addedResolvedRequest')).toEqual(members.addedResolvedRequest)
 })
 it('omits an omitted agent effort and carries model-default source into the event and checkpoint',async()=>{
  const sent:Record<string,unknown>[]=[]
  const session={...base(),critic:'off' as const,context:{currentRequest:{message:'Draft a pack.',input:'Draft a pack.'}},agent:{model:'connected',run:vi.fn(async(request,callbacks)=>{sent.push({...request});await callbacks.event({type:'message',id:'done',text:'Done.',phase:'final'})})}}
  const events:AssistantEvent[]=[]
  for await(const event of codex.start(session))events.push(event)
  expect(sent).toHaveLength(1)
  expect(sent[0]).not.toHaveProperty('effort')
  expect(events.find(e=>e.type==='policy_resolved')).toMatchObject({members:{'models.author.reasoning':{layer:'table',source:'model-default'}}})
  expect(events.filter(e=>e.type==='execution_checkpoint').at(-1)).toMatchObject({checkpoint:{policy:{'models.author.reasoning':{layer:'table',source:'model-default'}}}})
  const saved=(events.find(e=>e.type==='execution_checkpoint') as Extract<AssistantEvent,{type:'execution_checkpoint'}>).checkpoint
  const resumed:Record<string,unknown>[]=[]
  const resumedSession={...session,context:{...session.context,execution:saved},agent:{model:'connected',run:vi.fn(async(request,callbacks)=>{resumed.push({...request});await callbacks.event({type:'message',id:'done',text:'Done.',phase:'final'})})}}
  const resumedEvents:AssistantEvent[]=[]
  for await(const event of codex.start(resumedSession))resumedEvents.push(event)
  expect(JSON.stringify(resumed[0])).toBe(JSON.stringify(sent[0]))
  expect(resumedEvents.find(e=>e.type==='policy_resolved')).toMatchObject({members:{'models.author.reasoning':{layer:'table',source:'model-default'}}})
  expect(resumedEvents.filter(e=>e.type==='execution_checkpoint').at(-1)).toMatchObject({checkpoint:{policy:{'models.author.reasoning':{layer:'table',source:'model-default'}}}})
 })
 it('uses a discovered default, but refuses an explicit unsupported none before generation',async()=>{
  const discovered={reasoning:{default:'high' as const,supported:['low','medium','high'] as const}}
  const run=vi.fn<AgentRun>(async(request,callbacks)=>{expect(request.effort).toBe('high');await callbacks.event({type:'message',id:'done',text:'Done.',phase:'final'})})
  const admitted={...base(),critic:'off' as const,policy:{models:{author:{discovered:{reasoning:{...discovered.reasoning,supported:[...discovered.reasoning.supported]}}}}},agent:{model:'connected',run}}
  const events:AssistantEvent[]=[]
  for await(const event of codex.start(admitted))events.push(event)
  expect(run).toHaveBeenCalledTimes(1)
  expect(events.find(e=>e.type==='policy_resolved')).toMatchObject({members:{'models.author.reasoning':{value:'high',source:'discovered'}}})
  const refusedRun=vi.fn<AgentRun>()
  const refused:AgentAssistantSession={...base(),policy:{models:{author:{reasoning:'none',discovered:{reasoning:{default:'high',supported:['low','medium','high']}}}}},agent:{model:'connected',run:refusedRun}}
  const refusedEvents:AssistantEvent[]=[]
  for await(const event of codex.start(refused))refusedEvents.push(event)
  expect(refusedRun).not.toHaveBeenCalled()
  expect(refusedEvents.find(e=>e.type==='guardrail')).toMatchObject({tool:'models.author.reasoning',action:'refused',reason:'policy refused',detail:expect.stringContaining('model "connected"')})
  expect(refusedEvents.find(e=>e.type==='error')).toMatchObject({failure:{code:'configuration',outcome:'not-started'},message:expect.stringContaining('reasoning value "none" is not supported by model "connected"')})
 })
 for(const row of omission.dropped.filter(row=>row.capability==='agent'))it(`${row.name} stays absent in the request and over resume`,async()=>{
  const requests:Record<string,unknown>[][]=[]
  const makeSession=(sent:Record<string,unknown>[]):AgentAssistantSession=>({...base(),effort:row.raw as AgentAssistantSession['effort'],critic:row.role==='critic'?'on':'off',testPrompt:row.role==='critic'?'Refute it.':'',
   policy:{models:{[row.role]:{discovered:{reasoning:{supported:[...row.supported] as ReasoningValue[]}}}}},context:{currentRequest:{message:'Draft a pack.',input:'Draft a pack.'}},
   agent:{model:'connected',run:vi.fn(async(request,callbacks)=>{sent.push({...request});const text=request.phase==='critic'?'No counterexample found.':row.role==='critic'?'```json\n{"proposal":{"document":{"id":"pack"},"unknowns":[]}}\n```':'Done.';await callbacks.event({type:'message',id:`${request.phase}-done`,text,phase:'final'})})}})
  const seed=makeSession([]),opened=await contextualSession(seed,'codex'),saved=opened.context.execution.snapshot('stopped');opened.context.close()
  for(const execution of [undefined,saved]){
   const sent:Record<string,unknown>[]=[],session=makeSession(sent)
   if(execution)session.context={...session.context,execution}
   const events:AssistantEvent[]=[];for await(const event of codex.start(session))events.push(event)
   expect(events.filter(event=>event.type==='error'),row.name).toEqual([])
   const request=sent.find(one=>one.phase===row.role)!
   expect(request,row.name).not.toHaveProperty(row.requestPath)
   const resolved=events.find(event=>event.type==='policy_resolved') as Extract<AssistantEvent,{type:'policy_resolved'}>
   expect(resolved.members[`models.${row.role}.reasoning`],row.name).toEqual(row.resolved)
   if(row.decisionPath)expect(events.find(event=>event.type==='critic_decided'),row.name).not.toHaveProperty(row.decisionPath)
   expect(events.filter(event=>event.type==='execution_checkpoint').at(-1)?.checkpoint.policy?.[`models.${row.role}.reasoning`],row.name).toEqual(row.resolved)
   requests.push(sent)
  }
  expect(JSON.stringify(requests[1]),row.name).toBe(JSON.stringify(requests[0]))
 })
 for(const [i,input] of collisions.items.entries())it(`legacy collision ${i}: record and agent wire agree`,async()=>{
  const expected=collisions.expected[i]!
  const sent:{phase:string;effort:unknown}[]=[]
  const run:AgentRun=async(request,callbacks)=>{
   sent.push({phase:request.phase,effort:request.effort})
   const text=request.phase==='critic'?'No counterexample found.':'```json\n{"proposal":{"document":{"id":"pack"},"unknowns":[]}}\n```'
   await callbacks.event({type:'message',id:'reply',text,phase:'final'})
  }
  const session={...base(),...input,...collisions.cases[i],critic:'on',agent:{model:'connected',run}} as AgentAssistantSession
  const opened=await contextualSession(session,'codex')
  expect(opened.context.resolvedPolicy['models.author.reasoning']?.value).toBe(expected.author)
  expect(opened.context.resolvedPolicy['models.critic.reasoning']).toEqual({value:expected.critic,layer:'run',source:'layer'})
  expect(opened.session.values.reasoning).toBe(expected.author)
  opened.context.close()
  const events:AssistantEvent[]=[]
  for await(const event of codex.start(session))events.push(event)
  expect(events.filter(e=>e.type==='error')).toEqual([])
  expect(sent).toEqual([{phase:'author',effort:expected.author},{phase:'critic',effort:expected.critic}])
  expect(events.find(e=>e.type==='critic_decided')).toMatchObject({reasoning:expected.critic})
  expect(events.find(e=>e.type==='policy_resolved')).toMatchObject({members:{'models.author.reasoning':{value:expected.author},'models.critic.reasoning':{value:expected.critic}}})
 })
 it('starts withDelegation(codex) with valid agent tiers and the real connection',async()=>{
  const original=vi.fn<AgentRun>()
  const selected=vi.fn<AgentRun>(async(request,callbacks)=>{
   expect(request.effort).toBe('low')
   await callbacks.event({type:'message',id:'done',text:'Done.',phase:'final'})
  })
  const session:AgentAssistantSession={...base(),interactive:true,agent:{model:'connected',run:original},
   policy:{models:{author:{model:'reviewer',provider:'reviewer',reasoning:'low'}}},policyConnections:{reviewer:{agent:{model:'review-model',run:selected}}}}
  const events:AssistantEvent[]=[]
  for await(const event of withDelegation(codex).start(session))events.push(event)
  expect(events.filter(e=>e.type==='error')).toEqual([])
  expect(selected).toHaveBeenCalledTimes(1);expect(original).not.toHaveBeenCalled()
  expect(events.find(e=>e.type==='policy_resolved')).toMatchObject({members:{'models.author.model':{value:'reviewer',layer:'run'}}})
 })
})
