# {{md .Title}} · dates
{{range .Years}}
## {{.Year}}
{{range .Months}}
**{{.Name}}**:{{range .Days}} [{{.Day}}]({{.MDHref}}){{end}}
{{end}}
{{- end}}
## Pages
{{range .Pages}}
- [page {{.N}}]({{.Href}}.md) · {{.Range}} · {{.Count}} messages
{{- end}}
