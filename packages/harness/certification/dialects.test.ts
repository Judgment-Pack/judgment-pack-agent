import {describe,expect,it} from 'vitest'
import {vercel} from '../engines/vercel'
import {deepagents} from '../engines/deepagents'
import {codex} from '../engines/codex'
import {dialectFixture} from '../testing/dialects'
import {agentRunFrom,answerWith,type Turn} from '../testing/scenarioDriver'
import {runAssistantSession} from '../session'
import {normalize} from '../thinking'
import {childOffer} from '../delegation'
import type {AssistantEvent,EngineSession} from '../engine'
import fixture from '../../../contract/testdata/dialect-authoring/authoring.json'
import {contextDigest} from '../context/ledger'
for(const engine of [vercel,deepagents,codex])describe(`${engine.id==='codex'?'agent ':''}engine ${engine.id} · scenarios`,()=>{
 it('23 · dialect authoring uses certified tools and retains only an acknowledged current review proposal',async()=>{
  const args={...fixture.request,revision:fixture.snapshot.revision,packDigest:'sha256:'+await contextDigest(fixture.snapshot.packJson),dialectDigest:'sha256:'+await contextDigest(fixture.snapshot.dialectJson),...fixture.bindings}
  for(const mode of ['complete','plan','child','changed-preview','lost-ack','repeat-complete','failed-validation','transport-failed','preview-failed','draft-candidate-lost','draft-checked-drift','final-lineage-drift','empty','oversized']){
   const x=dialectFixture();x.state.mode=mode
   const originalDraft=x.ports.retainDraft,originalRetain=x.ports.retain
   x.ports.retainDraft=async(...params)=>{const ack=await originalDraft(...params);if(mode==='draft-candidate-lost'&&params[0].stage==='candidate')throw Error('Lost candidate ack');return mode==='draft-checked-drift'&&params[0].stage==='checked'?{...ack,previousReceipt:'wrong'}:ack}
   x.ports.retain=async(...params)=>{const ack=await originalRetain(...params);return mode==='final-lineage-drift'?{...ack,draft:{...ack.draft,checkedReceipt:'wrong'}}:ack}
   const turns:Turn[]=[{call:{tool:'read_dialect',arguments:fixture.request}},{call:{tool:'propose_dialect',arguments:mode==='empty'?{}:mode==='oversized'?{...args,summary:'x'.repeat(4001)}:args}},{say:'The proposal requires human review.'}]
   if(['repeat-complete','lost-ack','draft-candidate-lost','draft-checked-drift','final-lineage-drift'].includes(mode))turns.splice(2,0,{call:{tool:'propose_dialect',arguments:args}})
   const session={prompt:'Propose a structured dialect for review.',interactive:true,critic:'off' as const,context:{currentRequest:{message:'Propose a structured dialect for review.',input:'Propose a structured dialect for review.'},...(mode==='plan'?{phase:'plan' as const}:{}),...(mode==='child'?{objective:'Read structured fields.'}:{})}}
   const events:AssistantEvent[]=[],seen:{name:string}[]=[];let next=0
   await runAssistantSession(engine,{...session,testPrompt:'',tools:[],hostTools:mode==='child'?childOffer({capabilities:['read'],runtimeTools:[]},{tools:[],hostTools:x.tools}).hostTools:x.tools,allowConversation:true,signal:new AbortController().signal,callTool:async(name:string)=>{seen.push({name});throw Error('No decision Runtime tool was offered')},...(engine.id==='codex'?{agent:{model:'scripted-agent',run:agentRunFrom(turns)}}:{thinking:normalize('off','openai-compatible'),model:{family:'openai-compatible',model:'scripted-model',call:async()=>answerWith(turns[next++]??{say:'Done.'},'whole',next)}})} as EngineSession,event=>{events.push(event)})
   const ran={events,seen}
   expect(x.tools[0]!.taxonomy).toEqual({operation:'read',effect:'read'});expect(x.tools[1]!.taxonomy).toEqual({operation:'propose',effect:'propose'})
   expect(ran.events.some(e=>e.type==='tool_result'&&e.name==='read_dialect')).toBe(true)
   if(mode==='complete'||mode==='repeat-complete'){
    expect(x.state.retained).toHaveLength(mode==='repeat-complete'?2:1);expect(x.state.retained[0]!.documentJson).toBe(fixture.expectedDocument)
    expect(ran.events.some(e=>e.type==='tool_result'&&e.name==='propose_dialect'&&!e.isError&&e.text.includes('review-required'))).toBe(true)
   }else if(mode==='lost-ack'||mode==='final-lineage-drift'){
    expect(x.state.retained).toHaveLength(1);expect(ran.events.some(e=>e.type==='tool_result'&&e.name==='propose_dialect'&&e.isError)).toBe(true)
   }else{
    expect(x.state.retained,mode).toHaveLength(0)
    expect(ran.events.some(e=>e.type==='guardrail'&&e.tool==='propose_dialect'||e.type==='tool_result'&&e.name==='propose_dialect'&&e.isError)).toBe(true)
   }
   if(mode==='repeat-complete'){
    expect(x.state.drafts.map(d=>d.stage)).toEqual(['candidate','checked','candidate','checked'])
    expect(new Set(x.state.drafts.map(d=>d.attemptId)).size).toBe(2)
   }
   if(['failed-validation','preview-failed','changed-preview','draft-checked-drift','final-lineage-drift','lost-ack'].includes(mode))expect(x.state.drafts.map(d=>d.stage)).toEqual(['candidate','checked'])
   if(['transport-failed','draft-candidate-lost'].includes(mode))expect(x.state.drafts.map(d=>d.stage)).toEqual(['candidate'])
   if(mode==='draft-candidate-lost')expect(x.state.calls.some(c=>c.startsWith('dialect_')||c==='get_dialect_contract'||c==='preview')).toBe(false)
   if(mode==='draft-checked-drift')expect(x.state.calls.includes('preview')).toBe(false)
   expect(ran.seen.some(c=>['write_file','save_dialect','experimental_evaluate'].includes(c.name))).toBe(false)
  }
 })
})
