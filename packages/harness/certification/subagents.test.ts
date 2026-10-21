import {describe,it,expect} from 'vitest'
import {vercel} from '../engines/vercel'
import {deepagents} from '../engines/deepagents'
import {codex} from '../engines/codex'
import {withDelegation} from '../delegation'
import {subagentDigest,type SubagentDeclaration} from '../subagents'
import {skillDigest,type SkillDeclaration} from '../skills'
import {normalize} from '../thinking'
import type {Engine,EngineSession,AssistantEvent} from '../engine'
import definitionVector from '../../../contract/vectors/subagents/declaration.json'
const task={title:'Evidence',objective:'Check the evidence.',profile:'auditor'}
const report='Child evidence.\n```report\n'+JSON.stringify({summary:'Evidence checked.',findings:[],citations:[],openQuestions:[],confidence:'low',notDone:[]})+'\n```'
async function play(engine:Engine,restored?:import('../execution/state').ExecutionCheckpoint,changed=false,mode?:string){
 const agent=engine.id==='codex'
 const skill:SkillDeclaration={id:'evidence',name:'evidence',description:'Check evidence.',instructions:'SKILL EVIDENCE RULE: cite supplied sources.',source:{kind:'custom',id:'project',version:'1',trust:'person'},tools:['lookup'],pinnedDigest:''}
 skill.pinnedDigest=await skillDigest(skill)
 const definition={...structuredClone(definitionVector.items[0]),model:'fixture',reasoning:'low',skills:['evidence'],bounds:{...definitionVector.items[0]!.bounds,modelRequests:5,bytes:24000}} as SubagentDeclaration
 definition.pinnedDigest=await subagentDigest(definition)
 if(mode){
  if(mode==='tool')definition.tools=['missing']
  if(mode==='skill')definition.skills=['missing']
  if(mode==='bounds')definition.bounds.bytes=180001
  if(mode==='digest'||mode==='reapproved')definition.instructions+=' Changed.'
  if(mode!=='digest')definition.pinnedDigest=await subagentDigest(definition)
 }
 const requests:any[]=[],toolResults:unknown[]=[],events:AssistantEvent[]=[];let parent=restored||['tool','skill','bounds','digest'].includes(mode??'')?1:0,child=0,reads=0
 const selectedTask=mode==='taskBounds'?{...task,bounds:{bytes:999999}}:task
 const steps=[{name:'load_skill',arguments:{name:'evidence'}},{name:'lookup',arguments:{}},{name:'delegate_tasks',arguments:{tasks:[task]}}]
 const common={policy:{models:{'child.auditor':{reasoning:agent?'none':'off'}}},context:{...(restored?{execution:restored}:{}),currentRequest:{message:'Delegate the evidence check.',input:'Delegate the evidence check.'}},prompt:'Delegate the evidence check.',testPrompt:'',tools:[],hostTools:[{name:'lookup',effect:'read' as const,description:'Read evidence.',inputSchema:{type:'object',properties:{}},execute:async()=>{reads++;return {content:[{type:'text' as const,text:'EVIDENCE: supplied facts.'}]}}}],skills:[skill],subagents:restored?(changed?[{...definition,instructions:definition.instructions+' Changed.'}]:[]):[definition],callTool:async()=>({isError:true,content:[{type:'text' as const,text:'refused'}]}),signal:new AbortController().signal,allowConversation:true,interactive:true,critic:'off' as const}
 let session:EngineSession
 if(agent)session={...common,effort:'low',agent:{model:'fixture',run:async(request,callbacks)=>{
  requests.push(request);const isChild=request.instructions.includes(definition.instructions)
  if(!isChild&&parent++===0){const tool=request.tools.find(t=>t.description.startsWith('delegate_tasks:'))!;toolResults.push(await callbacks.tool({id:'delegate',name:tool.name,arguments:{tasks:[selectedTask]}},callbacks.signal));return}
  if(isChild){for(const step of steps){const tool=request.tools.find(t=>t.description.startsWith(step.name+':'));await callbacks.tool({id:step.name,name:tool?.name??step.name,arguments:step.arguments},callbacks.signal)}}
  await callbacks.event({type:'message',id:'answer',text:isChild?report:'Findings received.',phase:'final'})
 }}}
 else session={...common,thinking:normalize('low','openai-compatible'),model:{family:'openai-compatible',model:'fixture',call:async(_suffix,request)=>{
  const body=JSON.parse(request.body??'{}');requests.push(body)
  const isChild=JSON.stringify(body.messages?.filter((m:any)=>m.role==='system')).includes(definition.instructions)
  const step=isChild?steps[child++]:parent++===0?{name:'delegate_tasks',arguments:{tasks:[selectedTask]}}:undefined
  return new Response(JSON.stringify({id:'answer',object:'chat.completion',choices:[{index:0,message:{role:'assistant',content:step?null:isChild?report:'Findings received.',...(step?{tool_calls:[{id:'call-'+requests.length,type:'function',function:{name:step.name,arguments:JSON.stringify(step.arguments)}}]}:{})},finish_reason:step?'tool_calls':'stop'}]}),{headers:{'content-type':'application/json'}})
 }}}
 for await(const event of withDelegation(engine).start(session))events.push(event)
 return {requests,toolResults,events,reads,definition}
}
for(const engine of [vercel,deepagents,codex])describe(`${engine.id==='codex'?'agent ':''}engine ${engine.id} · scenarios`,()=>{
 it('13 declared sub-agent offer, scope, skills, tier, bounds and records',async()=>{
  const {requests,events,reads,definition}=await play(engine)
  expect(events.filter(e=>e.type==='error')).toEqual([])
  expect(JSON.stringify(requests[0])).toContain('DECLARED SUB-AGENTS')
  for(const id of ['analyze','review','research','author-test','auditor'])expect(JSON.stringify(requests[0])).toContain(id)
  expect(reads).toBe(1)
  const tasks=events.filter(e=>e.type==='delegated_task');expect(tasks.length).toBeGreaterThan(0)
  expect(tasks.at(-1)?.task).toMatchObject({state:'complete',profile:'auditor',subagent:{id:'auditor',digest:definition.pinnedDigest,source:definition.source},budget:{modelRequests:5,toolCalls:2,milliseconds:30000}})
  expect(tasks.every(t=>t.subagent?.digest===definition.pinnedDigest)).toBe(true)
  const children=requests.filter(r=>JSON.stringify(r.instructions??r.messages?.filter((m:any)=>m.role==='system')).includes(definition.instructions))
  expect(children.length).toBeGreaterThan(0)
  for(const child of children){const tools=JSON.stringify(child.tools);expect(tools).toContain('lookup');expect(tools).toContain('load_skill');expect(tools).not.toContain('delegate_tasks:');expect(tools).not.toContain('request_review');expect(tools).not.toContain('propose_pack');expect(child.effort??child.reasoning_effort).toBe('low')}
  expect(events.some(e=>e.type==='guardrail'&&e.reason==='subagent refused')).toBe(true)
  const checkpoint=events.filter(e=>e.type==='execution_checkpoint').at(-1)?.checkpoint
  expect(checkpoint?.delegation?.batches.flatMap(b=>b.children).some(c=>c.subagent?.digest===definition.pinnedDigest)).toBe(true)
 })
 it('13 restored withdrawn or changed definition is reported without dispatch',async()=>{
  const first=await play(engine)
  const checkpoint=first.events.filter(e=>e.type==='execution_checkpoint').at(-1)!.checkpoint
  for(const changed of [false,true]){
   const saved=structuredClone(checkpoint);saved.status='running'
   saved.delegation!.batches[0]!.children[0]!.state='queued'
   const resumed=await play(engine,saved,changed)
   expect(resumed.events.filter(e=>e.type==='error')).toEqual([])
   expect(resumed.reads).toBe(0)
   expect(resumed.events.some(e=>e.type==='guardrail'&&e.reason==='subagent withheld'&&e.detail.includes('not dispatched again'))).toBe(true)
   const record=resumed.events.filter(e=>e.type==='execution_checkpoint').at(-1)?.checkpoint.delegation?.batches[0]?.children[0]
   expect(record).toMatchObject({state:'interrupted',subagent:{id:'auditor',digest:first.definition.pinnedDigest},problem:'The declared sub-agent was withdrawn or changed; it was not dispatched again.'})
  }
 })

 it('13 invalid declared tools, skills, digest and bounds are withheld before dispatch',async()=>{
  for(const mode of ['tool','skill','digest','bounds']){
   const result=await play(engine,undefined,false,mode)
   expect(result.events.filter(e=>e.type==='error')).toEqual([])
   expect(result.events.some(e=>e.type==='guardrail'&&e.reason==='subagent withheld')).toBe(true)
   expect(result.reads).toBe(0)
   expect(result.requests).toHaveLength(1)
   expect(JSON.stringify(result.requests[0])).not.toContain('DECLARED SUB-AGENTS')
  }
  const approved=await play(engine,undefined,false,'reapproved')
  expect(approved.events.filter(e=>e.type==='error')).toEqual([])
  expect(approved.reads).toBe(1)
  expect(approved.events.some(e=>e.type==='delegated_task'&&e.task.state==='complete'&&e.subagent?.digest===approved.definition.pinnedDigest)).toBe(true)
  const widened=await play(engine,undefined,false,'taskBounds')
  expect(widened.reads).toBe(0)
  expect(widened.events.filter(e=>e.type==='delegated_task')).toEqual([])
  expect(JSON.stringify([widened.requests,widened.toolResults,widened.events])).toContain('Delegate one to three focused tasks')
 })

})
