import {describe,it,expect} from 'vitest'
import vectors from '../../contract/fixtures/turn-route/decision.json'
import {decideTurnRoute,restoreTurnRoute,widenedTurnRoute,validRoutingPolicy,validTurnRoute,routeOffers,type RoutingPolicy,type RouteCatalogue,type RouteFacts,type RouteSelection,type RouteClassifierRecord} from './turnRoute'
describe('turn route handwritten parity',()=>{
 for(const row of vectors.cases)it(row.id,()=>{
  const input=row.input as unknown as {policy:RoutingPolicy;catalogue:RouteCatalogue;facts:RouteFacts;binding:unknown;host?:RouteSelection;classifier?:RouteClassifierRecord}
  const result=decideTurnRoute(input.policy,input.catalogue,input.facts,input.binding,input.host,input.classifier)
  expect(result).toMatchObject({decidedBy:row.expected.decidedBy,reason:row.expected.reason,route:row.expected.route})
  expect(restoreTurnRoute(JSON.parse(JSON.stringify(result)),input.catalogue,input.binding)).toEqual(result)
  expect(()=>restoreTurnRoute(result,input.catalogue,{other:'run'})).toThrow('TurnRouteIdentityChanged')
  expect(()=>widenedTurnRoute(result,input.catalogue,'undeclared')).toThrow()
  if('widen'in row.expected&&row.expected.widen!==undefined){expect(routeOffers(result,input.catalogue,row.expected.widen)).toBe(false);const wide=widenedTurnRoute(result,input.catalogue,row.expected.widen);expect(wide.widened).toBe(true);expect(wide.route).toBe('agent');expect(wide.tools).toBe('all');expect(()=>widenedTurnRoute(wide,input.catalogue,'mutate')).toThrow('TurnRouteWidenRefused');expect(()=>widenedTurnRoute(wide,input.catalogue,'declared')).toThrow('TurnRouteWidenRefused')}
 })
 it('rejects unknown and unbounded classifier/rule declarations',()=>{const p=vectors.cases[0]!.input.policy;expect(validRoutingPolicy({...p,classifier:{...p.classifier,timeoutMilliseconds:5001}})).toBe(false);expect(validRoutingPolicy({...p,rules:[...p.rules,...p.rules]})).toBe(false);expect(validRoutingPolicy({...p,unknown:true})).toBe(false)})
})

it('keeps only actual admitted ask/pure tools through direct and narrow routes',()=>{
 const input=vectors.cases[0]!.input as unknown as {policy:RoutingPolicy;catalogue:RouteCatalogue;facts:RouteFacts;binding:unknown}
 const c={...input.catalogue,tools:[...input.catalogue.tools,{name:'ask_user',operation:'ask',effect:'pure',interaction:'question'},{name:'unsafe_ask',operation:'ask',effect:'write'}]}
 const r=decideTurnRoute(input.policy,c,input.facts,input.binding,{route:'direct',skills:[],tools:[],operations:[],planning:'withheld'})
 expect(routeOffers(r,c,'ask_user')).toBe(true);expect(routeOffers(r,c,'unsafe_ask')).toBe(false);expect(routeOffers(r,c,'undeclared')).toBe(false)
})
it('validates widened invocation disposition without treating a marker as authority',()=>{
 const input=vectors.cases[0]!.input as unknown as {policy:RoutingPolicy;catalogue:RouteCatalogue;facts:RouteFacts;binding:unknown}
 const r=decideTurnRoute(input.policy,input.catalogue,input.facts,input.binding), marker={dispatchId:'dispatch-original',requestDigest:'sha256:'+'a'.repeat(64)}
 expect(validTurnRoute({...r,widenedInvocation:marker})).toBe(false)
 const wide={...r,widened:true,widenedFor:'lookup',widenedInvocation:marker}
 expect(validTurnRoute(wide)).toBe(true)
 for(const altered of [{...marker,requestDigest:'bad'},{...marker,dispatchId:''},{...marker,extra:true}])expect(validTurnRoute({...wide,widenedInvocation:altered})).toBe(false)
 expect(()=>restoreTurnRoute(wide,input.catalogue,{other:'execution'})).toThrow('TurnRouteIdentityChanged')
})
