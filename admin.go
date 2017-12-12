package main

import (
	"fmt"
	"html/template"
	"log"
	"net/http"

	client "github.com/dimm0/k8s_portal/pkg/apis/optiputer.net/v1alpha1"
	meta_v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type AdminTemplateVars struct {
	IndexTemplateVars
	Users []client.PRPUser
}

func AdminHandler(w http.ResponseWriter, r *http.Request) {
	session, err := filestore.Get(r, "prp-session")
	if err != nil {
		log.Printf("Error getting the session: %s", err.Error())
	}

	if session.IsNew || session.Values["userid"] == nil {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}

	t, err := template.ParseFiles("templates/layout.tmpl", "templates/admin.tmpl")
	if err != nil {
		w.Write([]byte(err.Error()))
	} else {

		users := []client.PRPUser{}
		if curusers, err := crdclient.List(meta_v1.ListOptions{}); err != nil {
			users = curusers.Items
		} else {
			session.AddFlash(fmt.Sprintf("Unexpected error: %s", err.Error()))
			session.Save(r, w)
		}

		vars := AdminTemplateVars{buildIndexTemplateVars(session, w, r), users}

		err = t.Execute(w, vars)
		if err != nil {
			w.Write([]byte(err.Error()))
		}
	}
}
