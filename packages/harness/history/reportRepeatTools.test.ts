import {expect,it} from 'vitest'
import vectors from '../../../contract/fixtures/report/repeat-tools.json'
import {projectRunReport,canonicalRunReport} from '../runReport'
import {readRunReport,readRunReportDisplay} from '../runReportReader'
import {readExecution} from '../execution/state'
it('projects only actual enabled repeat summary and three policy provenance members',()=>{
 const cp=readExecution(vectors.checkpoint)!,before=JSON.stringify(cp),report=projectRunReport(cp)
 expect(report.version).toBe(2);if(report.version!==2)throw Error('report2 required')
 expect(report.repeatTools).toEqual(vectors.repeatTools);expect(canonicalRunReport(report)).not.toContain('keyDigest');expect(canonicalRunReport(report)).not.toContain('PRIVATE-POLICY');expect(JSON.stringify(cp)).toBe(before)
 expect(readRunReport(report)).toEqual(report);expect(readRunReportDisplay(report)).toEqual(report)
 const off:any=structuredClone(vectors.checkpoint);delete off.repeatTools;delete off.policy;expect((projectRunReport(readExecution(off)!) as any).repeatTools).toBeUndefined()
 off.version=2;delete off.planState;off.plan=[];expect((projectRunReport(readExecution(off)!) as any).repeatTools).toBeUndefined()
})
for(const fault of vectors.refusals)it(`refuses invented repeat report ${fault}`,()=>{
 const report:any=projectRunReport(readExecution(vectors.checkpoint)!),r=report.repeatTools
 if(fault==='legacy')report.checkpointVersion=2
 if(fault==='off')r.policy['repeatTools.mode'].value='off'
 if(fault==='missing-policy')delete r.policy
 if(fault==='threshold')r.executions[0].stopAt=3
 if(fault==='counter')r.executions[0].count=0
 if(fault==='delivered')r.executions[0].reminded=false
 if(fault==='private-seen')r.executions[0].seen=[]
 if(fault==='private-policy')r.policy.private={value:'secret',layer:'run'}
 if(fault==='provenance')r.policy['repeatTools.mode'].layer='invented'
 expect(()=>readRunReport(report)).toThrow();expect(()=>readRunReportDisplay(report)).toThrow()
})
