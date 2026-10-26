import {it,expect} from 'vitest'
import vector from '../../../contract/vectors/history/event-projection.json'
import legacy from '../../../contract/fixtures/history/capture.json'
import {readRunCapture} from './metadata'
import {historyJSONBytes} from './codec'
it('reads the shared explicit transcript marker and leaves legacy captures exact',()=>{for(const item of vector.items)expect(readRunCapture(historyJSONBytes(item))).toEqual(item);expect(readRunCapture(historyJSONBytes(legacy.capture))).toEqual(legacy.capture)})
for(const marker of ['',null,true,0,{},[], 'transcript-v2'])it(`refuses invalid event projection marker ${JSON.stringify(marker)}`,()=>{expect(()=>readRunCapture(historyJSONBytes({...vector.items[0],eventProjection:marker}))).toThrow()})
