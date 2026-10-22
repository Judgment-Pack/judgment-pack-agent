import {readFile} from 'node:fs/promises'
import {resolve} from 'node:path'
import {expect,it} from 'vitest'

it('keeps packaged contract copies byte-identical to their originals',async()=>{
 const [original,packaged]=await Promise.all([
  readFile(resolve('../..','contract/schemas/corpus-entry.json')),
  readFile(resolve('contract/schemas/corpus-entry.json'))
 ])
 expect(packaged).toEqual(original)
})
