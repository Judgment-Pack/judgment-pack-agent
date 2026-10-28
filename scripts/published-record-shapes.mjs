// Published v0.10 formats never follow mutable current schemas.
import {readFileSync} from 'node:fs'
import {createHash} from 'node:crypto'
const text=readFileSync('contract/records/published-v010-shapes.json','utf8')
if(createHash('sha256').update(text).digest('hex')!=='bc2c49c0340af12c3f016b772ab90c5a0c834a2667605027d70ef7ac6c438df0')throw Error('Published v0.10 record graph digest differs.')
const source=JSON.parse(text)
if(source.source!=='e00d2b11d11af4dabb0a4de6c1a5e7322bd8527e'||JSON.stringify(source.versions)!=='{"checkpoint":2,"event":1,"policy":1}')throw Error('Published v0.10 record graph identity differs.')
export const published=source.graph
