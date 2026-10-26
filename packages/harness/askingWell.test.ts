import {describe,it,expect} from 'vitest'
import {askingWellSkill,ASKING_WELL_PIN,selectAskingWell,requireAskingWell} from './askingWell'
import {declareSkills,skillDigest,catalogueSkills} from './skills'
import {CONTEXT_DEFAULTS} from './context/policy'
import {questionTool} from './questions'
import vector from '../../contract/testdata/asking-well/scenario.json'
const taxonomies=new Map([['ask_user',questionTool.taxonomy!]])
const admitted=async(skills=selectAskingWell(undefined,[questionTool]))=>declareSkills(skills,['ask_user'],CONTEXT_DEFAULTS,[],taxonomies)
describe('asking-well admission',()=>{
 it('pins actual bundle and dependency taxonomy and derives ask/pure',async()=>{
  const skill=askingWellSkill();expect(skill).toEqual(vector.skill);expect(skill.kind).toBe('method');expect(skill.tools).toEqual(['ask_user'])
  expect(ASKING_WELL_PIN).toBe(vector.pin);expect(await skillDigest(skill,taxonomies)).toBe(vector.pin)
  expect((await admitted()).skills[0]?.profile).toEqual(vector.profile)
  expect(await skillDigest({...skill,instructions:skill.instructions+' changed'},taxonomies)).not.toBe(vector.pin)
  expect(await skillDigest(skill,new Map([['ask_user',{operation:'read',effect:'read'}]]))).not.toBe(vector.pin)
 })
 it('retains host replacement provenance and refuses wrong kind duplicate and dependency',async()=>{
  const replacement={...askingWellSkill(),id:'project-ask',source:{kind:'custom' as const,id:'project',trust:'person' as const,version:'2'},instructions:'Use project context before asking.'}
  replacement.pinnedDigest=await skillDigest(replacement,taxonomies)
  const chosen=selectAskingWell([replacement],[questionTool]);expect(chosen).toEqual([replacement])
  const declared=await admitted(chosen);requireAskingWell(declared,[questionTool],CONTEXT_DEFAULTS,'test');expect(declared.skills[0]?.source.trust).toBe('person')
  expect(()=>selectAskingWell([{...replacement,kind:'knowledge'}],[questionTool])).toThrow('asking-well')
  expect(()=>selectAskingWell([replacement,replacement],[questionTool])).toThrow('asking-well')
  expect(()=>selectAskingWell([{...replacement,tools:[]}],[questionTool])).toThrow('asking-well')
 })
 it('does not insert from conversation flags or absent actual question offer',()=>{
  expect(selectAskingWell(undefined,[])).toBeUndefined()
  expect(selectAskingWell(undefined,[{...questionTool,interaction:undefined}])).toBeUndefined()
 })
 it('refuses stale pins and exhausted normal byte/count bounds without fallback',async()=>{
  const stale={...askingWellSkill(),pinnedDigest:'sha256:'+'0'.repeat(64)}
  const declared=await admitted([stale]);expect(declared.skills).toHaveLength(0)
  expect(()=>requireAskingWell(declared,[questionTool],CONTEXT_DEFAULTS,'test')).toThrow('asking-well')
  const normal=await admitted()
  for(const bounds of [{...CONTEXT_DEFAULTS,cataloguedSkills:0},{...CONTEXT_DEFAULTS,skillsCatalogueBytes:0}])expect(()=>requireAskingWell(normal,[questionTool],bounds,'test')).toThrow('asking-well')
  const bytes=await declareSkills(selectAskingWell(undefined,[questionTool]),['ask_user'],{skillBytes:1},[],taxonomies)
  expect(()=>requireAskingWell(bytes,[questionTool],CONTEXT_DEFAULTS,'test')).toThrow('asking-well')
  const unknown=await declareSkills([askingWellSkill()],[],CONTEXT_DEFAULTS,[],taxonomies)
  expect(()=>requireAskingWell(unknown,[questionTool],CONTEXT_DEFAULTS,'test')).toThrow('asking-well')
  expect(catalogueSkills(normal.skills,CONTEXT_DEFAULTS,'test').catalogued).toHaveLength(1)
 })
})
