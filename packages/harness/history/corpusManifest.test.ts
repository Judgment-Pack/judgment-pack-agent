import {it,expect,vi} from 'vitest'
import {copyCorpusManifestEnvelope,copyHistoryEnvelope} from './view'
it('copies resolved finite manifest policy fractions while keeping transport envelopes integral',()=>{
 const manifest={id:'original-live',policy:{'pace.warnAt':{value:0.8,layer:'table',source:'layer'}}},copy=copyCorpusManifestEnvelope(manifest) as typeof manifest
 expect(copy).toEqual(manifest);expect(copy).not.toBe(manifest);expect(copy.policy).not.toBe(manifest.policy);expect(()=>copyHistoryEnvelope(manifest)).toThrow()
 for(const value of [NaN,Infinity,-Infinity])expect(()=>copyCorpusManifestEnvelope({policy:{value}})).toThrow()
})
it('refuses executable/prototype manifest material without running getters or toJSON',()=>{
 const getter=vi.fn(()=>0.8),toJSON=vi.fn(()=>({policy:{value:0.8}})),input={};Object.defineProperty(input,'policy',{enumerable:true,get:getter})
 expect(()=>copyCorpusManifestEnvelope(input)).toThrow();expect(getter).not.toHaveBeenCalled()
 expect(()=>copyCorpusManifestEnvelope({toJSON})).toThrow();expect(toJSON).not.toHaveBeenCalled()
 expect(()=>copyCorpusManifestEnvelope(Object.create({policy:{value:0.8}}))).toThrow()
 for(const value of [Object.assign({}, {[Symbol('policy')]:0.8}),{policy:()=>0.8},[,,]])expect(()=>copyCorpusManifestEnvelope(value)).toThrow()
})
