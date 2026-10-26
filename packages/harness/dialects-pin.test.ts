import {expect,it} from 'vitest'
import fixture from '../../contract/testdata/dialect-authoring/authoring.json'
import superseded from '../../contract/testdata/dialect-authoring/superseded-prototype-identity.json'
import {DIALECT_RETAINED_COLUMN_DRAFT} from './dialects'
import {dialectFixture} from './testing/dialects'

it('uses the reviewed Draft tuple and refuses the superseded prototype before Runtime',async()=>{
 expect(DIALECT_RETAINED_COLUMN_DRAFT).toEqual(fixture.conversionContract)
 const x=dialectFixture();x.ports.conversionContract=superseded
 await expect(x.propose(await x.read())).rejects.toThrow('exact package-supported experimental Draft opt-in')
 expect(x.state.calls.includes('get_dialect_contract')).toBe(false)
 expect(x.state.calls.includes('preview')).toBe(false)
 expect(x.state.drafts.length).toBeGreaterThan(0);expect(x.state.retained).toHaveLength(0)
})
