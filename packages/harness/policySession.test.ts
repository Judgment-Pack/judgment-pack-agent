import {describe,it,expect,vi} from 'vitest'
import {join} from 'node:path'
import {API} from 'typescript/unstable/sync'
import * as ts from 'typescript/unstable/ast'
import {contextualSession} from './context/session'
import {codex} from './engines/codex'
import {withDelegation} from './delegation'
import type {AgentRun} from './agent'
import type {AgentAssistantSession,AssistantEvent} from './engine'
import type {ReasoningValue} from './policy'
import collisions from '../../contract/vectors/policy/legacy-collisions.json'
import omission from '../../contract/vectors/providers/reasoning-omission.json'

const base=():AgentAssistantSession=>({prompt:'Draft a pack.',testPrompt:'Review the pack.',tools:[],hostTools:[],callTool:vi.fn(),signal:new AbortController().signal,allowConversation:true,agent:{model:'connected',run:vi.fn()}})

function aliasedRawReasoningRead(session:{effort?:string}){const raw=session;return raw.effort}
function destructuredRawReasoningRead(session:{effort?:string}){const {effort}=session;return effort}
function computedRawReasoningRead(session:{effort?:string}){const key='effort';return session[key]}

type ReasoningRead={path:string,line:number,member:string,root:string}
const sensitive=new Set(['effort','thinking','thinkingBudget','reasoning_effort','criticReasoningFloor','author'])
const stringValue=(node:ts.Expression|ts.PropertyName|undefined,strings:Map<string,string>):string|undefined=>{
 if(!node)return undefined
 if(ts.isStringLiteralLikeNode(node))return node.text
 if(ts.isIdentifier(node))return strings.get(node.text)
 if(ts.isBinaryExpression(node)&&node.operatorToken.kind===ts.SyntaxKind.PlusToken){const left=stringValue(node.left,strings),right=stringValue(node.right,strings);return left===undefined||right===undefined?undefined:left+right}
 return undefined
}
const propertyNameValue=(node:ts.PropertyName|undefined,strings:Map<string,string>)=>node&&ts.isIdentifier(node)?node.text:stringValue(node,strings)
const reasoningReads=(file:ts.SourceFile,path:string,roots:string[],scope:ts.Node=file):ReasoningRead[]=>{
 const aliases=new Map<string,string>(),strings=new Map<string,string>(),reads:ReasoningRead[]=[]
 const rooted=(value:string|undefined)=>value!==undefined&&roots.some(root=>value===root||value.startsWith(`${root}.`))
 const canonical=(node:ts.Expression|undefined):string|undefined=>{
  if(!node)return undefined
  if(ts.isIdentifier(node))return aliases.get(node.text)??node.text
  if(node.kind===ts.SyntaxKind.ThisKeyword)return 'this'
  if(ts.isParenthesizedExpression(node)||ts.isAsExpression(node)||ts.isTypeAssertion(node)||ts.isNonNullExpression(node))return canonical(node.expression)
  if(ts.isPropertyAccessExpression(node)){const base=canonical(node.expression);return base===undefined?undefined:`${base}.${node.name.text}`}
  if(ts.isElementAccessExpression(node)){const base=canonical(node.expression),member=stringValue(node.argumentExpression,strings);return base===undefined||member===undefined?undefined:`${base}.${member}`}
  return undefined
 }
 const line=(node:ts.Node)=>file.getLineAndCharacterOfPosition(node.getStart(file)).line+1
 const memberRead=(node:ts.Node,base:string|undefined,member:string|undefined)=>{if(member&&sensitive.has(member)&&rooted(base))reads.push({path,line:line(node),member,root:base!})}
 const bind=(name:ts.BindingName,base:string|undefined)=>{
  if(!rooted(base))return
  if(ts.isIdentifier(name)){aliases.set(name.text,base!);return}
  for(const element of name.elements){
   if(ts.isOmittedExpression(element)||element.name===undefined)continue
   const member=element.propertyName?propertyNameValue(element.propertyName,strings):ts.isIdentifier(element.name)?element.name.text:undefined
   memberRead(element,base,member)
   bind(element.name,member===undefined?base:`${base}.${member}`)
  }
 }
 const isWrite=(node:ts.Node)=>node.parent!==undefined&&ts.isBinaryExpression(node.parent)&&node.parent.left===node&&node.parent.operatorToken.kind>=ts.SyntaxKind.FirstAssignment&&node.parent.operatorToken.kind<=ts.SyntaxKind.LastAssignment
 const visit=(node:ts.Node):void=>{
  if(ts.isVariableDeclaration(node)&&node.initializer){
   if(ts.isIdentifier(node.name)){const value=stringValue(node.initializer,strings);if(value!==undefined)strings.set(node.name.text,value)}
   bind(node.name,canonical(node.initializer))
  }
  if(ts.isBinaryExpression(node)&&node.operatorToken.kind===ts.SyntaxKind.EqualsToken&&ts.isIdentifier(node.left))bind(node.left,canonical(node.right))
  if(!isWrite(node)&&ts.isPropertyAccessExpression(node))memberRead(node,canonical(node.expression),node.name.text)
  if(!isWrite(node)&&ts.isElementAccessExpression(node))memberRead(node,canonical(node.expression),stringValue(node.argumentExpression,strings))
  node.forEachChild(visit)
 }
 visit(scope)
 return reads
}
const guardedReads=[
 {path:'./context/session.ts',roots:['session','original'],allowed:['71:thinking','71:effort','838:effort']},
 {path:'./criticSwitch.ts',roots:['tiers','this.table'],allowed:['253:author','253:criticReasoningFloor']},
 {path:'./thinking.ts',roots:['session'],allowed:['533:thinking']},
 {path:'./engines/codex/index.ts',roots:['session'],allowed:['269:effort']},
 {path:'./engines/vercel/loop.ts',roots:['session'],allowed:['673:thinking','1495:thinking','1502:thinking']},
 {path:'./engines/deepagents/loop.ts',roots:['session'],allowed:['389:thinking','927:thinking','933:thinking']}
] as const
describe('ADR policy table review',()=>{
 it('constructs reasoning requests only from the AST-enumerated admitted reads',()=>{
  const root=process.env.JP_REASONING_GUARD_ROOT
  const files=guardedReads.map(row=>join(root??process.cwd(),...(root?['packages','harness',row.path.replace(/^\.\//,'')]:[row.path])))
  const api=new API({cwd:process.cwd()})
  const snapshot=root?api.updateSnapshot({openFiles:files}):api.updateSnapshot({openProjects:[join(process.cwd(),'tsconfig.json')]})
  try{for(const [at,row] of guardedReads.entries()){
   const program=snapshot.getDefaultProjectForFile(files[at]!)?.program??snapshot.getProjects()[0]?.program
   const file=program?.getSourceFile(files[at]!)
   expect(file,`${row.path} is in the compiler project`).toBeDefined()
   const got=reasoningReads(file!,row.path,[...row.roots]).map(read=>`${read.line}:${read.member}`)
   expect(got,row.path).toEqual(row.allowed)
  }}finally{snapshot.dispose();api.close()}
 })
 it('rejects aliased, destructured and computed raw reasoning reads',()=>{
  expect([aliasedRawReasoningRead,destructuredRawReasoningRead,computedRawReasoningRead]).toHaveLength(3)
  const path=join(process.cwd(),'policySession.test.ts'),api=new API({cwd:process.cwd()}),snapshot=api.updateSnapshot({openProjects:[join(process.cwd(),'tsconfig.json')]})
  try{
   const file=snapshot.getDefaultProjectForFile(path)!.program.getSourceFile(path)!,fixtures=new Map<string,ts.Node>()
   const visit=(node:ts.Node):void=>{if(ts.isFunctionDeclaration(node)&&node.name?.text.endsWith('RawReasoningRead'))fixtures.set(node.name.text,node);node.forEachChild(visit)};visit(file)
   for(const name of ['aliasedRawReasoningRead','destructuredRawReasoningRead','computedRawReasoningRead'])expect(reasoningReads(file,path,['session'],fixtures.get(name)).map(read=>read.member),name).toEqual(['effort'])
  }finally{snapshot.dispose();api.close()}
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
  expect(opened.session.effort).toBe(expected.author)
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
