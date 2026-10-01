package render

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
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

// templatesHash fingerprints the embedded templates and assets: pages drawn
// by an older tgbup are redrawn even when their messages did not change.
var templatesHash = func() string {
	h := sha256.New()
	_ = fs.WalkDir(files, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := files.ReadFile(path)
		h.Write([]byte(path))
		h.Write(b)
		return err
	})
	return hex.EncodeToString(h.Sum(nil))[:12]
}()
