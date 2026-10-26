import {it,expect,vi} from 'vitest'
import {managedContext} from '../context/managed'
import {MemoryRunStore} from '../storage/run'
import {MemoryUsageBudgetStore} from '../storage/usageBudget'
import {usageTotals} from '../storage/usageJournal'
import {readExecution} from '../execution/state'
import {projectRunReport,readRunReport,canonicalRunReport} from '../runReport'
import type {AssistantSession} from '../engine'
it('keeps default report1 and derives report2 totals from an actual configured journal',async()=>{
 for(const mode of ['legacy','budget'] as const){
  const store=new MemoryRunStore(),budget=new MemoryUsageBudgetStore('project'),session:AssistantSession={prompt:'Work',testPrompt:'',tools:[],hostTools:[],callTool:vi.fn(async()=>({content:[]})),model:{family:'openai-compatible',model:'fixture',call:vi.fn()},signal:new AbortController().signal,context:{currentRequest:{message:'Work',input:'Work'},runStore:{store,id:mode},...(mode==='budget'?{usageBudget:{store:budget,scope:{resource:'project',id:'scope',revision:'policy',limits:{modelRequests:1}},bounder:async()=>({inputTokens:1,outputTokens:1})}}:{})}}
  for await(const _ of managedContext(session,async function*(){yield {type:'end'}})){}
  const head=(await store.load(mode))!,before=new Uint8Array(head.checkpoint!),checkpoint=readExecution(JSON.parse(new TextDecoder().decode(before)))!,report=projectRunReport(checkpoint)
  expect(report.version).toBe(mode==='legacy'?1:2);expect(report.checkpointVersion).toBe(mode==='legacy'?2:3)
  if(mode==='budget'){expect(checkpoint.usageBudget).toBeDefined();expect(report).toHaveProperty('usageTotals',usageTotals(checkpoint.usageBudget!))}
  else{expect(checkpoint.usageBudget).toBeUndefined();expect(Object.hasOwn(report,'usageTotals')).toBe(false)}
  expect(canonicalRunReport(readRunReport(canonicalRunReport(report)))).toBe(canonicalRunReport(report));expect((await store.load(mode))!.checkpoint).toEqual(before);expect(session.model.call).not.toHaveBeenCalled()
 }
})
