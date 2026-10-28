import {canonicalJSON} from './context/chat'
import {sha256} from './records'
import {OPERATION_FAMILIES} from './toolTaxonomy'

/** Routing is a cost/focus projection of an already admitted catalogue, never authority. */
export type RouteMode='off'|'rules'|'auto'
export type RouteSelection={route:'direct'|'agent';tier?:string;skills:string[];tools:'all'|string[];operations:'all'|string[];planning:'offered'|'withheld'}
export type RouteFacts={mode:string;attachments:boolean;artifact:boolean;turnBytes:number;slash?:string}
export type RouteRule={id:string;when:Partial<RouteFacts>&{maxTurnBytes?:number};route:RouteSelection}
export interface RoutingPolicy {mode:RouteMode;tiers:string[];rules?:RouteRule[];classifier?:{labels:Record<string,RouteSelection>;maxOutputTokens:number;timeoutMilliseconds:number}}
export interface RouteCatalogue {tools:{name:string;operation:string;effect?:string;interaction?:string}[];skills:string[];tiers:string[];tier?:string;planning:boolean;native:boolean}
export interface RouteClassifierRecord {binding:string;promptDigest:string;status:'label'|'refused'|'timeout'|'unknown-label'|'unsupported'|'pending';label?:string;usage?:{inputTokens?:number;outputTokens?:number}}
export interface TurnRoute extends RouteSelection {version:1;decidedBy:'host'|'rule'|'classifier'|'default';reason:string;bindingDigest:string;catalogueDigest:string;widened:boolean;widenedFor?:string;widenedInvocation?:{dispatchId:string;requestDigest:string};refusals:{stage:'host'|'rule'|'classifier';reason:string}[];classifier?:RouteClassifierRecord}
const object=(v:unknown):v is Record<string,unknown>=>!!v&&typeof v==='object'&&!Array.isArray(v)
const exact=(v:Record<string,unknown>,keys:string[])=>Object.keys(v).every(k=>keys.includes(k))
const id=(v:unknown):v is string=>typeof v==='string'&&v.length>0&&v.length<=100&&!/[\u0000-\u001f]/.test(v)
const names=(v:unknown):v is string[]=>Array.isArray(v)&&v.length<=128&&v.every(id)&&new Set(v).size===v.length
const digest=(v:unknown)=>typeof v==='string'&&/^sha256:[0-9a-f]{64}$/.test(v)
export const routeDigest=(v:unknown)=>`sha256:${sha256(canonicalJSON(v))}`
export function validRouteSelection(v:unknown):v is RouteSelection{return object(v)&&exact(v,['route','tier','skills','tools','operations','planning'])&&(v.route==='direct'||v.route==='agent')&&(v.tier===undefined||id(v.tier))&&names(v.skills)&&(v.tools==='all'||names(v.tools))&&(v.operations==='all'||names(v.operations)&&v.operations.every(x=>(OPERATION_FAMILIES as readonly string[]).includes(x)))&&(v.planning==='offered'||v.planning==='withheld')}
export function validRoutingPolicy(v:unknown):v is RoutingPolicy{
 if(!object(v)||!exact(v,['mode','tiers','rules','classifier'])||!['off','rules','auto'].includes(String(v.mode))||!names(v.tiers)||!v.tiers.length)return false
 if(v.rules!==undefined&&(!Array.isArray(v.rules)||v.rules.length>64||new Set(v.rules.map(r=>object(r)?r.id:null)).size!==v.rules.length||!v.rules.every(r=>object(r)&&exact(r,['id','when','route'])&&id(r.id)&&object(r.when)&&exact(r.when,['mode','attachments','artifact','turnBytes','maxTurnBytes','slash'])&&Object.entries(r.when).every(([k,x])=>k==='mode'||k==='slash'?id(x):k==='artifact'||k==='attachments'?typeof x==='boolean':Number.isSafeInteger(x)&&Number(x)>=0&&Number(x)<=1048576)&&validRouteSelection(r.route))))return false
 if(v.classifier!==undefined){const c=v.classifier;if(!object(c)||!exact(c,['labels','maxOutputTokens','timeoutMilliseconds'])||!object(c.labels)||Object.keys(c.labels).length<1||Object.keys(c.labels).length>32||!Object.entries(c.labels).every(([k,r])=>/^[a-z][a-z0-9_-]{0,31}$/.test(k)&&validRouteSelection(r))||!Number.isSafeInteger(c.maxOutputTokens)||Number(c.maxOutputTokens)<1||Number(c.maxOutputTokens)>64||!Number.isSafeInteger(c.timeoutMilliseconds)||Number(c.timeoutMilliseconds)<1||Number(c.timeoutMilliseconds)>5000)return false}
 return new TextEncoder().encode(canonicalJSON(v)).length<=65536
}
export function validTurnRoute(v:unknown):v is TurnRoute{
 if(!object(v)||!exact(v,['version','route','tier','skills','tools','operations','planning','decidedBy','reason','bindingDigest','catalogueDigest','widened','widenedFor','widenedInvocation','refusals','classifier'])||v.version!==1||!validRouteSelection(Object.fromEntries(['route','tier','skills','tools','operations','planning'].filter(k=>v[k]!==undefined).map(k=>[k,v[k]])))||!['host','rule','classifier','default'].includes(String(v.decidedBy))||!id(v.reason)||!digest(v.bindingDigest)||!digest(v.catalogueDigest)||typeof v.widened!=='boolean'||(v.widenedFor!==undefined&&(!v.widened||!id(v.widenedFor)))||!Array.isArray(v.refusals)||v.refusals.length>66||!v.refusals.every(r=>object(r)&&exact(r,['stage','reason'])&&['host','rule','classifier'].includes(String(r.stage))&&id(r.reason)))return false
 if(v.widenedInvocation!==undefined){const i=v.widenedInvocation;if(!v.widened||!id(v.widenedFor)||!object(i)||!exact(i,['dispatchId','requestDigest'])||typeof i.dispatchId!=='string'||!i.dispatchId.length||i.dispatchId.length>256||/[\u0000-\u001f]/.test(i.dispatchId)||!digest(i.requestDigest))return false}
 if(v.classifier!==undefined){const c=v.classifier;if(!object(c)||!exact(c,['binding','promptDigest','status','label','usage'])||!id(c.binding)||!digest(c.promptDigest)||!['label','refused','timeout','unknown-label','unsupported','pending'].includes(String(c.status))||(c.label!==undefined&&!id(c.label))||(c.usage!==undefined&&(!object(c.usage)||!exact(c.usage,['inputTokens','outputTokens'])||!Object.values(c.usage).every(x=>Number.isSafeInteger(x)&&Number(x)>=0))))return false}
 return new TextEncoder().encode(canonicalJSON(v)).length<=65536
}
export function fullRoute(c:RouteCatalogue):RouteSelection{return {route:'agent',...(c.tier===undefined?{}:{tier:c.tier}),skills:[],tools:'all',operations:'all',planning:c.planning?'offered':'withheld'}}
export function routeSelectionProblem(s:unknown,c:RouteCatalogue):string|undefined{
 if(!validRouteSelection(s))return 'invalid-selection'
 if(s.tier!==undefined&&!c.tiers.includes(s.tier))return 'tier-not-admitted'
 if(s.skills.some(x=>!c.skills.includes(x)))return 'skill-not-admitted'
 if(s.tools!=='all'&&s.tools.some(x=>!c.tools.some(t=>t.name===x)))return 'tool-not-admitted'
 if(s.operations!=='all'&&s.operations.some(x=>!c.tools.some(t=>t.operation===x)))return 'operation-not-admitted'
 if(s.planning==='offered'&&!c.planning)return 'planner-not-admitted'
 if(s.route==='direct'&&c.native)return 'native-direct-unsupported'
 return undefined
}
export function matchesRouteRule(rule:RouteRule,facts:RouteFacts){return Object.entries(rule.when).every(([k,v])=>k==='maxTurnBytes'?facts.turnBytes<=Number(v):facts[k as keyof RouteFacts]===v)}
/** First three stages have no untrusted authority; a bad candidate records refusal and defaults. */
export function decideTurnRoute(policy:RoutingPolicy,catalogue:RouteCatalogue,facts:RouteFacts,binding:unknown,host?:RouteSelection,classifier?:RouteClassifierRecord):TurnRoute{
 if(!validRoutingPolicy(policy))throw Error('TurnRoutePolicyInvalid')
 const base={version:1 as const,bindingDigest:routeDigest(binding),catalogueDigest:routeDigest(catalogue),widened:false,refusals:[] as TurnRoute['refusals']}
 const finish=(selection:RouteSelection,decidedBy:TurnRoute['decidedBy'],reason:string):TurnRoute=>({...base,...structuredClone(selection),decidedBy,reason,...(classifier?{classifier:structuredClone(classifier)}:{})})
 if(policy.mode==='off')return finish(fullRoute(catalogue),'default','off')
 let selection:RouteSelection|undefined,source:'host'|'rule'|'classifier'='host',reason='host'
 if(host)selection=host
 else{const rule=policy.rules?.find(r=>matchesRouteRule(r,facts));if(rule){selection=rule.route;source='rule';reason=rule.id}
 else if(policy.mode==='auto'&&classifier){source='classifier';reason=classifier.label??classifier.status;if(classifier.status==='label')selection=policy.classifier?.labels[classifier.label!];if(!selection)base.refusals.push({stage:'classifier',reason:classifier.status==='label'?'unknown-label':classifier.status})}}
 if(selection){const problem=routeSelectionProblem(selection,catalogue);if(problem)base.refusals.push({stage:source,reason:problem});else return finish(selection,source,reason)}
 return finish(fullRoute(catalogue),'default',base.refusals.length?'selection-refused':'full')
}
export function routeOffers(route:TurnRoute,c:RouteCatalogue,name:string){const tool=c.tools.find(t=>t.name===name);return !!tool&&(tool.operation==='ask'&&tool.effect==='pure'&&tool.interaction==='question'||route.route!=='direct'&&(route.tools==='all'||route.tools.includes(name))&&(route.operations==='all'||route.operations.includes(tool.operation))&&(name!=='update_plan'||route.planning==='offered'))}
/** Restored data is validated against current admission, never an execution grant. */
export function restoreTurnRoute(value:unknown,c:RouteCatalogue,binding:unknown):TurnRoute{if(!validTurnRoute(value)||value.catalogueDigest!==routeDigest(c)||value.bindingDigest!==routeDigest(binding)||routeSelectionProblem(Object.fromEntries(['route','tier','skills','tools','operations','planning'].filter(k=>value[k as keyof TurnRoute]!==undefined).map(k=>[k,value[k as keyof TurnRoute]])),c))throw Error('TurnRouteIdentityChanged');return structuredClone(value)}
export function widenedTurnRoute(route:TurnRoute,c:RouteCatalogue,name:string):TurnRoute{if(route.widened||routeOffers(route,c,name)||!c.tools.some(t=>t.name===name)&&!c.skills.includes(name))throw Error('TurnRouteWidenRefused');return {...structuredClone(route),...fullRoute(c),widened:true,widenedFor:name}}
