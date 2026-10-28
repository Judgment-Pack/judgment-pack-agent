import {expect,it} from 'vitest'
import vectors from '../../../contract/fixtures/history/report-task-versions.json'
import {canonicalJSON} from '../context/chat'
import {readRunReport,readRunReportDisplay,RunReportInvalid} from '../runReportReader'
for(const row of vectors.cases)it(`keeps source task feature truthful ${row.name}`,()=>{
 const input=canonicalJSON(row.report)
 if(row.valid){expect(readRunReportDisplay(row.report)).toEqual(row.report);if(row.native)expect(readRunReport(input)).toEqual(row.report)}
 else{expect(()=>readRunReportDisplay(row.report)).toThrow(RunReportInvalid);expect(()=>readRunReport(input)).toThrow(RunReportInvalid)}
})
