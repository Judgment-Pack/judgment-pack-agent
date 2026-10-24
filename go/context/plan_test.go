package runcontext

import (
 "context"
 "encoding/json"
 "os"
 "strings"
 "testing"
 "github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)
func planFixtures(t *testing.T)[]*Value{t.Helper();raw,err:=os.ReadFile("../../contract/vectors/plan/checkpoints.json");if err!=nil{t.Fatal(err)};return MustParseJSON(string(raw)).Get("items").Items()}
func planInput(t *testing.T,plan *Value,omitIDs bool)*Value{t.Helper();out:=Array();for _,s:=range plan.Items(){next:=Object();for _,k:=range s.Keys(){if k!="id"||!omitIDs{next.Set(k,s.Get(k).Clone())}};out.Append(next)};return out}
func TestPlanSharedVectorsAndDeterministicDiff(t *testing.T){
 rows:=planFixtures(t)
 for _,row:=range rows{saved,err:=ReadExecution(row);if err!=nil{t.Fatal(err)};raw,_:=json.Marshal(saved);if CanonicalJSON(MustParseJSON(string(raw)))!=CanonicalJSON(row){t.Fatal("shared checkpoint changed")}}
 raw,err:=os.ReadFile("../../contract/vectors/plan/events.json");if err!=nil{t.Fatal(err)}
 for _,row:=range MustParseJSON(string(raw)).Get("items").Items(){var event contract.RunEvent;if err=json.Unmarshal(row.JSON(),&event);err!=nil{t.Fatal(err)};encoded,_:=json.Marshal(event);if CanonicalJSON(MustParseJSON(string(encoded)))!=CanonicalJSON(row){t.Fatal("shared event changed")}}
 plan,state:=identifyPlan(Array(),nil)
 for i:=1;i<len(rows);i++{
  next:=rows[i].Get("plan");if i==1{next=planInput(t,next,true)};if i==2{next=Array(next.Items()[0].Clone(),ObjectOf("step",String("Report"),"status",String("pending")))}
  plan,state,err=revisePlan(plan,state,next,rows[i].Get("explanation").Str());if err!=nil{t.Fatal(err)}
  if CanonicalJSON(plan)!=CanonicalJSON(rows[i].Get("plan"))||CanonicalJSON(state)!=CanonicalJSON(rows[i].Get("planState")){t.Fatalf("shared transition %d differs",i)}
 }
}
func TestPlanIdentityAndBoundsRefusalsAreAtomic(t *testing.T){
 rows:=planFixtures(t)
 for _,input:=range []string{`[{"id":"step-99","step":"Inspect","status":"pending"}]`,`[{"id":"step-2","step":"Verify","status":"pending"},{"id":"step-2","step":"Verify","status":"pending"}]`,`[{"id":"step-1","step":"Removed","status":"pending"}]`}{
  plan,state:=rows[2].Get("plan").Clone(),rows[2].Get("planState").Clone();before:=CanonicalJSON(state)
  if _,_,err:=revisePlan(plan,state,MustParseJSON(input),"Invalid");err==nil||!strings.Contains(err.Error(),"PlanIdentityInvalid"){t.Fatalf("identity refusal: %v",err)};if CanonicalJSON(state)!=before{t.Fatal("refused revision mutated state")}
 }
 plan:=Array();for i:=0;i<20;i++{plan.Append(ObjectOf("step",String("Item"),"status",String("pending")))};plan,state:=identifyPlan(plan,nil);next:=Array()
 for i:=len(plan.Items())-1;i>=0;i--{s:=plan.Items()[i].Clone();s.Set("step",String("Changed"));s.Set("status",String("completed"));next.Append(s)}
 if _,_,err:=revisePlan(plan,state,next,"Too many");err==nil||!strings.Contains(err.Error(),"PlanRevisionTooLarge"){t.Fatalf("overflow: %v",err)}
 for i:=0;i<35;i++{var err error;plan,state,err=revisePlan(plan,state,plan,"No change");if err!=nil{t.Fatal(err)}}
 if state.Get("discarded").Num()!=3||state.Get("history").Len()!=32||state.Get("nextId").Num()!=21||!validPlanState(plan,state){t.Fatal("bounded history/high-water invalid")}
}
func TestPlanSemanticStateRefusals(t *testing.T){
 for _,change:=range []func(*Value){func(s *Value){s.Get("planState").Set("revision",String("sha256:"+strings.Repeat("b",64)))},func(s *Value){s.Get("planState").Set("nextId",Number(2))},func(s *Value){s.Get("planState").Get("history").Items()[1].Set("previous",String("sha256:"+strings.Repeat("b",64)))},func(s *Value){s.Get("planState").Set("discarded",Number(1))},func(s *Value){s.Get("plan").Items()[0].Set("id",String("step-3"))}}{row:=planFixtures(t)[2].Clone();change(row);if _,err:=ReadExecution(row);err==nil{t.Fatal("malformed plan state accepted")}}
}
func TestPlanLegacyReadAndResumePreserveIdentity(t *testing.T){
 session,_:=trackedSession(t);run:=openRun(t,session);defer run.Context.Close()
 saved,err:=run.Context.Execution.Snapshot("stopped");if err!=nil{t.Fatal(err)}
 legacy:=MustParseJSON(`{"version":1,"id":"legacy-plan","requestKey":"`+saved.RequestKey+`","phase":"work","status":"stopped","unit":"model-requests","used":0,"limit":20,"grants":[],"plan":[{"step":"Legacy","status":"completed"}],"explanation":"","operations":[],"context":{"scope":"legacy-scope","entries":[]}}`)
 cp,err:=ReadExecution(legacy);if err!=nil{t.Fatal(err)};before:=CanonicalJSON(legacy)
 held:=*session.Context;held.Execution=cp;session.Context=&held;resumed:=openRun(t,session);defer resumed.Context.Close()
 if CanonicalJSON(legacy)!=before||resumed.Context.Execution.State().Plan.Items()[0].Get("id").Str()!="step-1"{t.Fatal("legacy read rewritten or missing migration")}
 snapshot,err:=resumed.Context.Execution.Snapshot("stopped");if err!=nil{t.Fatal(err)};held.Execution=&snapshot;again:=openRun(t,session);defer again.Context.Close()
 if CanonicalJSON(again.Context.Execution.State().PlanState)!=CanonicalJSON(resumed.Context.Execution.State().PlanState){t.Fatal("resume plan state changed")}
}
func TestPlanCommittedDeliveryIsPrivateAndChildCheckpointFresh(t *testing.T){
 ctx:=context.Background();store:=NewMemoryRunStore(MemoryRunStoreOptions{});writer,binding:=writerFixture(t,store,"plan-execution",false)
 row:=planFixtures(t)[1];cp,err:=ReadExecution(row);if err!=nil{t.Fatal(err)}
 raw,_:=os.ReadFile("../../contract/vectors/plan/events.json");var event contract.RunEvent;if err=json.Unmarshal(MustParseJSON(string(raw)).Get("items").Items()[0].JSON(),&event);err!=nil{t.Fatal(err)}
 committed,err:=writer.CommitPlan(ctx,*binding,*cp,event);if err!=nil{t.Fatal(err)};head,_:=store.Load(ctx,binding.Scope.Execution)
 if _,err=writer.Event(ctx,*binding,committed);err!=nil{t.Fatal(err)};next,_:=store.Load(ctx,binding.Scope.Execution);if next.Revision!=head.Revision{t.Fatal("private notice appended twice")}
 if _,err=writer.Event(ctx,*binding,committed);err!=nil{t.Fatal(err)};next,_=store.Load(ctx,binding.Scope.Execution);if next.Revision==head.Revision{t.Fatal("private identity was reusable")}
 child:=*binding;child.Root=false;child.Scope=DurableScope{Execution:"child-plan",Role:"author",Task:"child-plan"};cp.ID="child-plan";event.PlanRevised.Execution="child-plan"
 committed,err=writer.CommitPlan(ctx,child,*cp,event);if err!=nil{t.Fatal(err)};head,_=store.Load(ctx,binding.Scope.Execution);saved:=MustParseJSON(string(head.Checkpoint))
 if saved.Get("durable").Get("children").Items()[0].Get("checkpoint").Get("planState").Get("sequence").Num()!=1{t.Fatal("child checkpoint stale")}
 copied:=contract.StampEvent(committed);if _,err=writer.Event(ctx,child,copied);err!=nil{t.Fatal(err)};next,_=store.Load(ctx,binding.Scope.Execution);if next.Revision==head.Revision{t.Fatal("copy gained skip authority")}
 before:=next.Revision;if _,err=writer.Event(ctx,child,committed);err!=nil{t.Fatal(err)};next,_=store.Load(ctx,binding.Scope.Execution);if next.Revision!=before{t.Fatal("original child receipt not delivered")}
}
func TestPlanReceiptBeforeDeliveryResumeAndManagedNotice(t *testing.T){
 ctx:=context.Background();store:=NewMemoryRunStore(MemoryRunStoreOptions{});session,_:=trackedSession(t);session.Context.RunStore=&DurableRunInput{Store:store,ID:"plan-execution"}
 run:=openRun(t,session);tool:=hostTool(t,run,"update_plan");args:=json.RawMessage(`{"explanation":"Track","plan":[{"step":"Inspect","status":"pending"}]}`)
 if _,err:=tool.Execute(ctx,args);err!=nil{t.Fatal(err)};head,_:=store.Load(ctx,"plan-execution");if MustParseJSON(string(head.Checkpoint)).Get("planState").Get("sequence").Num()!=1{t.Fatal("tool answered before plan commit")}
 boundary:=NewBoundary(run);out,_:=boundary.Event(contract.RunEvent{ToolResult:&contract.ToolResult{Type:"tool_result",Name:"update_plan",IsError:false,Text:"recorded"}})
 notices:=0;for _,e:=range out{if e.PlanRevised!=nil{notices++}};if notices!=1{t.Fatalf("managed notice count %d",notices)}
 page,_:=store.ReadEvents(ctx,"plan-execution","",100);recorded:=0;for _,e:=range page.Items{envelope,err:=ReadStoredRunEvent(e.Bytes);if err!=nil{t.Fatal(err)};if envelope.Event.PlanRevised!=nil{recorded++}};if recorded!=1{t.Fatal("managed event append duplicated")}
 run.Context.Close();session.Context.RunStore.Resume=true;resumed:=openRun(t,session);defer resumed.Context.Close();if resumed.Context.Execution.State().PlanState.Get("sequence").Num()!=1{t.Fatal("resume regenerated revision")}
 for _,notice:=range resumed.Context.TakeNotices(){if notice.PlanRevised!=nil{t.Fatal("resume recreated delivery notice")}}
}
