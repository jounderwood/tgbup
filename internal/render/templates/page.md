# {{md .Title}}

{{if .Prev}}[← older]({{.Prev}}.md) · {{end}}[dates](dates.md){{if .Next}} · [newer →]({{.Next}}.md){{end}}
{{range .Days}}
## {{.Date}}
{{range .Messages}}
{{- if .Service}}
_{{md .Service}}_ · {{md .Sender}} · {{.Time}} ^m{{.ID}}
{{else}}
**{{md .Sender}}** · {{.Time}}{{if .Edited}} · edited{{end}}{{if .Pinned}} · pinned{{end}}{{if .Deleted}} · deleted in Telegram{{end}} ^m{{.ID}}

{{with .Forward}}_{{md .}}_
{{end}}
{{- with .Reply}}
{{- if .Missing}}> _reply to a message not in the archive_

{{else if .NoLink}}> {{md .Sender}}: {{md .Text}}

{{else}}> {{md .Sender}}: {{md .Text}} [↩]({{if .Page}}{{.Page}}.md{{end}}#^m{{.ID}})

{{end}}
{{- end}}
{{- with .Media}}
{{- if not .Href}}_{{md .Note}}_
{{else if or (eq .Kind "document") (eq .Kind "audio")}}[{{md .Name}}]({{.Href}}){{with .Size}} · {{.}}{{end}}
{{else}}![]({{.Href}})
{{end}}
{{- end}}
{{- with .Markdown}}{{.}}
{{end}}
{{- range .Versions}}
> _{{.When}}:_ {{quote .Markdown}}{{if .Attachment}} _(with another attachment)_{{end}}
{{end}}
{{- with .Meta}}
{{- with .Link}}
> {{if .Href}}[**{{md .Title}}**](<{{mdurl .Href}}>){{else}}**{{md .Title}}**{{end}}{{with .Site}} · {{md .}}{{end}}
{{- with .Description}}
> {{md .}}{{end}}
{{- with .Photo}}
> ![]({{.}}){{end}}
{{end}}
{{- with .Place}}📍 {{with .Name}}{{md .}} — {{end}}[{{.Coords}}](<{{mdurl .MapURL}}>){{if .Live}} · live location (last point){{end}}
{{end}}
{{- with .Contact}}👤 {{md .Name}}{{with .Phone}} · {{md .}}{{end}}
{{end}}
{{- with .Poll}}**📊 {{md .Question}}**
{{range .Answers}}- {{md .Text}} — {{.Percent}}% ({{.Voters}}){{if .Correct}} ✓{{end}}
{{end}}_{{.Total}} votes{{with .Flags}} · {{.}}{{end}}_
{{end}}
{{- with .Todo}}**☑ {{md .Title}}**
{{range .Items}}- [{{if .Done}}x{{else}} {{end}}] {{md .Title}}{{with .By}} — {{md .}}{{end}}
{{end}}
{{- end}}
{{- with .Dice}}{{.}}
{{end}}
{{- with .Other}}_{{md .}}_
{{end}}
{{- end}}
{{- with .Reactions}}{{.}}
{{end}}
{{- end}}
{{- end}}
{{- end}}
