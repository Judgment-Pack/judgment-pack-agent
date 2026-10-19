/** Host-owned verify/correct loop shared by artifact adapters. Never saves, grants
 * permission, retries transport, changes expectations, or allocates model budget. */
export type CorrectionResult<T,C>={state:'ready'|'limit'|'stalled'|'answered';candidate:T;check:C;corrections:number}
export interface CorrectionPorts<T,C> {
 initial:T
 maxCorrections:number
 fingerprint:(candidate:T)=>string
 check:(candidate:T,signal:AbortSignal)=>Promise<C>
 passed:(check:C)=>boolean
 correct:(candidate:T,check:C,signal:AbortSignal)=>Promise<T|null>
 /** Persist exact candidate/check before another model request is possible. */
 checkpoint:(candidate:T,check:C,corrections:number)=>Promise<void>
}
export async function correctArtifact<T,C>(ports:CorrectionPorts<T,C>,signal:AbortSignal):Promise<CorrectionResult<T,C>> {
 if(!Number.isSafeInteger(ports.maxCorrections)||ports.maxCorrections<0)throw new Error('Invalid correction limit.')
 let candidate=ports.initial
 const seen=new Set([ports.fingerprint(candidate)])
 for(let corrections=0;;corrections++) {
  signal.throwIfAborted()
  const check=await ports.check(candidate,signal)
  signal.throwIfAborted()
  await ports.checkpoint(candidate,check,corrections)
  signal.throwIfAborted()
  const result={candidate,check,corrections}
  if(ports.passed(check))return {...result,state:'ready'}
  if(corrections>=ports.maxCorrections)return {...result,state:'limit'}
  const next=await ports.correct(candidate,check,signal)
  signal.throwIfAborted()
  if(next===null)return {...result,state:'answered'}
  const key=ports.fingerprint(next)
  if(seen.has(key))return {...result,state:'stalled'}
  seen.add(key);candidate=next
 }
}
