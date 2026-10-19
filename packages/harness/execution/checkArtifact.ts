import {correctArtifact} from './correctArtifact'

/** A host-tool proposal is one attempt in the existing managed agent turn.
 * Retain it before checking, and retain findings before returning corrective
 * guidance. Further attempts use that turn's existing tools and allowance.
 * This model-free check never starts a nested session or grants a write. */
export async function checkArtifact<T,C>(ports:{
 candidate:T
 fingerprint:(candidate:T)=>string
 retain:(candidate:T,check?:C)=>Promise<void>
 check:(candidate:T,signal:AbortSignal)=>Promise<C>
 passed:(check:C)=>boolean
},signal:AbortSignal):Promise<C>{
 signal.throwIfAborted()
 await ports.retain(ports.candidate)
 const result=await correctArtifact({initial:ports.candidate,maxCorrections:0,
  fingerprint:ports.fingerprint,check:ports.check,passed:ports.passed,
  checkpoint:(candidate,check)=>ports.retain(candidate,check),correct:async()=>null,
 },signal)
 return result.check
}
