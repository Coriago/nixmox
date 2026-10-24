package browser

import (
	"github.com/maxence-charriere/go-app/v11/pkg/app"
	"nixmox/internal/domain"
	"nixmox/internal/ui/components/form"
)

func Render(model form.Model, set func(string, string, string), secret func(string, []byte), notice func(string)) app.UI {
	return app.Section().Class("identity-panel").Body(
		app.H2().Text(model.Title), app.P().Text(model.Note),
		app.Range(model.Fields).Slice(func(i int) app.UI {
			field := model.Fields[i]
			var input app.UI
			switch field.Kind {
			case "select":
				input = app.Select().Class("query-input").Aria("label", field.Label).OnChange(func(ctx app.Context, e app.Event) { set(model.ID, field.ID, e.Get("target").Get("value").String()) }, app.EventScope(model.ID, field.ID)).Body(
					app.Option().Value("").Selected(field.Value == "").Text("Select…"),
					app.Range(field.Choices).Slice(func(j int) app.UI {
						choice := field.Choices[j]
						return app.Option().Value(choice.Value).Selected(choice.Value == field.Value).Text(choice.Label)
					}),
				)
			case "file":
				input = app.Input().Type("file").Aria("label", field.Label).OnChange(func(ctx app.Context, e app.Event) {
					secret(model.ID, nil)
					files := e.Get("target").Get("files")
					if files.Length() == 0 {
						return
					}
					file := files.Index(0)
					if file.Get("size").Int() > domain.CommandPayloadLimit {
						notice("Credential file exceeds the size limit")
						return
					}
					file.Call("text").Then(func(value app.Value) {
						data := []byte(value.String())
						ctx.Dispatch(func(app.Context) {
							defer clear(data)
							secret(model.ID, data)
							set(model.ID, field.ID, file.Get("name").String())
						})
					})
				}, app.EventScope(model.ID, field.ID))
			case "password":
				input = app.Input().Type("password").AutoComplete(false).Class("query-input").Aria("label", field.Label).OnInput(func(ctx app.Context, e app.Event) {
					data := []byte(e.Get("target").Get("value").String())
					defer clear(data)
					secret(model.ID, data)
				}, app.EventScope(model.ID, field.ID))
			default:
				input = app.Input().Type("text").Class("query-input").Aria("label", field.Label).Value(field.Value).OnInput(func(ctx app.Context, e app.Event) { set(model.ID, field.ID, e.Get("target").Get("value").String()) }, app.EventScope(model.ID, field.ID))
			}
			return app.Div().Body(app.Label().Text(field.Label), input)
		}),
	)
}
