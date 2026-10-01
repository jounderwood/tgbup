package render

import (
	"embed"
	htmltemplate "html/template"
	"io"
	texttemplate "text/template"
)

//go:embed templates assets
var files embed.FS

var (
	monthHTML = htmltemplate.Must(htmltemplate.ParseFS(files, "templates/month.html"))
	monthMD   = texttemplate.Must(texttemplate.New("month.md").Funcs(texttemplate.FuncMap{"md": mdEscaper.Replace}).ParseFS(files, "templates/month.md"))
)

func writeMonthHTML(w io.Writer, p Page) error { return monthHTML.Execute(w, p) }

func writeMonthMD(w io.Writer, p Page) error { return monthMD.Execute(w, p) }
