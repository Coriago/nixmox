//go:build js && wasm

package main

import (
	"encoding/json"
	"nixmox/internal/domain"
	"nixmox/internal/presentation"
	"strings"
	"syscall/js"
)

// Only configuration preview runs in WASM; ordinary console views stay HTML.
func main() {
	render := js.FuncOf(func(_ js.Value, args []js.Value) any {
		result := presentation.PreviewResult{}
		if len(args) != 1 || args[0].Type() != js.TypeString || len(args[0].String()) > 2<<20 {
			result.Errors = []domain.FieldError{{Code: "INVALID_PREVIEW", Message: "Expected bounded preview JSON"}}
		} else {
			var request presentation.PreviewRequest
			decoder := json.NewDecoder(strings.NewReader(args[0].String()))
			decoder.DisallowUnknownFields()
			decoder.UseNumber()
			if err := decoder.Decode(&request); err != nil {
				result.Errors = []domain.FieldError{{Code: "INVALID_PREVIEW", Message: err.Error()}}
			} else {
				result = (presentation.TemplateRenderer{}).Preview(request)
			}
		}
		data, _ := json.Marshal(result)
		return string(data)
	})
	js.Global().Set("nixmoxPreview", render)
	select {}
}
