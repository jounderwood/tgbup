# {{md .Title}}
{{range .Threads}}
- [{{md (or .Title "Messages")}}]({{.Latest}}.md) · {{.Messages}} messages · {{.Range}} · [calendar]({{.DatesMD}})
{{- end}}
