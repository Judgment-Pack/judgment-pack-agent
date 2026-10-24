import assert from 'node:assert/strict'
import {once} from 'node:events'
import {createRequire} from 'node:module'
import {join,resolve} from 'node:path'
import {PassThrough} from 'node:stream'
import test from 'node:test'
import {deflateSync,gzipSync} from 'node:zlib'

// An isolated old installation can select the same assertions without patching
// the repository's modules. All request fixtures and input bytes are handwritten.
const require=createRequire(process.env.BODY_PARSER_PROBE_ROOT
 ?join(resolve(process.env.BODY_PARSER_PROBE_ROOT),'package.json'):import.meta.url)
const bodyParser=require('body-parser')

function requestFixture(t,contentType='application/json'){
 const req=new PassThrough()
 req.socket=new PassThrough()
 req.complete=false
 req.headers={'content-type':contentType,'content-encoding':'deflate','transfer-encoding':'chunked'}
 req.once('end',()=>{req.complete=true})
 const decoders=[],detached=[]
 const pipe=req.pipe.bind(req),unpipe=req.unpipe.bind(req)
 // A separate body consumer must retain all bytes when only the decoder detaches.
 const observer=new PassThrough(),raw=[]
 observer.on('data',chunk=>raw.push(chunk))
 pipe(observer)
 req.pipe=(destination,...args)=>{decoders.push(destination);return pipe(destination,...args)}
 req.unpipe=destination=>{detached.push(destination);return unpipe(destination)}
 t.after(()=>{
  for(const decoder of decoders){decoder.on('error',()=>{});unpipe(decoder);decoder.destroy()}
  req.destroy();req.socket.destroy();observer.destroy()
 })
 return {req,decoders,detached,observer,raw}
}

for(const [encoding,compress] of [['deflate',deflateSync],['gzip',gzipSync]]){
 test(`body-parser lifecycle: ${encoding} completion detaches only its decoder`,{timeout:5000},async t=>{
  const {req,decoders,detached,observer,raw}=requestFixture(t)
  req.headers['content-encoding']=encoding
  const prefix=Buffer.concat([compress(Buffer.from('{"ok":true}')),Buffer.from([0])])
  const tail=Buffer.from([1,2,3])
  let calls=0
  const parsed=new Promise((resolve,reject)=>{
   const timeout=setTimeout(()=>reject(Error('compressed parser did not complete before request end')),2000)
   bodyParser.json()(req,{},error=>{calls++;clearTimeout(timeout);error?reject(error):resolve()})
  })
  req.write(prefix)
  await parsed
  assert.deepEqual(req.body,{ok:true})
  assert.equal(req.complete,false,'control must complete decoding before the HTTP body finishes')
  assert.equal(decoders.length,1)
  const decoder=decoders[0]
  assert.ok(detached.includes(decoder),'completed decoder must be detached from its request')
  assert.equal(decoder.destroyed,true,'completed decoder must be destroyed')
  assert.ok(decoder.listenerCount('error')>0,'finished decoder must retain a late-error listener')
  // Model a pending decoder error without allowing an uncaught process exception.
  assert.doesNotThrow(()=>decoder.emit('error',Error('fixture late decoder error')))
  const finished=once(observer,'finish'),ended=once(req,'end')
  req.end(tail)
  await Promise.all([finished,ended])
  assert.equal(req.complete,true,'remaining request bytes must be consumed')
  assert.deepEqual(Buffer.concat(raw),Buffer.concat([prefix,tail]),'unrelated body consumer must retain every byte')
  assert.equal(calls,1,'parser completion must be published once')
 })
}

test('body-parser lifecycle: unsupported charset refuses before creating a decoder',t=>{
 const {req,decoders}=requestFixture(t,'application/json; charset=utf-999')
 let calls=0,verified=0,error
 bodyParser.json({verify(){verified++}})(req,{},value=>{calls++;error=value})
 assert.equal(calls,1)
 assert.equal(error?.status,415)
 assert.equal(error?.type,'charset.unsupported')
 assert.equal(verified,0)
 assert.equal(decoders.length,0,'unsupported charset must refuse before a decoder is piped')
})
