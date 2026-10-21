package work

import (
	"html/template"
	"net/http"
	"strings"
)

var briefTemplate = template.Must(template.New("brief").Parse(`<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>{{.Name}} · Yamnaya</title><style>:root{color-scheme:light dark}body{font:16px/1.7 system-ui;margin:0;background:light-dark(#f5f4eb,#141c1a);color:light-dark(#263c32,#dae6dd)}main{max-width:850px;padding:32px 24px;margin:auto}header{font:13px monospace;white-space:pre-wrap;color:light-dark(#426b50,#a4c8a5)}h1{font:36px/1.2 Georgia}h2{font-size:17px;margin-top:28px}p{white-space:pre-wrap}small{opacity:.8}a{color:light-dark(#285c40,#ade0b6)}section{border-top:1px solid light-dark(#c6d1c2,#3a4b3e);margin-top:30px}pre{white-space:pre-wrap;overflow-wrap:anywhere}</style><main><header>       /\          /\
  /\  /  \   /\   /  \    yamnaya / manager brief
 /  \/    \_/  \_/    \</header><small>{{.ID}} · revision {{.Revision}} · responsible {{.ResponsibleName}} · {{.State}}</small><h1>{{.Name}}</h1>{{if .CoverageErrors}}<section><h2>Coverage unavailable</h2>{{range .CoverageErrors}}<p>{{.}}</p>{{end}}</section>{{end}}{{if not .BriefCurrent}}<p><strong>Assessment needs refreshing against current context or evidence.</strong></p>{{end}}<p>{{.Brief.Assessment}}</p><h2>Changed</h2><p>{{.Brief.Changed}}</p><h2>Purpose</h2><p>{{.Purpose}}</p><h2>Approach and rationale</h2><p>{{.Brief.Rationale}}</p><h2>Uncertainty</h2><p>{{.Brief.Uncertainty}}</p><h2>Next action</h2><p>{{if .Next}}{{.Next}}{{else}}{{.Brief.Next}}{{end}}</p>{{if .WaitFor}}<h2>Waiting for</h2><p>{{.WaitFor}}</p>{{end}}{{if .Result}}<h2>Result evidence</h2><p>{{.Result}}</p>{{if .VerifiedBy}}<p>Verified by {{.VerifiedBy}}</p>{{else}}<p>Awaiting independent verification</p>{{end}}{{end}}<h2>Delegated authority</h2><p>{{.Authority}}</p>{{if .Related}}<section><h2>Related work</h2>{{range $id,$meaning:=.Related}}<p><a href="/brief/{{$id}}">{{$id}}</a> · {{$meaning}}</p>{{end}}</section>{{end}}<section><h2>Current context · revision {{.Context.Revision}}</h2><small>Direction recorded by {{.Context.Actor}}</small><p>{{.Context.Text}}</p></section><section><h2>Review obligations</h2>{{range .Reviews}}<h2>{{.Title}}</h2><small>{{.Kind}} · {{.State}} · requested by {{.Requester}} · prepared by {{.Preparer}} · {{.Version}}</small><p>{{.Question}}</p>{{if .Handoff}}<h2>Agent handoff</h2><p>{{.Handoff.Quote}}</p><small>Requested by {{.Handoff.Agent}} · event {{.Handoff.Event}} · {{.Handoff.Path}} · byte {{.Handoff.Offset}}</small>{{end}}{{if .StatusError}}<p><strong>Status unavailable: {{.StatusError}}</strong></p>{{end}}<p>{{.Recommendation}}</p><p>{{.Evidence}}</p>{{if .ReviewedVersion}}<p>Reviewed source {{.ReviewedVersion}} · context revision {{.ReviewedContext}}</p>{{end}}{{if .Feedback}}<h2>Previous feedback</h2><p>{{.Feedback}}</p>{{end}}{{if .Judgment}}<h2>Recorded judgment</h2><p>{{.Judgment}}</p>{{end}}{{if .FollowThrough}}<h2>Follow-through</h2><p>{{.FollowThrough}}</p>{{end}}<p><a href="{{.URL}}">Original source</a></p>{{end}}</section></main></html>`))

func BriefHandler(store *Store) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /brief/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/brief/")
		scope, err := store.Get(id)
		if err == nil {
			scope.CoverageErrors, err = store.Coverage(scope.ID)
		}
		if err != nil {
			peers, peerErr := store.Peers()
			if peerErr == nil {
				for _, peer := range peers {
					for _, candidate := range peer.Scopes {
						if peer.Host+"/"+candidate.ID == id {
							scope = candidate
							scope.Host = peer.Host
							scope.ID = id
							if peer.Error != "" {
								scope.CoverageErrors = append(scope.CoverageErrors, peer.Host+": "+peer.Error)
							}
							err = nil
						}
					}
				}
			}
		}
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if scope.Related == nil {
			scope.Related = map[string]string{}
		}
		if scope.Parent != "" {
			scope.Related[scope.Parent] = "Delegated by"
		}
		if all, listErr := store.List(); listErr == nil {
			for _, child := range all {
				if child.Parent == scope.ID || ((scope.Kind == "chief" || scope.Kind == "agent") && child.Owner == scope.Owner && child.ID != scope.ID) {
					scope.Related[child.ID] = child.Name + " · " + child.State
				}
			}
		}
		if links, linkErr := store.Attachments(); linkErr == nil {
			for _, link := range links {
				if link.Agent == scope.Owner && scope.ID == scope.Owner {
					scope.Related[link.Work] = link.Reason
				}
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'; base-uri 'none'")
		name := scope.Responsible()
		if a, agentErr := store.Agent(scope.Owner); agentErr == nil {
			name = a.Name
		}
		if name == "" {
			name = "unassigned"
		}
		briefTemplate.Execute(w, struct {
			Scope
			ResponsibleName string
		}{scope, name})
	})
	return mux
}
