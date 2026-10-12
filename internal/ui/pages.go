package ui

import (
	"html/template"
	"net/http"
)

type pageData struct {
	Title, Message string
	ShowTokenForm  bool
}

var pageTmpl = template.Must(template.New("page").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>{{.Title}} · agentbox</title>
<style>
:root{color-scheme:light dark;font:15px/1.5 system-ui,sans-serif}
body{margin:0;display:grid;place-items:center;min-height:100vh;background:#f6f7f9;color:#1b1f24}
@media(prefers-color-scheme:dark){body{background:#0f1115;color:#e6e8eb}}
main{max-width:34rem;padding:2rem}
.brand{font-family:ui-monospace,SFMono-Regular,Menlo,monospace;font-size:.85rem;opacity:.6;margin:0 0 .5rem}
h1{font-size:1.4rem;margin:0 0 .75rem}
pre,code,input{font-family:ui-monospace,SFMono-Regular,Menlo,monospace;font-size:.85rem}
pre{padding:.75rem;border-radius:6px;background:rgba(127,127,127,.12);overflow:auto}
form{margin-top:1.5rem;display:flex;gap:.5rem}input{flex:1;padding:.4rem .5rem}
button{padding:.4rem .8rem}
</style></head>
<body><main><p class="brand">agentbox</p><h1>{{.Title}}</h1><p>{{.Message}}</p>
{{if .ShowTokenForm}}<p>Run this in a terminal to open a signed-in dashboard:</p><pre>agentbox ui</pre>
<p>Or paste the contents of <code>~/.agentbox/token</code>:</p>
<form method="post" action="/ui/login"><input type="password" name="token" placeholder="token" autocomplete="off"><button>Sign in</button></form>{{end}}
</main></body></html>
`))

func page(w http.ResponseWriter, status int, d pageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	noFraming(w)
	w.WriteHeader(status)
	_ = pageTmpl.Execute(w, d)
}

// noFraming stops another origin from embedding dashboard pages (clickjacking)
// and from having them sniffed into another content type.
func noFraming(w http.ResponseWriter) {
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}

func forbidden(w http.ResponseWriter, why string) {
	page(w, http.StatusForbidden, pageData{Title: "Sign in to agentbox", Message: why, ShowTokenForm: true})
}

var notBuilt = pageData{Title: "No dashboard in this build",
	Message: "This agentbox binary was built without the dashboard. Install a release build, or run `make ui` (needs Node 20+) and rebuild."}
