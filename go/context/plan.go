package runcontext

import (
 "fmt"
 "strconv"
 "strings"
 "github.com/Judgment-Pack/judgment-pack-agent/go/failure"
)

const PlanHistoryLimit=32
const PlanDiffLimit=40
const PlanStepLimit=20
func planStepID(v *Value) bool {
 if !v.IsString() || !strings.HasPrefix(v.Str(),"step-") { return false }
 s:=strings.TrimPrefix(v.Str(),"step-")
 if len(s)==0 || len(s)>16 || s[0]=='0' { return false }
 for _,c:=range s { if c<'0'||c>'9' {return false} }
 n,err:=strconv.ParseInt(s,10,64);return err==nil&&n>=1&&n<=9007199254740991
}
func planDigest(plan *Value) string { return "sha256:"+ContextDigest(CanonicalJSON(plan)) }
func identifyPlan(plan,state *Value)(*Value,*Value){
 if state!=nil {return plan.Clone(),state.Clone()}
 steps:=orArray(plan).Clone()
 for i,step:=range steps.Items(){ step.Set("id",String(fmt.Sprintf("step-%d",i+1))) }
 return steps,ObjectOf("revision",String(planDigest(steps)),"sequence",Number(0),"nextId",Number(float64(steps.Len()+1)),"discarded",Number(0),"history",Array())
}
func planIdentityRefused()error{return failure.ToolInput("PlanIdentityInvalid: use each current step id once, or omit id for a new step; the plan was left untouched.")}
// revisePlan constructs a detached candidate. Its caller publishes checkpoint and event together.
func revisePlan(current,state,incoming *Value,explanation string)(*Value,*Value,error){
 state=state.Clone(); next:=state.Get("nextId").Num()
 if state.Get("sequence").Num()>=9007199254740991||next>9007199254740991-float64(incoming.Len()){return nil,nil,planIdentityRefused()}
 old:=map[string]*Value{}; positions:=map[string]int{}
 for i,s:=range current.Items(){old[s.Get("id").Str()]=s;positions[s.Get("id").Str()]=i}
 seen:=map[string]bool{};plan:=Array()
 for _,input:=range incoming.Items(){
  id:=input.Get("id").Str()
  if input.Has("id")&&(old[id]==nil||seen[id]){return nil,nil,planIdentityRefused()}
  if !input.Has("id"){id=fmt.Sprintf("step-%.0f",next);next++};seen[id]=true
  step:=Object();if old[id]!=nil {for _,key:=range old[id].Keys(){if key!="detail"{step.Set(key,old[id].Get(key).Clone())}}}
  for _,key:=range input.Keys(){step.Set(key,input.Get(key).Clone())};step.Set("id",String(id));plan.Append(step)
 }
 diff:=Array()
 for i,s:=range current.Items(){
  id:=s.Get("id").Str();to:=-1;var after *Value
  for j,candidate:=range plan.Items(){if candidate.Get("id").Str()==id{to=j;after=candidate;break}}
  if to<0{diff.Append(ObjectOf("id",String(id),"kind",String("removed"),"before",s.Clone(),"from",Number(float64(i))));continue}
  if s.Get("step").Str()!=after.Get("step").Str()||s.Has("detail")!=after.Has("detail")||s.Get("detail").Str()!=after.Get("detail").Str(){diff.Append(ObjectOf("id",String(id),"kind",String("changed"),"before",s.Clone(),"after",after.Clone()))}
  if s.Get("status").Str()!=after.Get("status").Str(){diff.Append(ObjectOf("id",String(id),"kind",String("status"),"before",s.Clone(),"after",after.Clone()))}
  if i!=to{diff.Append(ObjectOf("id",String(id),"kind",String("reordered"),"from",Number(float64(i)),"to",Number(float64(to))))}
 }
 for to,s:=range plan.Items(){id:=s.Get("id").Str();if old[id]==nil{diff.Append(ObjectOf("id",String(id),"kind",String("added"),"after",s.Clone(),"to",Number(float64(to))))}}
 if diff.Len()>PlanDiffLimit{return nil,nil,failure.ToolInput("PlanRevisionTooLarge: a plan revision may record at most forty changes; the plan was left untouched.")}
 sequence:=state.Get("sequence").Num()+1;revision:=planDigest(plan)
 history:=state.Get("history").Clone();history.Append(ObjectOf("sequence",Number(sequence),"previous",state.Get("revision").Clone(),"revision",String(revision),"explanation",String(explanation),"diff",diff))
 discarded:=state.Get("discarded").Num();if history.Len()>PlanHistoryLimit{rows:=history.Items();history=Array(rows[1:]...);discarded++}
 return plan,ObjectOf("revision",String(revision),"sequence",Number(sequence),"nextId",Number(next),"discarded",Number(discarded),"history",history),nil
}
func validPlanState(plan,state *Value)bool{
 if state==nil{for _,s:=range plan.Items(){if s.Has("id"){return false}};return true}
 if !state.IsObject()||!isCount(state.Get("sequence"),9007199254740991)||!isCount(state.Get("discarded"),9007199254740991)||!isCount(state.Get("nextId"),9007199254740991)||state.Get("nextId").Num()<1||state.Get("revision").Str()!=planDigest(plan){return false}
 sequence,discarded:=state.Get("sequence").Num(),state.Get("discarded").Num();history:=state.Get("history")
 if !history.IsArray()||history.Len()>PlanHistoryLimit||discarded+float64(history.Len())!=sequence||float64(history.Len())!=min(sequence,PlanHistoryLimit){return false}
 seen:=map[string]bool{}
 for _,s:=range plan.Items(){id:=s.Get("id");if !planStepID(id)||seen[id.Str()]{return false};n,_:=strconv.ParseInt(strings.TrimPrefix(id.Str(),"step-"),10,64);if float64(n)>=state.Get("nextId").Num(){return false};seen[id.Str()]=true}
 previous:=""
 for i,row:=range history.Items(){
  if !row.IsObject()||row.Get("sequence").Num()!=discarded+float64(i)+1||!planRevisionDigest(row.Get("previous"))||!planRevisionDigest(row.Get("revision"))||previous!=""&&row.Get("previous").Str()!=previous||!isText(row.Get("explanation"),2000)||!row.Get("diff").IsArray()||row.Get("diff").Len()>PlanDiffLimit{return false}
  previous=row.Get("revision").Str()
  for _,d:=range row.Get("diff").Items(){if !validPlanChange(d,state.Get("nextId").Num()){return false}}

 }
 return history.Len()==0||previous==state.Get("revision").Str()
}

func planRevisionDigest(v *Value)bool{return v.IsString()&&strings.HasPrefix(v.Str(),"sha256:")&&digest64.MatchString(strings.TrimPrefix(v.Str(),"sha256:"))}

func validPlanChange(d *Value,nextID float64)bool{
 if !d.IsObject()||!planStepID(d.Get("id")){return false}
 id:=d.Get("id").Str();n,_:=strconv.ParseInt(strings.TrimPrefix(id,"step-"),10,64);if float64(n)>=nextID{return false}
 for _,key:=range []string{"before","after"}{if s:=d.Get(key);s!=nil{if !ValidTaskSteps(Array(s))||s.Get("id").Str()!=id{return false}}}
 switch d.Get("kind").Str(){
 case "added":return d.Has("after")&&!d.Has("before")&&isCount(d.Get("to"),19)&&!d.Has("from")
 case "removed":return d.Has("before")&&!d.Has("after")&&isCount(d.Get("from"),19)&&!d.Has("to")
 case "reordered":return isCount(d.Get("from"),19)&&isCount(d.Get("to"),19)&&!d.Has("before")&&!d.Has("after")&&d.Get("from").Num()!=d.Get("to").Num()
 case "changed","status":return d.Has("before")&&d.Has("after")&&!d.Has("from")&&!d.Has("to")
 default:return false
 }
}
