// Published v0.10 and v0.12 formats never follow mutable current schemas.
import {readFileSync} from 'node:fs'
import {createHash} from 'node:crypto'
const text=readFileSync('contract/records/published-v010-shapes.json','utf8')
if(createHash('sha256').update(text).digest('hex')!=='bc2c49c0340af12c3f016b772ab90c5a0c834a2667605027d70ef7ac6c438df0')throw Error('Published v0.10 record graph digest differs.')
const source=JSON.parse(text)
if(source.source!=='e00d2b11d11af4dabb0a4de6c1a5e7322bd8527e'||JSON.stringify(source.versions)!=='{"checkpoint":2,"event":1,"policy":1}')throw Error('Published v0.10 record graph identity differs.')
export const published=source.graph
// ADR-0029: checkpoint 3 and event 2, current until checkpoint 4 and event 3 carried the principal, frozen at v0.12.0.
const text012=readFileSync('contract/records/published-v012-shapes.json','utf8')
if(createHash('sha256').update(text012).digest('hex')!=='c23e27841d57f326ec65f68fb5772efec85d5f6e4c3fbb7c9fa9b64882de17bf')throw Error('Published v0.12 record graph digest differs.')
const source012=JSON.parse(text012)
if(source012.source!=='28e2099dc1bc0a57ac94997280696b131ccf3eb2'||JSON.stringify(source012.versions)!=='{"checkpoint":3,"event":2,"policy":2}')throw Error('Published v0.12 record graph identity differs.')
export const published012=source012.graph
