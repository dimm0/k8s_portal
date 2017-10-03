package main

import (
	"encoding/json"
	"html/template"
	"log"
	"net/http"
	"path"
	"time"

	"github.com/boltdb/bolt"
	"github.com/spf13/viper"
)

type NamespaceAdmin struct {
	User []PrpUser
}

type AdminTemplateVars struct {
	IndexTemplateVars
	Namespace map[string]NamespaceAdmin
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

		admins, err := getClusterAdmins()
		if err != nil {
			log.Printf("Error getting the admins: %s", err.Error())
		}

		vars := AdminTemplateVars{buildIndexTemplateVars(session), map[string]NamespaceAdmin{}}

		if db, err := bolt.Open(path.Join(viper.GetString("storage_path"), "users.db"), 0600, &bolt.Options{Timeout: 5 * time.Second}); err == nil {
			defer db.Close()

			if err = db.View(func(tx *bolt.Tx) error {
				b := tx.Bucket([]byte("Users"))

				c := b.Cursor()

				for k, v := c.First(); k != nil; k, v = c.Next() {
					var user PrpUser
					if err = json.Unmarshal(v, &user); err != nil {
						return err
					}

					if val, ok := admins[user.ISS+"#"+user.UserID]; ok {
						user.IsAdmin = val
					}

					userNs := getUserNamespace(user.Email)
					if ns, ok := vars.Namespace[userNs]; ok {
						ns.User = append(ns.User, user)
						vars.Namespace[userNs] = ns
					} else {
						vars.Namespace[userNs] = NamespaceAdmin{[]PrpUser{user}}
					}
				}

				return nil
			}); err != nil {
				log.Printf("failed to read the users DB %s", err.Error())
			}
		} else {
			log.Printf("failed to connect database %s", err.Error())
		}

		err = t.Execute(w, vars)
		if err != nil {
			w.Write([]byte(err.Error()))
		}
	}
}
