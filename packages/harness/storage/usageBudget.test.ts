import {describe,it,expect} from 'vitest'
import vector from '../../../contract/vectors/usage-budget/admission.json'
import {MemoryUsageBudgetStore,UsageBudgetExceeded,UsageBudgetUnknown,type UsageReservation,type UsageAmounts} from './usageBudget'
import {StorageCommitMismatch} from './common'
const first=()=>structuredClone(vector.first) as UsageReservation

describe('shared atomic usage budget capability',()=>{
 it('admits under/at, keeps missing output reserved and refuses a sibling beyond the limit',async()=>{
  const store=new MemoryUsageBudgetStore(vector.resource),request=first(),fresh=await store.reserve(request),receipt=fresh.receipt
  expect(fresh.created).toBe(true)
  const partial=await store.settle({scope:vector.scope,id:request.id,reservationDigest:receipt.reservationDigest,commitId:'reported-1',evidence:vector.evidence,kind:'reported',observed:vector.reported})
  expect(partial.state).toBe('partial');expect(partial.observed.outputTokens).toBeUndefined()
  const next={...first(),id:'request-2',root:'root-2',upper:vector.secondUpper}
  await store.reserve(next)
  await expect(store.reserve({...first(),id:'request-3'})).rejects.toMatchObject({name:'UsageBudgetExceeded',dimension:'modelRequests'})
  const retained=await store.reserve(request)
  expect(retained.created).toBe(false);expect(retained.receipt).toEqual(receipt)
  await expect(store.reserve({...request,requestDigest:vector.evidence})).rejects.toBeInstanceOf(StorageCommitMismatch)
  const head=(await store.readReservation(vector.scope,request.id))!;head.request.upper.outputTokens=0
  expect((await store.readReservation(vector.scope,request.id))!.request.upper.outputTokens).toBe(5)
 })
 it('atomically includes externally committed usage and all concurrent sibling reservations',async()=>{
  const scope={...vector.scope,limits:{modelRequests:5}},store=new MemoryUsageBudgetStore(vector.resource,{seeds:[{scope,committed:{modelRequests:3}}]})
  const results=await Promise.allSettled(Array.from({length:10},(_,index)=>store.reserve({...first(),scope,id:String(index),root:String(index),upper:{modelRequests:1}})))
  expect(results.filter(result=>result.status==='fulfilled')).toHaveLength(2)
  for(const result of results)if(result.status==='rejected')expect(result.reason).toBeInstanceOf(UsageBudgetExceeded)
 })
 it('retains a lost acknowledgment, never invents missing external tokens, and separates no admission',async()=>{
  let once=false
  const lost=Error('fixture lost budget acknowledgment'),store=new MemoryUsageBudgetStore(vector.resource,{afterChange(){if(!once){once=true;throw lost}}})
  await expect(store.reserve(first())).rejects.toBe(lost)
  const head=(await store.readReservation(vector.scope,vector.first.id))!
  expect(head.receipt.state).toBe('reserved')
  expect((await store.reserve(first())).created).toBe(false)
  const next={...first(),id:'other',upper:{modelRequests:1,inputTokens:11,outputTokens:5}}
  await expect(store.reserve(next)).rejects.toBeInstanceOf(UsageBudgetExceeded)
  await store.settle({scope:vector.scope,id:vector.first.id,reservationDigest:head.receipt.reservationDigest,commitId:'not-admitted',evidence:vector.evidence,kind:'never-admitted'})
  await store.reserve(next)
  const unknown=new MemoryUsageBudgetStore(vector.resource,{seeds:[{scope:vector.scope,committed:{modelRequests:0} as UsageAmounts}]})
  await expect(unknown.reserve(first())).rejects.toBeInstanceOf(UsageBudgetUnknown)
 })
})
