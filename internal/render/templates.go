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
	monthHTML = htmlTemplate("month.html")
	indexHTML = htmlTemplate("index.html")
	chatHTML  = htmlTemplate("chat.html")
	monthMD   = mdTemplate("month.md")
	indexMD   = mdTemplate("index.md")
	chatMD    = mdTemplate("chat.md")
)

func htmlTemplate(name string) *htmltemplate.Template {
	return htmltemplate.Must(htmltemplate.ParseFS(files, "templates/"+name))
}

func mdTemplate(name string) *texttemplate.Template {
	return texttemplate.Must(texttemplate.New(name).Funcs(texttemplate.FuncMap{"md": mdEscaper.Replace}).ParseFS(files, "templates/"+name))
}

func writeMonthHTML(w io.Writer, p Page) error { return monthHTML.Execute(w, p) }

func writeMonthMD(w io.Writer, p Page) error { return monthMD.Execute(w, p) }
