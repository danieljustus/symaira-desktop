package store

// The oracle uses the real Store methods and migrations. SQLite clock functions
// are overridden only in this opt-in, isolated test process, never in production.
import (
 "bytes"
 "context"
 "encoding/json"
 "fmt"
 "os"
 "path/filepath"
 "runtime"
 "testing"
 "time"
 "database/sql/driver"
 sqlite "modernc.org/sqlite"
)

type queueOp struct { Op string `json:"op"`; ID int64 `json:"id"`; Text string `json:"text"` }
type queueStep struct { Input queueOp `json:"input"`; Result any `json:"result"`; Error string `json:"error"`; Jobs []map[string]any `json:"jobs"`; Documents []map[string]any `json:"documents"` }

func TestPortIngestQueueContract(t *testing.T) {
 if os.Getenv("PORT_INGEST_QUEUE") != "1" { t.Skip("isolated SQLite fake-clock oracle; set PORT_INGEST_QUEUE=1") }
 now := "2000-01-01 00:00:00"
 if err := sqlite.RegisterScalarFunction("current_timestamp", 0, func(_ *sqlite.FunctionContext, _ []driver.Value)(driver.Value,error){return now,nil}); err != nil {t.Fatal(err)}
 if err := sqlite.RegisterScalarFunction("datetime", -1, func(_ *sqlite.FunctionContext, args []driver.Value)(driver.Value,error){
  if len(args)==2 && args[0]=="now" && args[1]=="-10 seconds" {v,e:=time.Parse("2006-01-02 15:04:05",now);if e!=nil{return nil,e};return v.Add(-10*time.Second).Format("2006-01-02 15:04:05"),nil};return nil,fmt.Errorf("unexpected datetime arguments: %v",args)
 }); err != nil {t.Fatal(err)}
 ctx:=context.Background();path:=filepath.Join(t.TempDir(),"queue.db")
 s,err:=Open(path);if err!=nil{t.Fatal(err)}
 defer func(){if err:=s.Close();err!=nil{t.Error(err)}}()
 for _,v:=range []string{"/vault/a","/vault/b"} {if _,_,err:=s.CreateOrGet(ctx,"input.pdf","hash","application/pdf",v);err!=nil{t.Fatal(err)}}
 ops:=[]queueOp{
 {"enqueue",1,"ocr"},{"enqueue",1,"ocr"},{"reocr",1,""},{"reocr",1,""},
 {"claim_id",1,""},{"claim_id",1,""},{"advance",0,"2000-01-01 01:00:00"},{"claim_id",1,""},
 {"fail",1,"first"},{"claim_id",1,""},{"advance",0,"2000-01-01 01:00:09"},{"claim_id",1,""},
 {"advance",0,"2000-01-01 01:00:10"},{"claim_id",1,""},{"fail",1,"second"},
 {"advance",0,"2000-01-01 01:00:20"},{"claim_id",1,""},{"fail",1,"terminal"},
 {"advance",0,"2000-01-01 01:00:30"},{"claim_id",1,""},{"retry",1,""},{"claim_id",1,""},
 {"restart",0,""},{"claim_id",1,""},{"complete",1,""},{"claim_id",1,""},
 {"fail",1,"completed can fail"},{"retry",1,""},{"complete",1,""},
 {"claim",0,""},{"complete",2,""},{"claim",0,""},{"reocr",1,""},{"complete",3,""},{"reocr",1,""},
 {"skipped",2,"duplicate"},{"claim_id",5,""},{"retry",5,""},{"claim_id",5,""},{"reset",0,""},
 {"complete",999,""},{"fail",999,"missing"},{"retry",999,""},{"claim_id",999,""},
 {"list",0,""},{"list",1,"/vault/a"},{"list",1,"/vault/b"},{"list",-1,"/missing"},
 {"claim_id",4,""},{"complete",4,""},{"claim_id",5,""},{"complete",5,""},{"claim",0,""},
 }
 snapshot:=func(query string) []map[string]any {
  rows,e:=s.db.Query(query);if e!=nil{t.Fatal(e)};defer rows.Close();cols,e:=rows.Columns();if e!=nil{t.Fatal(e)}
  out:=[]map[string]any{};for rows.Next(){vals:=make([]any,len(cols));ptrs:=make([]any,len(cols));for i:=range vals{ptrs[i]=&vals[i]};if e:=rows.Scan(ptrs...);e!=nil{t.Fatal(e)};m:=map[string]any{};for i,k:=range cols{switch v:=vals[i].(type){case time.Time:m[k]=v.Format("2006-01-02 15:04:05");case []byte:m[k]=string(v);default:m[k]=v}};out=append(out,m)};if e:=rows.Err();e!=nil{t.Fatal(e)};return out
 }
 steps:=[]queueStep{}
 for _,op:=range ops {var result any;var e error
  switch op.Op {
  case "advance":now=op.Text
  case "enqueue":result,e=s.EnqueueJob(ctx,op.ID,op.Text)
  case "skipped":result,e=s.EnqueueSkippedJob(ctx,op.ID,"ocr",op.Text)
  case "reocr":var j *Job;var created bool;j,created,e=s.EnqueueReprocessJob(ctx,op.ID);result=map[string]any{"job":j,"created":created}
  case "claim_id":result,e=s.ClaimJobByID(ctx,op.ID)
  case "claim":result,e=s.ClaimJob(ctx)
  case "complete":e=s.CompleteJob(ctx,op.ID)
  case "fail":e=s.FailJob(ctx,op.ID,op.Text)
  case "retry":e=s.RetryJob(ctx,op.ID)
  case "reset":e=s.ResetRunningJobs(ctx)
  case "restart":if e=s.Close();e!=nil{t.Fatal(e)};s,e=Open(path);if e==nil{e=s.ResetRunningJobs(ctx)}
  case "list":var jobs []*Job;var total int;jobs,total,e=s.ListJobsPage(ctx,op.Text,int(op.ID),1);result=map[string]any{"jobs":jobs,"total":total}
  default:t.Fatal(op.Op)
  }
  text:="";if e!=nil{text=e.Error()};steps=append(steps,queueStep{op,result,text,snapshot("SELECT * FROM jobs ORDER BY id"),snapshot("SELECT * FROM documents ORDER BY id")})
 }
 schema:=snapshot("SELECT type, name, tbl_name, sql FROM sqlite_master WHERE name NOT LIKE 'sqlite_%' ORDER BY type,name")
 content,e:=json.MarshalIndent(map[string]any{"schema_version":1,"clock":"SQLite current_timestamp and datetime(now,-10 seconds) overridden in isolated Go test","schema":schema,"steps":steps},"","  ");if e!=nil{t.Fatal(e)};content=append(content,'\n')
 _,file,_,_:=runtime.Caller(0);out:=filepath.Join(filepath.Dir(file),"../../../../testdata/port/ingest/queue.json")
 if os.Getenv("PORT_GENERATE")=="1" {if e:=os.MkdirAll(filepath.Dir(out),0700);e!=nil{t.Fatal(e)};if e:=os.WriteFile(out,content,0600);e!=nil{t.Fatal(e)}} else {old,e:=os.ReadFile(out);if e!=nil{t.Fatal(e)};if !bytes.Equal(old,content){t.Fatal("ingest queue fixture drift; regenerate deliberately")}}
 t.Logf("replayed %d state-machine steps",len(steps))
}
