package drain

import (
	"archive/zip"
	"bytes"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
	_ "modernc.org/sqlite"
)

func put(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil { t.Fatal(err) }
	if err := os.WriteFile(p, []byte(body), 0600); err != nil { t.Fatal(err) }
	return p
}
func joined(b *Bundle) string { p, _ := json.Marshal(b); return string(p) }
func load(t *testing.T, source, path string) *Bundle {
	t.Helper(); b, err := Load(source, path); if err != nil { t.Fatal(err) }; return b
}

func TestCodexContextSubagentsAndZstd(t *testing.T) {
	d := t.TempDir()
	put(t,d,"parent.jsonl", `{"type":"session_meta","payload":{"id":"p","cwd":"/repo","base_instructions":{"text":"Keep exact decisions"}}}
{"type":"turn_context","payload":{"model":"gpt-6.1","developer_instructions":"Use CI"}}
{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"Fix it"}]}}
{"type":"event_msg","payload":{"type":"user_message","message":"Fix it"}}
{"type":"response_item","payload":{"type":"function_call","name":"exec_command","call_id":"c","arguments":"{\"cmd\":\"git status\"}"}}
{"type":"response_item","payload":{"type":"function_call_output","call_id":"c","output":"clean"}}
{"type":"compacted","payload":{"message":"Previous decision: retain format"}}
`)
	raw := []byte(`{"type":"session_meta","payload":{"id":"child","source":{"subagent":{"thread_spawn":{"parent_thread_id":"p"}}}}}`+"\n"+`{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Child finding"}]}}`+"\n")
	enc, _ := zstd.NewWriter(nil); compressed := enc.EncodeAll(raw,nil); enc.Close()
	if err:=os.WriteFile(filepath.Join(d,"child.jsonl.zst"),compressed,0600);err!=nil {t.Fatal(err)}
	put(t,d,"other.jsonl",`{"type":"session_meta","payload":{"id":"unrelated"}}`+"\n")
	b:=load(t,"codex",d)
	if err:=b.Select("p");err!=nil {t.Fatal(err)}
	s:=joined(b)
	for _,want:=range []string{"Keep exact decisions","Use CI","git status","clean","retain format","Child finding","parent_id"} {if !strings.Contains(s,want){t.Errorf("missing %s: %s",want,s)}}
	if strings.Contains(s,"unrelated") {t.Fatal("included unrelated session")}
	if strings.Count(s,"Fix it")!=1 {t.Fatal("double-counted Codex event mirror")}
}

func TestClaudeSidechainsAndToolResults(t *testing.T) {
	d:=t.TempDir()
	p:=put(t,d,"p.jsonl",`{"type":"user","sessionId":"p","uuid":"u","message":{"role":"user","content":"Exact requirement"}}
{"type":"assistant","sessionId":"p","uuid":"a","message":{"role":"assistant","model":"claude-sonnet-4","content":[{"type":"thinking","thinking":"Reason"},{"type":"tool_use","id":"tool","name":"Read","input":{"file_path":"/repo/main.go"}}]}}
{"type":"user","sessionId":"p","uuid":"r","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool","content":"recorded result"}]}}
`)
	put(t,d,"p/subagents/agent-x.jsonl",`{"type":"assistant","sessionId":"p","agentId":"x","isSidechain":true,"message":{"role":"assistant","content":"subagent outcome"}}`+"\n")
	b:=load(t,"claude",p)
	if len(b.Sessions)!=2 {t.Fatalf("want parent and subagent, got %s",joined(b))}
	for _,s:=range []string{"Reason","recorded result","subagent outcome","/repo/main.go"} {if !strings.Contains(joined(b),s){t.Error(s)}}
}

func TestClaudeDesktopExportZip(t *testing.T) {
	d:=t.TempDir(); var buf bytes.Buffer; z:=zip.NewWriter(&buf)
	f,_:=z.Create("conversations.json"); f.Write([]byte(`[{"uuid":"desk","name":"Work","chat_messages":[{"uuid":"m","sender":"human","text":"Continue this","attachments":[{"file_name":"code.go","extracted_content":"DO NOT INCLUDE FILE BODY"}]}]}]`));z.Close()
	p:=filepath.Join(d,"export.zip");os.WriteFile(p,buf.Bytes(),0600)
	b:=load(t,"claude-desktop",p)
	if !strings.Contains(joined(b),"Continue this") || strings.Contains(joined(b),"DO NOT INCLUDE FILE BODY") {t.Fatal(joined(b))}
}

func TestOpenCodeExport(t *testing.T) {
	d:=t.TempDir();p:=put(t,d,"export.json",`{"info":{"id":"s","title":"Build","directory":"/repo"},"messages":[{"info":{"id":"m","role":"assistant","modelID":"custom-model"},"parts":[{"type":"text","text":"Exact decision"},{"type":"tool","tool":"bash","callID":"call","state":{"status":"completed","input":{"command":"go test"},"output":"PASS"}}]}]}`)
	b:=load(t,"opencode",p)
	for _,want:=range []string{"Exact decision","go test","PASS"} {if !strings.Contains(joined(b),want){t.Error(want)}}
}

func database(t *testing.T, statements ...string) string {
	t.Helper();p:=filepath.Join(t.TempDir(),"state.db");db,err:=sql.Open("sqlite",p);if err!=nil{t.Fatal(err)}
	for _,s:=range statements {if _,err:=db.Exec(s);err!=nil{t.Fatal(err)}};if err:=db.Close();err!=nil{t.Fatal(err)};return p
}

func TestOpenCodeSQLiteSubagents(t *testing.T) {
	p:=database(t,
	`CREATE TABLE session(id TEXT,parent_id TEXT,title TEXT,directory TEXT); CREATE TABLE message(id TEXT,session_id TEXT,time_created INTEGER,data TEXT); CREATE TABLE part(id TEXT,message_id TEXT,session_id TEXT,time_created INTEGER,data TEXT);`,
	`INSERT INTO session VALUES('p',NULL,'Parent','/repo'),('c','p','Child','/repo');`,
	`INSERT INTO message VALUES('m','p',1,'{"role":"user"}'),('n','c',2,'{"role":"assistant"}');`,
	`INSERT INTO part VALUES('a','m','p',1,'{"type":"text","text":"Request"}'),('b','n','c',2,'{"type":"reasoning","text":"Child context"}');`)
	b:=load(t,"opencode",p);if err:=b.Select("p");err!=nil{t.Fatal(err)}
	if len(b.Sessions)!=2 || !strings.Contains(joined(b),"Child context"){t.Fatal(joined(b))}
}

func TestOpenCodeV2SQLite(t *testing.T) {
	p:=database(t,`CREATE TABLE session_v2(id TEXT,parent_id TEXT,title TEXT,directory TEXT); CREATE TABLE session_message(id TEXT,session_id TEXT,type TEXT,seq INTEGER,time_created INTEGER,data TEXT);`,
	`INSERT INTO session_v2 VALUES('p',NULL,'V2','/repo');`,
	`INSERT INTO session_message VALUES('u','p','user',1,1,'{"text":"User context"}'),('a','p','assistant',2,2,'{"content":[{"type":"text","text":"V2 decision"}]}');`)
	b:=load(t,"opencode",p);if !strings.Contains(joined(b),"V2 decision"){t.Fatal(joined(b))}
}

func TestCursorOrderedBubblesAndSubagents(t *testing.T) {
	p:=database(t,`CREATE TABLE cursorDiskKV(key TEXT PRIMARY KEY,value BLOB);`,
	`INSERT INTO cursorDiskKV VALUES('composerData:p','{"composerId":"p","name":"Task","fullConversationHeadersOnly":[{"bubbleId":"z","type":1},{"bubbleId":"a","type":2}]}');`,
	`INSERT INTO cursorDiskKV VALUES('bubbleId:p:a','{"type":2,"text":"Decision","toolFormerData":{"name":"run","params":"test","result":"passed"}}'),('bubbleId:p:z','{"type":1,"text":"Requirement"}');`,
	`INSERT INTO cursorDiskKV VALUES('composerData:task-toolu_x','{"composerId":"task-toolu_x","parentComposerId":"p","conversation":[{"type":2,"text":"Child result"}]}');`)
	b:=load(t,"cursor",p);if err:=b.Select("p");err!=nil{t.Fatal(err)}
	s:=joined(b);if strings.Index(s,"Requirement")>strings.Index(s,"Decision") || !strings.Contains(s,"Child result") || !strings.Contains(s,"passed"){t.Fatal(s)}
}

func TestVTVibeCopiedTree(t *testing.T) {
	d:=t.TempDir();put(t,d,"session.md","# vtvibe session\nmodel    : local-model\n")
	put(t,d,"chat/0001-model.md","<!-- model, 2026-11-02 12:00:00 -->\n\nRCtrl+A to hide\n")
	put(t,d,"chat/0002-user.md","<!-- user, 2026-11-02 12:00:01 -->\n\nKeep this requirement\n")
	put(t,d,"chat/0003-assistant.md","<!-- assistant, 2026-11-02 12:00:02 -->\n\nChosen solution\n")
	put(t,d,"ctx/code.go","PRIVATE LOCAL FILE BODY")
	b:=load(t,"vtvibe",d);s:=joined(b)
	if !strings.Contains(s,"Chosen solution") || strings.Contains(s,"RCtrl+A") || strings.Contains(s,"PRIVATE LOCAL FILE BODY"){t.Fatal(s)}
}

func TestNoImplicitLocalReadsAndExplicitRoot(t *testing.T) {
	d:=t.TempDir();p:=put(t,d,"code.go","package x\n// Explain purpose\nfunc Important() {}\n")
	fresh:=func()*Bundle{return &Bundle{Version:Version,Assets:map[string][]byte{},Sessions:[]Session{{ID:"p",CWD:d,Events:[]Event{{Role:"user",Blocks:[]Block{{Type:"file",Path:p}}}}}}}}
	b:=fresh();if err:=Prepare(b,Options{});err!=nil{t.Fatal(err)};if strings.Contains(joined(b),"Important"){t.Fatal("implicit file read")}
	b=fresh();if err:=Prepare(b,Options{Files:"full",Roots:[]string{d}});err!=nil{t.Fatal(err)};if !strings.Contains(joined(b),"Important"){t.Fatal(joined(b))}
	b=fresh();if err:=Prepare(b,Options{Files:"summary",Roots:[]string{d}});err!=nil{t.Fatal(err)};if !strings.Contains(joined(b),"Explain purpose"){t.Fatal(joined(b))}
	if err:=Prepare(fresh(),Options{Files:"full"});err==nil{t.Fatal("full requires allow-root")}
}

func TestFileSymlinkEscape(t *testing.T) {
	d:=t.TempDir();outside:=put(t,t.TempDir(),"secret","SECRET")
	p:=filepath.Join(d,"escape");if err:=os.Symlink(outside,p);err!=nil{t.Skip(err)}
	b:=&Bundle{Assets:map[string][]byte{},Sessions:[]Session{{ID:"s",Events:[]Event{{Role:"user",Blocks:[]Block{{Type:"file",Path:p}}}}}}}
	if err:=Prepare(b,Options{Files:"full",Roots:[]string{d}});err!=nil{t.Fatal(err)}
	if strings.Contains(joined(b),"SECRET") || len(b.Warnings)==0 {t.Fatal(joined(b))}
}

func TestModelStrippingAndConservativeCompaction(t *testing.T) {
	b:=&Bundle{Assets:map[string][]byte{},Sessions:[]Session{{ID:"s",Models:[]string{"private-model-v9"},Events:[]Event{
	{Role:"user",Blocks:[]Block{{Type:"text",Text:"private-model-v9: keep exact decision"}}},
	{Role:"tool",Blocks:[]Block{{Type:"text",Text:"same output"}}},
	{Role:"tool",Blocks:[]Block{{Type:"text",Text:"same output"}}},
	{Role:"user",Blocks:[]Block{{Type:"text",Text:"private-model-v9: keep exact decision"}}},
	}}}}
	if err:=Prepare(b,Options{StripModels:true,Compact:true});err!=nil{t.Fatal(err)}
	s:=joined(b);if strings.Contains(s,"private-model-v9") || strings.Count(s,"keep exact decision")!=2 || strings.Count(s,"same output")!=1 {t.Fatal(s)}
}

func TestMarkdownMediaOrderAndBinaryOptIn(t *testing.T) {
	d:=t.TempDir();b:=&Bundle{Version:1,Assets:map[string][]byte{"b":{0xff,0xd8,0xff},"a":{137,80,78,71},"bin":{0,1,2}},Sessions:[]Session{{ID:"s",Events:[]Event{{Role:"user",Blocks:[]Block{
	{Type:"image",Asset:"b",MIME:"image/jpeg"},{Type:"image",Asset:"a",MIME:"image/png"},{Type:"file",Asset:"bin",MIME:"application/octet-stream"},
	}}}}}}
	p:=filepath.Join(d,"conversation.md");if err:=Write(b,p,Options{Format:"md"});err!=nil{t.Fatal(err)}
	raw,err:=os.ReadFile(p);if err!=nil{t.Fatal(err)};s:=string(raw)
	for _,name:=range []string{"00000001.jpg","00000002.png"}{if !strings.Contains(s,name){t.Error(s)}}
	if strings.Contains(s,"AAEC"){t.Fatal("binary embedded by default")}
	p2:=filepath.Join(d,"base64.md");if err:=Write(b,p2,Options{Format:"md",Base64:true});err!=nil{t.Fatal(err)}
	raw,_=os.ReadFile(p2);if !strings.Contains(string(raw),base64.StdEncoding.EncodeToString([]byte{0,1,2})){t.Fatal(string(raw))}
}

func TestZipDeterministicAndNoRawSource(t *testing.T) {
	b:=&Bundle{Version:1,Assets:map[string][]byte{},Sessions:[]Session{{ID:"s",Events:[]Event{{Role:"user",Blocks:[]Block{{Type:"text",Text:"Continue"}}}}}}}
	d:=t.TempDir();a:=filepath.Join(d,"a.zip");c:=filepath.Join(d,"b.zip")
	for _,p:=range []string{a,c}{if err:=Write(b,p,Options{});err!=nil{t.Fatal(err)}}
	x,_:=os.ReadFile(a);y,_:=os.ReadFile(c);if !bytes.Equal(x,y){t.Fatal("nondeterministic zip")}
	z,err:=zip.OpenReader(a);if err!=nil{t.Fatal(err)};defer z.Close();names:=map[string]bool{};for _,f:=range z.File{names[f.Name]=true}
	if !names["manifest.json"] || !names["conversation.md"]{t.Fatal(names)}
}

func TestMalformedInputFailsVisibly(t *testing.T) {
	p:=put(t,t.TempDir(),"bad.jsonl","{broken}\n")
	if _,err:=Load("codex",p);err==nil{t.Fatal("silently accepted corrupt transcript")}
}

func TestSelectionMissingChildIsReported(t *testing.T) {
	b:=&Bundle{Sessions:[]Session{{ID:"p",Events:[]Event{{Role:"assistant",Kind:"tool_call",Name:"spawn_agent",Blocks:[]Block{{Type:"text",Text:`{"agent_id":"missing"}`}}}}}}}
	if err:=b.Select("p");err!=nil{t.Fatal(err)}
	if len(b.Warnings)==0{t.Fatal("missing child must be visible")}
}
