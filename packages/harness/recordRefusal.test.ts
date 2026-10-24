import {describe,it,expect} from 'vitest'
import checkpoint from '../../contract/vectors/record-versions/checkpoint.json'
import {checkRecord,UnknownRecordMember,readRunEvent,type RecordKind} from './recordReader'
import {managedContext} from './context/managed'
import {OutputGuard,OutputRefused} from './outputGuard'
import type {AgentAssistantSession,AssistantEvent} from './engine'

const marker='boundary_sentinel'
const patterns=[{name:'record-marker',pattern:marker}]
const notice={version:1,type:'guardrail',tool:'records',action:'refused',reason:'record member unsupported',detail:'Record contains an unsupported member.'}

describe('record refusal output boundary',()=>{
 it('keeps member diagnostics separate from input-independent public events',()=>{
  for(const kind of ['checkpoint','event','policy'] as RecordKind[])for(const member of ['',marker,marker+'x'.repeat(10000)]){
   const body=kind==='checkpoint'?structuredClone(checkpoint.items[1]):kind==='event'?{version:1,type:'end'}:{version:1}
   try{checkRecord(kind,{...body,[member]:true});throw Error('unknown member admitted')}catch(error){
    expect(error).toBeInstanceOf(UnknownRecordMember)
    const held=error as UnknownRecordMember
    expect(held.member).toBe('/'+member)
    expect(held.message).toBe('Unknown record member: '+('/'+member).slice(0,256)+'.')
    expect(held.event()).toEqual(notice)
    expect(readRunEvent(held.event())).toEqual(notice)
   }
  }
 })
 for(const mode of ['redact','refuse'] as const)it(`${mode}: startup refuses checkpoint/policy members without exposing them or running work`,async()=>{
  const guard=new OutputGuard({outputGuard:{value:mode,layer:'run'},outputPatterns:{value:patterns,layer:'run'}},()=>({}),()=>{})
  if(mode==='refuse')expect(()=>guard.value(marker,'message')).toThrow(OutputRefused)
  else expect(guard.value(marker,'message')).toBe('[redacted: record-marker]')
  for(const kind of ['checkpoint','policy'] as const)for(const invalidPatterns of [false,true]){
   let runners=0,providers=0,tools=0
   const policy={outputGuard:mode,outputPatterns:invalidPatterns?[{name:'record-marker',pattern:'('}]:patterns}
   if(invalidPatterns)expect(()=>new OutputGuard({outputGuard:{value:mode,layer:'run'},outputPatterns:{value:policy.outputPatterns,layer:'run'}},()=>({}),()=>{})).toThrow()
   const session:AgentAssistantSession={prompt:'Review',testPrompt:'',tools:[],hostTools:[],callTool:async()=>{tools++;throw Error('unexpected tool')},signal:new AbortController().signal,allowConversation:true,interactive:true,critic:'off',policy:kind==='policy'?({...policy,[marker]:true} as never):policy,context:kind==='checkpoint'?{execution:{...structuredClone(checkpoint.items[1]),[marker]:true} as never,currentRequest:{message:'Review',input:'review'}}:undefined,agent:{model:'fixture',run:async()=>{providers++;throw Error('unexpected provider')}}}
   const events:AssistantEvent[]=[]
   for await(const event of managedContext(session,async function*(){runners++;yield {type:'end'}}))events.push(event)
   expect({runners,providers,tools}).toEqual({runners:0,providers:0,tools:0})
   expect(events.map(e=>e.type)).toEqual(['guardrail','error','usage','end'])
   expect(events[0]).toEqual(invalidPatterns&&kind==='checkpoint'?{...notice,tool:'outputPatterns',reason:'policy refused',detail:'outputPatterns (run): invalid output guard configuration'}:notice)
   expect(JSON.stringify(events)).not.toContain(marker)
   for(const event of events){expect(event.version).toBe(1);expect(readRunEvent(event)).toEqual(event)}
   expect(events[1]).toMatchObject({type:'error',failure:{code:kind==='policy'||invalidPatterns?'configuration':'stale-state'}})
  }
 })
})
