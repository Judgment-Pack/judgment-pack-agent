import {describe,it,expect,vi} from 'vitest'
import {join,resolve} from 'node:path'
import {tmpdir} from 'node:os'
import {mkdtempSync,readdirSync,rmSync,writeFileSync} from 'node:fs'
import {API,type Snapshot,type Type} from 'typescript/unstable/sync'
import * as ts from 'typescript/unstable/ast'
import {contextualSession} from './context/session'
import {codex} from './engines/codex'
import {withDelegation} from './delegation'
import type {AgentRun} from './agent'
import type {AgentAssistantSession,AssistantEvent,EngineSession,RequestSession} from './engine'
import type {ReasoningValue} from './policy'
import collisions from '../../contract/vectors/policy/legacy-collisions.json'
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
   if(symbol?.declarations.some(declaration=>declaration.resolve()?.getSourceFile().fileName.endsWith('/engine.ts')))return true
   if(ts.isObjectLiteralExpression(expression))return expression.properties.some(property=>ts.isSpreadAssignment(property)&&rawMember(property.expression,name))
   if(ts.isParenthesizedExpression(expression)||ts.isAsExpression(expression)||ts.isNonNullExpression(expression))return rawMember(expression.expression,name)
   return false
  }
  // The checker retains literal keys through annotations, unions and const aliases.
  const literalKeys=(type:Type|undefined):string[]=>type?.isStringLiteralType()?[type.value]:type?.isUnionType()?type.getTypes().flatMap(literalKeys):[]
  const record=(node:ts.Node,name:string,base:ts.Expression):void=>{
   if(!['effort','thinking','criticReasoning'].includes(name)||!rawMember(base,name))return
   let parent:ts.Node|undefined=node,fn=''
   while(parent){if(ts.isFunctionDeclaration(parent)&&parent.name){fn=parent.name.text;break}parent=parent.parent}
   const relative=path.slice(harness.length+1),allowed=relative==='context/session.ts'&&(fn==='resolveSessionPolicy'||fn==='requestSession')
   if(!allowed)unexpected.push(`${relative}:${file.getLineAndCharacterOfPosition(node.getStart(file)).line+1}:${name}`)
  }
  const visit=(node:ts.Node):void=>{
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
  expect(opened.session.reasoning).toBe(expected.author)
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
