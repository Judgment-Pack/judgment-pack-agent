import {ReplayUnavailable} from './captureCollector'
import {readCanonicalHistoryJSON,HISTORY_OBJECT_BYTES} from './codec'
const object=(v:unknown):v is Record<string,unknown>=>v!==null&&typeof v==='object'&&!Array.isArray(v)
const fail=():never=>{throw new ReplayUnavailable()}
export const nativeBindingsHeader='x-jp-native-tool-bindings'
export function nativeBindings(request:Record<string,unknown>,headers:Record<string,string>):Record<string,string>|undefined{
 const raw=headers[nativeBindingsHeader];if(raw===undefined){if(Array.isArray(request.tools)&&request.tools.some(tool=>object(tool)&&typeof tool.name==='string'&&/^desk_(host|runtime)_/.test(tool.name)))return fail();return}
 const bindings=readCanonicalHistoryJSON(new TextEncoder().encode(raw),HISTORY_OBJECT_BYTES)
 if(!object(bindings)||Object.values(bindings).some(v=>typeof v!=='string'||!v)||new Set(Object.values(bindings)).size!==Object.keys(bindings).length||!Array.isArray(request.tools)||request.tools.length!==Object.keys(bindings).length) return fail()
 const seen=new Set<string>();for(const tool of request.tools){if(!object(tool)||typeof tool.name!=='string'||seen.has(tool.name)||!Object.hasOwn(bindings,tool.name))return fail();seen.add(tool.name)}
 return bindings as Record<string,string>
}
