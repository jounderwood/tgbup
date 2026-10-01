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
	pageHTML     = htmlTemplate("page.html")
	indexHTML    = htmlTemplate("index.html")
	chatHTML     = htmlTemplate("chat.html")
	datesHTML    = htmlTemplate("dates.html")
	redirectHTML = htmlTemplate("redirect.html")
	mediaHTML    = htmlTemplate("media.html")
	pageMD       = mdTemplate("page.md")
	indexMD      = mdTemplate("index.md")
	chatMD       = mdTemplate("chat.md")
	datesMD      = mdTemplate("dates.md")
)

func htmlTemplate(name string) *htmltemplate.Template {
	return htmltemplate.Must(htmltemplate.ParseFS(files, "templates/"+name))
}

func mdTemplate(name string) *texttemplate.Template {
	return texttemplate.Must(texttemplate.New(name).Funcs(texttemplate.FuncMap{"md": mdEscaper.Replace}).ParseFS(files, "templates/"+name))
}

func writePageHTML(w io.Writer, p Page) error { return pageHTML.Execute(w, p) }

func writePageMD(w io.Writer, p Page) error { return pageMD.Execute(w, p) }
