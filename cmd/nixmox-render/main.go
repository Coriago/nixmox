//go:build js && wasm

package main

import(
 "encoding/json"
 "syscall/js"
 "nixmox/internal/domain"
 "nixmox/internal/render"
)
func main(){
 callback:=js.FuncOf(func(this js.Value,args []js.Value)any{
  var input struct{Template render.Template `json:"template"`;Templates map[string]string `json:"templates"`;Values map[string]any `json:"values"`}
  if len(args)!=1||len(args[0].String())>4<<20||domain.StrictJSON([]byte(args[0].String()),&input)!=nil{return `{"error":"Invalid preview input."}`}
  result,err:=render.Render(input.Template,input.Values,input.Templates)
  var response any=result;if err!=nil{response=map[string]string{"error":err.Error()}}
  data,_:=json.Marshal(response);return string(data)
 });defer callback.Release()
 js.Global().Set("nixmoxRender",callback)
 select{}
}
