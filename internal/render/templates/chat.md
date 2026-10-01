# {{md .Title}}
{{range .Sections}}
{{- if .Title}}
## {{md .Title}}
{{end}}
{{range .Months}}- [{{.Month}}]({{.Href}}.md)
{{end}}
{{- end}}
