package proxy

import (
	"html/template"
	"net/http"
)

type link struct{ Href, Text string }

type pageData struct {
	Title   string
	Message string
	Detail  string // e.g. the box's last_error
	Command string // a CLI command to show
	Links   []link
	Lines   []string // log lines
	Refresh bool     // add <meta http-equiv="refresh" content="2">
}

var pageTmpl = template.Must(template.New("page").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>{{.Title}} · agentbox</title>{{if .Refresh}}
<meta http-equiv="refresh" content="2">{{end}}
<style>
:root{color-scheme:light dark;font:15px/1.5 system-ui,sans-serif}
body{margin:0;display:grid;place-items:center;min-height:100vh;background:#f6f7f9;color:#1b1f24}
@media(prefers-color-scheme:dark){body{background:#0f1115;color:#e6e8eb}}
main{max-width:40rem;padding:2rem}
.brand{font-family:ui-monospace,SFMono-Regular,Menlo,monospace;font-size:.85rem;opacity:.6;margin:0 0 .5rem}
h1{font-size:1.4rem;margin:0 0 .75rem}
pre{font-family:ui-monospace,SFMono-Regular,Menlo,monospace;font-size:.85rem;padding:.75rem;border-radius:6px;background:rgba(127,127,127,.12);overflow:auto;white-space:pre-wrap}
ul{padding-left:1.2rem}a{color:#2563eb}@media(prefers-color-scheme:dark){a{color:#7cb0ff}}
.detail{opacity:.8}
</style></head>
<body><main><p class="brand">agentbox</p><h1>{{.Title}}</h1><p>{{.Message}}</p>
{{if .Detail}}<p class="detail">{{.Detail}}</p>{{end}}
{{if .Command}}<pre>{{.Command}}</pre>{{end}}
{{if .Links}}<ul>{{range .Links}}<li><a href="{{.Href}}">{{.Text}}</a></li>{{end}}</ul>{{end}}
{{if .Lines}}<pre>{{range .Lines}}{{.}}
{{end}}</pre>{{end}}
</main></body></html>
`))

func (h *Handler) page(w http.ResponseWriter, status int, d pageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = pageTmpl.Execute(w, d)
}
