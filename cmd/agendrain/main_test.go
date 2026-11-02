package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLIExportAndList(t *testing.T){
	d:=t.TempDir();input:=filepath.Join(d,"p.jsonl");os.WriteFile(input,[]byte(`{"type":"session_meta","payload":{"id":"p"}}`+"\n"+`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"Continue"}]}}`+"\n"),0600)
	var out,stderr bytes.Buffer
	if code:=run([]string{"list","--source","codex","--input",input},&out,&stderr);code!=0||!strings.Contains(out.String(),"p"){t.Fatalf("%d %s %s",code,out.String(),stderr.String())}
	p:=filepath.Join(d,"out.zip");out.Reset();stderr.Reset()
	if code:=run([]string{"export","--source","codex","--input",input,"--output",p},&out,&stderr);code!=0{t.Fatalf("%d %s",code,stderr.String())}
	if _,e:=os.Stat(p);e!=nil{t.Fatal(e)}
	if code:=run([]string{"export","--source","codex","--input",input,"--output",p},&out,&stderr);code==0{t.Fatal("overwrote existing export")}
}

func TestCLIRejectsUnknownAndConflictingFlags(t *testing.T){
	for _,args:=range [][]string{{"wat"},{"export","--format","rar"},{"export","--files","full"},{"export","extra"}}{
	 var out,stderr bytes.Buffer;if run(args,&out,&stderr)==0{t.Errorf("accepted %v",args)}
	}
}
