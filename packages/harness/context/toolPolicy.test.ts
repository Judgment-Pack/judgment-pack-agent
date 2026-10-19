import {expect,it} from 'vitest'
import {runtimeToolContextPolicy} from './toolPolicy'
it('retains only immutable Dialect references, never cached checks of mutable artifacts',()=>{
 for(const name of ['get_dialect_contract','get_dialect_schema','get_dialect_reference'])expect(runtimeToolContextPolicy(name)).toEqual({immutable:true,reference:true})
 for(const name of ['dialect_validate','dialect_check','dialect_digest','dialect_canonicalize','dialect_init','read_dialect'])expect(runtimeToolContextPolicy(name)).toEqual({})
})
