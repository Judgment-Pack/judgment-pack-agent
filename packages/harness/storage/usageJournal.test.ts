import {describe,it,expect} from 'vitest'
import vector from '../../../contract/vectors/usage-budget/admission.json'
import {MemoryUsageBudgetStore,UsageBudgetUnknown,UsageBudgetIdentityChanged} from './usageBudget'
import {UsageJournalOwner,usageTotals,validUsageJournal,type UsageDispatchFacts} from './usageJournal'
const facts=():UsageDispatchFacts=>({execution:'run-1',role:'author',source:'provider-1',tool:'model-1',descriptor:JSON.stringify({body:'{"max_tokens":5}'}),kind:'model',family:'openai-compatible',connection:'connection-1',connectionRevision:'revision-1',model:'model-1',request:new TextEncoder().encode('{"max_tokens":5}'),encodedOutputMaximum:5})
const bounder=async()=>({inputTokens:10,outputTokens:5})
const pick=(f:UsageDispatchFacts)=>({execution:f.execution,role:f.role,source:f.source,tool:f.tool,descriptor:f.descriptor,kind:f.kind})
describe('original writer usage journal owner',()=>{
 it('consumes a fresh exact ticket once and never counts reservations as reported usage',async()=>{
  const owner=new UsageJournalOwner({store:new MemoryUsageBudgetStore(vector.resource),scope:vector.scope,bounder},'root-1'),f=facts()
  const token=await owner.reserve('dispatch-1',f)
  expect(usageTotals(owner.snapshot()).inputTokens).toEqual({known:0,complete:false})
  expect(()=>owner.consume(token,{...pick(f),descriptor:'different'})).toThrow(UsageBudgetIdentityChanged)
  expect(owner.consume(token,pick(f))).toBe('dispatch-1')
  expect(()=>owner.consume(token,pick(f))).toThrow(UsageBudgetIdentityChanged)
  await owner.observe('dispatch-1',{modelRequests:1,inputTokens:4},vector.evidence)
  expect(validUsageJournal(owner.snapshot())).toBe(true)
  for(const mutate of [(row:any)=>{row.observed.modelRequests=0},(row:any)=>{row.observed=undefined},(row:any)=>{row.settlement.observed.inputTokens=3}]){const bad=owner.snapshot();mutate(bad.entries[0]);expect(validUsageJournal(bad)).toBe(false)}
  expect(usageTotals(owner.snapshot())).toEqual({modelRequests:{known:1,complete:true},providerInvocations:{known:0,complete:true},inputTokens:{known:4,complete:true},outputTokens:{known:0,complete:false}})
 })
 it('retains a lost Reserve acknowledgment and refuses a physical dispatch on replay',async()=>{
  let once=false,dispatches=0
  const base=new MemoryUsageBudgetStore(vector.resource,{afterChange(){if(!once){once=true;throw Error('lost acknowledgment')}}}),owner=new UsageJournalOwner({store:base,scope:vector.scope,bounder},'root-1')
  await expect(owner.reserve('dispatch-1',facts())).rejects.toThrow('lost acknowledgment')
  const restored=new UsageJournalOwner({store:base,scope:vector.scope,bounder},'root-1',owner.snapshot())
  try{await restored.reserve('dispatch-1',facts());dispatches++}catch(error){expect(error).toBeInstanceOf(UsageBudgetUnknown)}
  expect(dispatches).toBe(0);expect(owner.snapshot().entries[0]!.state).toBe('unknown')
 })
 it('refuses retained created=false even if a host lost the original writer journal',async()=>{
  const base=new MemoryUsageBudgetStore(vector.resource),first=new UsageJournalOwner({store:base,scope:vector.scope,bounder},'root-1')
  await first.reserve('dispatch-1',facts())
  let dispatches=0
  const second=new UsageJournalOwner({store:base,scope:vector.scope,bounder},'root-1')
  try{await second.reserve('dispatch-1',facts());dispatches++}catch(error){expect(error).toBeInstanceOf(UsageBudgetUnknown)}
  expect(dispatches).toBe(0)
 })
 it('does not accept a ticket from another original owner or a saved reservation',async()=>{
  const store=new MemoryUsageBudgetStore(vector.resource),owner=new UsageJournalOwner({store,scope:vector.scope,bounder},'root-1'),f=facts(),token=await owner.reserve('dispatch-1',f)
  const other=new UsageJournalOwner({store,scope:vector.scope,bounder},'root-1',owner.snapshot())
  expect(()=>other.consume(token,pick(f))).toThrow(UsageBudgetIdentityChanged)
  expect(()=>owner.consume(owner.snapshot().entries[0]!,pick(f))).toThrow(UsageBudgetIdentityChanged)
 })
})

it('reconciles retained reservations through the same host resource and refuses replacement state',async()=>{
 const store=new MemoryUsageBudgetStore(vector.resource),owner=new UsageJournalOwner({store,scope:vector.scope,bounder},'root-1'),f=facts(),ticket=await owner.reserve('dispatch-1',f)
 owner.consume(ticket,pick(f));await owner.observe('dispatch-1',{modelRequests:1,inputTokens:4},vector.evidence)
 const valid=new UsageJournalOwner({store,scope:vector.scope,bounder},'root-1',owner.snapshot());await valid.reconcile()
 const replaced=new UsageJournalOwner({store:new MemoryUsageBudgetStore(vector.resource),scope:vector.scope,bounder},'root-1',owner.snapshot())
 await expect(replaced.reconcile()).rejects.toBeInstanceOf(UsageBudgetIdentityChanged)
 expect(()=>new UsageJournalOwner({store,scope:{...vector.scope,revision:'changed'},bounder},'root-1',owner.snapshot())).toThrow(UsageBudgetIdentityChanged)
})
