import {AIFailureError,failure} from '../failures'
/** Live host capability, never part of a request, checkpoint or event. */
export interface ProviderRelease {
 readonly invocation:object
 /** Host calls this only after the specific provider invocation released its account. */
 acknowledge(invocation:object):boolean
}
export class ProviderReleaseUnknown extends AIFailureError {
 constructor(){super({...failure('storage','agent','unknown'),detail:'ProviderReleaseUnknown: provider account release was not acknowledged for this invocation.'});this.name='ProviderReleaseUnknown'}
}
export function providerReleaseAttempt():{capability:ProviderRelease;finish:()=>boolean}{
 const invocation=Object.freeze(Object.create(null)) as object
 let live=true,acknowledged=false,invalid=false
 const capability=Object.freeze({invocation,acknowledge(token:object){if(!live||token!==invocation||acknowledged){invalid=true;return false};acknowledged=true;return true}})
 return {capability,finish(){live=false;return acknowledged&&!invalid}}
}
