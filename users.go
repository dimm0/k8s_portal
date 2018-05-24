package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"strings"

	nautilusapi "github.com/dimm0/k8s_portal/pkg/apis/optiputer.net/v1alpha1"
	meta_v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type UsersTemplateVars struct {
	IndexTemplateVars
	Users     []nautilusapi.PRPUser
	MailtoAll string
}

type AutoCompleteItem struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

type NamespaceUsers struct {
	Users  []nautilusapi.PRPUser `json:"users"`
	Admins []nautilusapi.PRPUser `json:"admins"`
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

	userclientset, err := user.GetUserClientset()
	if err != nil {
		session.AddFlash(fmt.Sprintf("Unexpected error: %s", err.Error()))
		session.Save(r, w)
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}

	switch r.Method {
	case "GET":
		if r.URL.Query().Get("format") == "json" {

			switch r.URL.Query().Get("action") {
			case "autocomplete":
				term := r.URL.Query().Get("term")
				if term == "" {
					w.WriteHeader(http.StatusInternalServerError)
					w.Write([]byte("Please provide term"))
					return
				}
				users := []nautilusapi.PRPUser{}
				autocompleteUsers := []AutoCompleteItem{}
				if curusers, err := crdclient.List(meta_v1.ListOptions{}); err == nil {
					users = curusers.Items
					for _, user := range users {
						if strings.Contains(strings.ToLower(user.Spec.Name+" "+user.Spec.Email), strings.ToLower(term)) {
							autocompleteUsers = append(autocompleteUsers, AutoCompleteItem{user.Spec.UserID, user.Spec.Name + " &lt;" + user.Spec.Email + "&gt;"})
						}
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
			case "general":
				users := []nautilusapi.PRPUser{}
				if curusers, err := crdclient.List(meta_v1.ListOptions{}); err == nil {
					users = curusers.Items
					if usersJson, err := json.Marshal(users); err == nil {
						w.Write(usersJson)
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
			case "namespace":
				if r.URL.Query().Get("namespace") == "" {
					w.WriteHeader(http.StatusBadRequest)
					w.Write([]byte("Not enough params"))
					return
				}

				nsUsers := NamespaceUsers{}

				for _, role := range []string{"user", "admin"} {
					if userBindings, err := userclientset.Rbac().RoleBindings(r.URL.Query().Get("namespace")).Get("nautilus-"+role, meta_v1.GetOptions{}); err == nil {
						if len(userBindings.Subjects) > 0 {
							users := []nautilusapi.PRPUser{}
							for _, userBinding := range userBindings.Subjects {
								if user, err := GetUser(userBinding.Name); err == nil {
									users = append(users, *user)
								} else {
									w.WriteHeader(http.StatusInternalServerError)
									w.Write([]byte(fmt.Sprintf("Error getting user: %s", err.Error())))
									return
								}
							}
							switch role {
							case "user":
								nsUsers.Users = users
							case "admin":
								nsUsers.Admins = users
							}
						}
					}
				}

				if nsUsersJson, err := json.Marshal(nsUsers); err == nil {
					w.Write(nsUsersJson)
					return
				} else {
					w.WriteHeader(http.StatusInternalServerError)
					w.Write([]byte(fmt.Sprintf("Error getting users: %s", err.Error())))
					return
				}

			}
		}

		t, err := template.ParseFiles("templates/layout.tmpl", "templates/users.tmpl")
		if err != nil {
			w.Write([]byte(err.Error()))
		} else {

			users := []nautilusapi.PRPUser{}
			var mailAllBuf bytes.Buffer

			if curusers, err := crdclient.List(meta_v1.ListOptions{}); err == nil {
				users = curusers.Items

				for _, user := range users {
					mailAllBuf.WriteString(user.Spec.Name + "<" + user.Spec.Email + ">,")
				}
			} else {
				session.AddFlash(fmt.Sprintf("Unexpected error: %s", err.Error()))
				session.Save(r, w)
			}

			vars := UsersTemplateVars{buildIndexTemplateVars(session, w, r), users, mailAllBuf.String()}

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
