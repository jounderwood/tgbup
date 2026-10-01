# {{.Title}}
{{range .Days}}
## {{.Date}}
{{range .Messages}}
{{- if .Service}}
_{{md .Service}}_ · {{md .Sender}} · {{.Time}} ^m{{.ID}}
{{else}}
**{{md .Sender}}** · {{.Time}}{{if .Edited}} · edited{{end}}{{if .Pinned}} · pinned{{end}} ^m{{.ID}}

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
{{- with .Reactions}}{{.}}
{{end}}
{{- end}}
{{- end}}
{{- end}}
