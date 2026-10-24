import assert from 'node:assert/strict'
import {resolve} from 'node:path'
import test from 'node:test'
import {fileURLToPath} from 'node:url'
import {inventoryUndici,requireFixedUndici,undiciBehaviorTests} from '../packages/harness/scripts/undici-controls.mjs'

const root=process.env.UNDICI_PROBE_ROOT??fileURLToPath(new URL('..',import.meta.url))
const inventory=inventoryUndici(resolve(root))
for(const copy of inventory.copies)undiciBehaviorTests(test,copy)

test('Undici graph: all installed runtime and dev copies have compatible fixed resolutions',()=>requireFixedUndici(inventory))
test('Undici graph: a fixed root cannot hide an affected nested copy',()=>{
 assert.throws(()=>requireFixedUndici({copies:[{location:'root',version:'8.12.2'},{location:'nested',version:'7.29.0'}],owners:[]}),/unverified Undici copy/)
})
test('Undici graph: a fixed incompatible major cannot satisfy an owner',()=>{
 assert.throws(()=>requireFixedUndici({copies:[{location:'root',version:'8.12.2'}],owners:[{owner:'fixture-provider',range:'^7.29.0',undiciVersion:'8.12.2'}]}),/incompatible Undici owner/)
})
