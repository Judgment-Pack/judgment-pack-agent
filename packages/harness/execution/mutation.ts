import {canonicalJSON} from '../context/chat'
import type {ExecutionCheckpoint} from './state'

/** A patch replaces named members atomically. Undefined removes an optional member. */
export class CheckpointIncrement {constructor(readonly amount:number){if(!Number.isSafeInteger(amount))throw Error('Invalid checkpoint increment')}}
export const CHECKPOINT_MEMBERS=["schema","version","digest","retainUntil","id","requestKey","phase","status","unit","used","limit","grants","plan","explanation","operations","completions","context","reason","testCalls","delegation","handoff","approval","prompts","sources","skills","critic","policy","crossProvider","compaction","memories","draft","outputGuard","records","sourceIdentities","steers","usage","failover","childUsage"] as const
const members=new Set<string>(CHECKPOINT_MEMBERS),counters=new Set(['used','limit','testCalls'])
export type CheckpointPatch = {[K in keyof ExecutionCheckpoint]?: ExecutionCheckpoint[K] | (K extends 'used'|'limit'|'testCalls' ? CheckpointIncrement : never)}
const revisions=new WeakMap<ExecutionCheckpoint,number>()
export function mutateCheckpoint(target:ExecutionCheckpoint,patch:CheckpointPatch,validate?:(candidate:ExecutionCheckpoint)=>void):boolean {
 const candidate={...target}
 for(const key of Object.keys(patch) as (keyof ExecutionCheckpoint)[]){
  if(!members.has(key))throw Error('Unknown checkpoint member: '+key)
  if(patch[key]===undefined)delete candidate[key]
  else if(patch[key] instanceof CheckpointIncrement){
   const before=candidate[key]??0,amount=(patch[key] as CheckpointIncrement).amount
   if(!counters.has(key)||typeof before!=='number'||!Number.isSafeInteger(amount)||!Number.isSafeInteger(before+amount))throw Error('Invalid checkpoint counter')
   Object.assign(candidate,{[key]:before+amount})
  }else Object.assign(candidate,{[key]:structuredClone(patch[key])})
 }
 validate?.(structuredClone(candidate))
 if(canonicalJSON(candidate)===canonicalJSON(target))return false
 for(const key of Object.keys(target) as (keyof ExecutionCheckpoint)[])delete target[key]
 Object.defineProperties(target,Object.getOwnPropertyDescriptors(candidate))
 revisions.set(target,(revisions.get(target)??0)+1)
 return true
}
export class CheckpointMutation {
 #value:ExecutionCheckpoint
 constructor(value?:ExecutionCheckpoint){this.#value=structuredClone(value??{} as ExecutionCheckpoint);if(value)revisions.set(this.#value,1)}
 get revision(){return revisions.get(this.#value)??0}
 read():ExecutionCheckpoint {return structuredClone(this.#value)}
 apply(patch:CheckpointPatch,validate?:(candidate:ExecutionCheckpoint)=>void):boolean {return mutateCheckpoint(this.#value,patch,validate)}
}
