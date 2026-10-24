import {describe,it,expect} from 'vitest'
import vectors from '../../contract/vectors/proposal/numeric-admission.json'
import {readProposalDocument,parseProposalJSON,ProposalDocumentError,PROPOSAL_SOURCE_BYTES,PROPOSAL_SOURCE_DEPTH} from './proposalNumbers'
import {readProposedPack,extractProposal,PROPOSE_PACK_SCHEMA,PROPOSE_PACK_INSTRUCTIONS} from './engines/contract'

describe('proposal numeric source admission',()=>{
 for(const row of vectors.cases)it(row.name,()=>{
  if('canonical' in row)expect(JSON.stringify(readProposalDocument(row.source))).toBe(row.canonical)
  else {try{readProposalDocument(row.source);expect.fail('source accepted')}catch(cause){expect(cause).toBeInstanceOf(ProposalDocumentError);expect((cause as ProposalDocumentError).kind).toBe(row.refusal)}}
 })
 it('checks the document subtree including escaped envelope keys and duplicate envelopes',()=>{
  expect(()=>extractProposal('```json\n{"propo\\u0073al":{"docu\\u006dent":{"n":9007199254740993}}}\n```')).toThrow('numerical value changes')
  expect(()=>extractProposal('```json\n{"proposal":{"document":{"n":9007199254740993}},"proposal":{"document":{"n":1}}}\n```')).toThrow('numerical value changes')
  expect(extractProposal('```json\n{"metadata":9007199254740993,"proposal":{"document":{"n":1.20}}}\n```').document).toEqual({n:1.2})
  expect(parseProposalJSON('{"a":[9007199254740993],"proposal":{"document":{"n":2}}}',['proposal','document'])).toBeDefined()
 })
 it('bounds UTF-8 bytes, nesting and exponents without lowering the supported proposal hard maximum',()=>{
  const largerThanDefault='{"text":"'+'x'.repeat(70_000)+'"}'
  expect(readProposalDocument(largerThanDefault).text).toHaveLength(70_000)
  expect(()=>readProposalDocument('{"text":"'+'x'.repeat(PROPOSAL_SOURCE_BYTES)+'"}')).toThrow('work bound')
  expect(()=>readProposalDocument('{"text":"'+'é'.repeat(PROPOSAL_SOURCE_BYTES/2)+'"}')).toThrow('work bound')
  expect(()=>readProposalDocument('{"n":'+'['.repeat(PROPOSAL_SOURCE_DEPTH)+'0'+']'.repeat(PROPOSAL_SOURCE_DEPTH)+'}')).toThrow('work bound')
  expect(readProposalDocument('{"n":0e9999999}').n).toBe(0)
  expect(()=>readProposalDocument('[]')).toThrow('document object')
 })
 it('requires the new native source even when legacy values have already become safe numbers or null',()=>{
  for(const document of [{id:'p'},null,{n:1},{n:1.2},{n:0},{n:null},JSON.parse('{"n":9007199254740991.1}')]){
   expect(()=>readProposedPack({document})).toThrow('documentJson')
   expect(()=>readProposedPack({document,documentJson:'{"id":"p"}'})).toThrow('legacy document')
  }
  for(const args of [{},{documentJson:null},{documentJson:''},{documentJson:'[]'}])expect(()=>readProposedPack(args)).toThrow()
  for(const row of vectors.cases){
   if('canonical'in row)expect(JSON.stringify(readProposedPack({documentJson:row.source}).document)).toBe(row.canonical)
   else expect(()=>readProposedPack({documentJson:row.source})).toThrow()
  }
  expect(PROPOSE_PACK_SCHEMA.required).toEqual(['documentJson'])
  expect(PROPOSE_PACK_SCHEMA.properties.documentJson.type).toBe('string')
  expect(PROPOSE_PACK_INSTRUCTIONS).toContain('documentJson')
  expect(()=>readProposedPack({documentJson:'{}',kind:'replace'})).toThrow('Only a new pack')
  expect(()=>readProposedPack({documentJson:'{}',unknowns:[1]})).toThrow('array of strings')
 })
})

it('classifies depth beyond a generic JSON parser limit as a source work bound',()=>{
 const source='{"id":"p","n":'+'['.repeat(10001)+'0'+']'.repeat(10001)+'}'
 try { readProposalDocument(source); expect.fail('deep source admitted') }
 catch(cause) { expect(cause).toBeInstanceOf(ProposalDocumentError); expect((cause as ProposalDocumentError).kind).toBe('work-bound') }
})
