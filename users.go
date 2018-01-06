package main

import (
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"strings"

	client "github.com/dimm0/k8s_portal/pkg/apis/optiputer.net/v1alpha1"
	meta_v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type UsersTemplateVars struct {
	IndexTemplateVars
	Users []client.PRPUser
}

type AutoCompleteItem struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

func UsersHandler(w http.ResponseWriter, r *http.Request) {
	session, err := filestore.Get(r, "prp-session")
	if err != nil {
		log.Printf("Error getting the session: %s", err.Error())
	}

	if session.IsNew || session.Values["userid"] == nil {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}

	user, err := GetUser(session.Values["userid"].(string))
	if err != nil {
		session.AddFlash(fmt.Sprintf("Unexpected error: %s", err.Error()))
		session.Save(r, w)
		http.Redirect(w, r, "/", http.StatusFound)
	}

	if strings.ToLower(user.Spec.Role) != "admin" {
		session.AddFlash("Unauthorized")
		session.Save(r, w)
		http.Redirect(w, r, "/", http.StatusFound)
	}

	switch r.Method {
	case "GET":
		if r.URL.Query().Get("format") == "json" {
			users := []client.PRPUser{}
			autocompleteUsers := []AutoCompleteItem{}
			if curusers, err := crdclient.List(meta_v1.ListOptions{}); err == nil {
				users = curusers.Items
				for _, user := range users {
					autocompleteUsers = append(autocompleteUsers, AutoCompleteItem{user.Spec.UserID, user.Spec.Name + " <" + user.Spec.Email + ">"})
				}
				if autocomplUsersJson, err := json.Marshal(autocompleteUsers); err == nil {
					w.Write(autocomplUsersJson)
					return
				} else {
					w.WriteHeader(http.StatusInternalServerError)
					w.Write([]byte(fmt.Sprintf("Error getting users: %s", err.Error())))
					return
				}
			} else {
				w.WriteHeader(http.StatusInternalServerError)
				w.Write([]byte(fmt.Sprintf("Error getting users: %s", err.Error())))
				return
			}
		}

		t, err := template.ParseFiles("templates/layout.tmpl", "templates/users.tmpl")
		if err != nil {
			w.Write([]byte(err.Error()))
		} else {

			users := []client.PRPUser{}
			if curusers, err := crdclient.List(meta_v1.ListOptions{}); err == nil {
				users = curusers.Items
			} else {
				session.AddFlash(fmt.Sprintf("Unexpected error: %s", err.Error()))
				session.Save(r, w)
			}

			vars := UsersTemplateVars{buildIndexTemplateVars(session, w, r), users}

			err = t.Execute(w, vars)
			if err != nil {
				w.Write([]byte(err.Error()))
			}
		}
	case "POST":
		if err := r.ParseForm(); err != nil {
			w.Write([]byte(err.Error()))
			return
		}

		changeUser, err := GetUser(r.PostFormValue("user"))
		if err != nil {
			session.AddFlash(fmt.Sprintf("Unexpected error: %s", err.Error()))
			session.Save(r, w)
			http.Redirect(w, r, "/", http.StatusFound)
			return
		}
		if strings.ToLower(changeUser.Spec.Role) == "guest" && r.PostFormValue("action") == "validate" {
			changeUser.Spec.Role = "user"
			_, err := crdclient.Update(changeUser)
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				w.Write([]byte(fmt.Sprintf("Error updating user: %s", err.Error())))
				return
			}
		} else if strings.ToLower(changeUser.Spec.Role) == "user" && r.PostFormValue("action") == "unvalidate" {
			changeUser.Spec.Role = "guest"
			_, err := crdclient.Update(changeUser)
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				w.Write([]byte(fmt.Sprintf("Error updating user: %s", err.Error())))
				return
			}
		}
		w.Write([]byte(changeUser.Spec.Role))
	}
}
