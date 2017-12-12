package main

import (
	"html/template"
	"log"
	"net/http"
)

type AdminTemplateVars struct {
	IndexTemplateVars
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

		vars := AdminTemplateVars{buildIndexTemplateVars(session, w, r)}

		err = t.Execute(w, vars)
		if err != nil {
			w.Write([]byte(err.Error()))
		}
	}
}
