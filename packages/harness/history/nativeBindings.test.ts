import {it,expect} from 'vitest'
import {nativeBindings,nativeBindingsHeader} from './nativeBindings'
import {canonicalJSON} from '../context/chat'
const request={tools:[{name:'desk_host_0',description:'Ignored: injection.'},{name:'desk_host_1',description:'Ignored.'}]}
const mapping={'desk_host_0':'lookup:scoped','desk_host_1':'second'}
it('uses the exact original alias mapping including colon names without reading descriptions',()=>{expect(nativeBindings(request,{[nativeBindingsHeader]:canonicalJSON(mapping)})).toEqual(mapping);expect(nativeBindings({tools:[{name:'lookup'}]},{})).toBeUndefined()})
for(const fault of ['missing','partial','extra','duplicate-name','duplicate-alias','noncanonical','malformed','non-string'] as const)it(`refuses native alias metadata ${fault}`,()=>{
 const headers:Record<string,string>={[nativeBindingsHeader]:canonicalJSON(mapping)},r=structuredClone(request)
 if(fault==='missing')delete headers[nativeBindingsHeader]
 if(fault==='partial')headers[nativeBindingsHeader]=canonicalJSON({'desk_host_0':'lookup'})
 if(fault==='extra')headers[nativeBindingsHeader]=canonicalJSON({...mapping,extra:'foreign'})
 if(fault==='duplicate-name')headers[nativeBindingsHeader]=canonicalJSON({'desk_host_0':'lookup','desk_host_1':'lookup'})
 if(fault==='duplicate-alias')r.tools[1]!.name='desk_host_0'
 if(fault==='noncanonical')headers[nativeBindingsHeader]='{ "desk_host_0":"lookup:scoped","desk_host_1":"second" }'
 if(fault==='malformed')headers[nativeBindingsHeader]='{'
 if(fault==='non-string')headers[nativeBindingsHeader]='{"desk_host_0":1,"desk_host_1":"second"}'
 expect(()=>nativeBindings(r,headers)).toThrow()
})
