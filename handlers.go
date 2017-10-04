package main

import (
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/boltdb/bolt"
	oidc "github.com/coreos/go-oidc"
	"github.com/gorilla/sessions"
	"k8s.io/client-go/tools/clientcmd/api"
	clientcmdlatest "k8s.io/client-go/tools/clientcmd/api/latest"

	"github.com/spf13/viper"
	"golang.org/x/oauth2"
	"k8s.io/api/core/v1"
	rbacv1beta1 "k8s.io/api/rbac/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd"
)

//keep config file to be requested later by JS
var keys = map[string][]byte{}

//OIDC states
var states = map[string]string{}

var keysLock = sync.RWMutex{}
var statesLock = sync.RWMutex{}

type PrpUser struct {
	UserID  string
	ISS     string
	Email   string
	Name    string
	IDP     string
	IsAdmin bool
}

type IndexTemplateVars struct {
	User PrpUser
}

type ConfigTemplateVars struct {
	IndexTemplateVars
	ConfigId string
}

type ServicesTemplateVars struct {
	IndexTemplateVars
	Pods         []v1.Pod
	GrafanaUrl   string
	PerfsonarUrl string
}

func buildIndexTemplateVars(session *sessions.Session) IndexTemplateVars {
	returnVars := IndexTemplateVars{User: PrpUser{}}
	if session.Values["email"] == nil {
		return returnVars
	}

	if db, err := bolt.Open(path.Join(viper.GetString("storage_path"), "users.db"), 0600, &bolt.Options{Timeout: 5 * time.Second}); err == nil {
		defer db.Close()

		if err = db.View(func(tx *bolt.Tx) error {
			b := tx.Bucket([]byte("Users"))

			v := b.Get([]byte(session.Values["email"].(string)))
			if err = json.Unmarshal(v, &returnVars.User); err != nil {
				return err
			}

			return nil
		}); err != nil {
			log.Printf("failed to read the users DB %s", err.Error())
		}
	} else {
		log.Printf("failed to connect database %s", err.Error())
	}

	if admins, err := getClusterAdmins(); err != nil {
		log.Printf("Error getting the admins: %s", err.Error())
	} else {
		if val, ok := admins[returnVars.User.ISS+"#"+returnVars.User.UserID]; ok {
			returnVars.User.IsAdmin = val
		}
	}

	return returnVars
}

func RootHandler(w http.ResponseWriter, r *http.Request) {
	session, err := filestore.Get(r, "prp-session")
	if err != nil {
		log.Printf("Error getting the session: %s", err.Error())
	}

	t, err := template.ParseFiles("templates/layout.tmpl", "templates/home.tmpl")
	if err != nil {
		w.Write([]byte(err.Error()))
	} else {
		err = t.Execute(w, buildIndexTemplateVars(session))
		if err != nil {
			w.Write([]byte(err.Error()))
		}
	}
}

//handles the http requests for configuration file
func LogoutHandler(w http.ResponseWriter, r *http.Request) {
	session, err := filestore.Get(r, "prp-session")
	if err != nil {
		log.Printf("Error getting the session: %s", err.Error())
	}
	session.Options.MaxAge = -1
	if e := session.Save(r, w); e != nil {
		http.Error(w, "Failed to save session: "+e.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/", http.StatusFound)
}

//handles the http requests for configuration file
func GetConfigHandler(w http.ResponseWriter, r *http.Request) {

	if r.Method != "GET" {
		return
	}

	session, err := filestore.Get(r, "prp-session")
	if err != nil {
		log.Printf("Error getting the session: %s", err.Error())
	}

	if session.IsNew {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}

	id := r.URL.Query().Get("id")

	configFile, ok := keys[id]
	if ok {
		w.Header().Add("Content-Disposition", "attachment; filename=\"config\"")
		w.Header().Add("Content-Type", "application/yaml")
		w.Write(configFile)
		delete(keys, id)
	} else {
		http.Error(w, "Not found", http.StatusNotFound)
	}
}

func getClusterAdmins() (map[string]bool, error) {
	var admins = map[string]bool{}
	binding, err := clientset.Rbac().ClusterRoleBindings().Get("cilogon-admins", metav1.GetOptions{})

	if err != nil {
		return admins, err
	}

	for _, subj := range binding.Subjects {
		admins[subj.Name] = true
	}
	return admins, err
}

//handles the http requests for get services
func ServicesHandler(w http.ResponseWriter, r *http.Request) {

	if r.Method != "GET" {
		return
	}

	session, err := filestore.Get(r, "prp-session")
	if err != nil {
		log.Printf("Error getting the session: %s", err.Error())
	}

	if session.IsNew || session.Values["email"] == nil {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}

	grafanaURL := "Not found"
	grafanaService, err := clientset.Services("monitoring").Get("grafana", metav1.GetOptions{})
	if err != nil {
		log.Printf("Error getting Grafana service: %s", err.Error())
	} else {
		for _, port := range grafanaService.Spec.Ports {
			if port.NodePort > 30000 {
				grafanaURL = fmt.Sprintf("http://%s:%d", viper.GetString("cluster_url"), port.NodePort)
			}
		}
	}

	perfsonarURL := "Not found"
	perfsonarService, err := clientset.Services("perfsonar").Get("perfsonar-toolkit", metav1.GetOptions{})
	if err != nil {
		log.Printf("Error getting Perfsonar service: %s", err.Error())
	} else {
		for _, port := range perfsonarService.Spec.Ports {
			if port.NodePort > 30000 {
				perfsonarURL = fmt.Sprintf("https://%s:%d", viper.GetString("cluster_url"), port.NodePort)
			}
		}
	}

	podsList, _ := clientset.Core().Pods(getUserNamespace(session.Values["email"].(string))).List(metav1.ListOptions{})

	t, err := template.ParseFiles("templates/layout.tmpl", "templates/services.tmpl")
	if err != nil {
		w.Write([]byte(err.Error()))
	} else {
		err = t.Execute(w, ServicesTemplateVars{Pods: podsList.Items, GrafanaUrl: grafanaURL, PerfsonarUrl: perfsonarURL, IndexTemplateVars: buildIndexTemplateVars(session)})
		if err != nil {
			w.Write([]byte(err.Error()))
		}
	}
}

func getUserNamespace(email string) string {

	userDomain := strings.Split(email, "@")[1]

	reg, _ := regexp.Compile("[^a-zA-Z0-9-]+")
	userNamespace := reg.ReplaceAllString(userDomain, "-")
	return userNamespace
}

func AuthenticateHandler(w http.ResponseWriter, r *http.Request) {

	if r.Method != "GET" {
		return
	}

	session, err := filestore.Get(r, "prp-session")
	if err != nil {
		log.Printf("Error getting the session: %s", err.Error())
	}

	var stateVal string
	handleState := func() {
		statesLock.Lock()
		defer statesLock.Unlock()

		stateValTemp, ok := states[r.URL.Query().Get("state")]
		if !ok {
			http.Error(w, "state did not match", http.StatusBadRequest)
			return
		}
		stateVal = stateValTemp
	}
	handleState()

	curConfig := config
	if stateVal == "config" {
		curConfig = pubconfig
	}

	oauth2Token, err := curConfig.Exchange(r.Context(), r.URL.Query().Get("code"))
	if err != nil {
		http.Error(w, "Failed to exchange token: "+err.Error()+" for code "+r.URL.Query().Get("code"), http.StatusInternalServerError)
		return
	}

	oidcConfig := &oidc.Config{
		ClientID: curConfig.ClientID,
	}
	verifier := provider.Verifier(oidcConfig)

	idToken, err := verifier.Verify(r.Context(), oauth2Token.Extra("id_token").(string))
	if err != nil {
		http.Error(w, "Failed to verify ID Token: "+err.Error(), http.StatusInternalServerError)
		return
	}

	userInfo, err := provider.UserInfo(r.Context(), oauth2.StaticTokenSource(oauth2Token))
	if err != nil {
		http.Error(w, "Failed to get userinfo: "+err.Error(), http.StatusInternalServerError)
		return
	}
	userID := idToken.Issuer + "#" + idToken.Subject

	clusterInfoConfig, err := clientset.ConfigMaps("kube-public").Get("cluster-info", metav1.GetOptions{})
	if err != nil {
		http.Error(w, "Failed to get cluster config: "+err.Error(), http.StatusInternalServerError)
		return
	}

	switch stateVal {
	case "auth":
		userNamespace := getUserNamespace(userInfo.Email)

		if _, err := clientset.Core().Namespaces().Get(userNamespace, metav1.GetOptions{}); err != nil {
			clientset.Core().Namespaces().Create(&v1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: userNamespace}})
		}

		binding, err := clientset.Rbac().RoleBindings(userNamespace).Get("cilogon", metav1.GetOptions{})
		if err != nil {
			binding, err = clientset.Rbac().RoleBindings(userNamespace).Create(&rbacv1beta1.RoleBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: "cilogon",
				},
				RoleRef: rbacv1beta1.RoleRef{
					APIGroup: "rbac.authorization.k8s.io",
					Kind:     "ClusterRole",
					Name:     "cilogon-edit",
				},
				Subjects: []rbacv1beta1.Subject{rbacv1beta1.Subject{Kind: "User", APIGroup: "rbac.authorization.k8s.io", Name: userID}},
			})
			if err != nil {
				log.Printf("Error creating userbinding %s\n", err.Error())
			}
		} else {
			found := false
			for _, subj := range binding.Subjects {
				if subj.Name == userID {
					found = true
				}
			}

			if !found {
				newUser := UserPatchJson{}
				newUser.Op = "add"
				newUser.Path = "/subjects/-"
				newUser.Value.Kind = "User"
				newUser.Value.Name = userID
				newUser.Value.ApiGroup = "rbac.authorization.k8s.io"
				userStr, _ := json.Marshal([]UserPatchJson{newUser})
				log.Printf("Doing patch %s", userStr)

				patchres, err := clientset.Rbac().RoleBindings(userNamespace).Patch("cilogon", types.JSONPatchType, userStr, "")
				if err != nil {
					log.Printf("Error doing patch %s\n", err.Error())
				} else {
					log.Printf("Success doing patch %v\n", patchres)
				}
			}
		}

		session.Values["email"] = userInfo.Email
		if e := session.Save(r, w); e != nil {
			http.Error(w, "Failed to save session: "+e.Error(), http.StatusInternalServerError)
			return
		}
		log.Printf("Saved session")

		var Claims struct {
			Name string `json:"name"`
			IDP  string `json:"idp_name"`
		}
		if err := userInfo.Claims(&Claims); err != nil {
			log.Printf("Error getting userInfo from claims %s", err.Error())
		}

		if db, err := bolt.Open(path.Join(viper.GetString("storage_path"), "users.db"), 0600, &bolt.Options{Timeout: 5 * time.Second}); err == nil {
			defer db.Close()

			if err = db.Update(func(tx *bolt.Tx) error {
				b, err := tx.CreateBucketIfNotExists([]byte("Users"))
				if err != nil {
					return fmt.Errorf("create bucket: %s", err)
				}

				if buf, err := json.Marshal(PrpUser{Email: userInfo.Email, UserID: userInfo.Subject, Name: Claims.Name, IDP: Claims.IDP, ISS: idToken.Issuer}); err != nil {
					return err
				} else if err := b.Put([]byte(userInfo.Email), buf); err != nil {
					return err
				}
				return nil
			}); err != nil {
				log.Printf("failed to update the users DB %s", err.Error())
			}
		} else {
			log.Printf("failed to connect database %s", err.Error())
		}

		http.Redirect(w, r, "/", http.StatusFound)
	case "config":
		co, err := clientcmd.Load([]byte(clusterInfoConfig.Data["kubeconfig"]))
		clust := *co.Clusters[""]
		co.Clusters[viper.GetString("cluster_name")] = &clust
		delete(co.Clusters, "")
		co.Contexts = map[string]*api.Context{
			viper.GetString("cluster_name"): &api.Context{
				Cluster:   viper.GetString("cluster_name"),
				AuthInfo:  userInfo.Subject,
				Namespace: getUserNamespace(session.Values["email"].(string)),
			},
		}
		co.AuthInfos = map[string]*api.AuthInfo{userInfo.Subject: {
			AuthProvider: &api.AuthProviderConfig{
				Name: "oidc",
				Config: map[string]string{
					"id-token":       oauth2Token.Extra("id_token").(string),
					"refresh-token":  oauth2Token.RefreshToken,
					"client-id":      viper.GetString("pub_client_id"),
					"client-secret":  viper.GetString("pub_client_secret"),
					"idp-issuer-url": idToken.Issuer,
				},
			},
		}}
		co.CurrentContext = viper.GetString("cluster_name")

		data, err := runtime.Encode(clientcmdlatest.Codec, co)
		if err == nil {
			newId := randStringBytes(16)
			keysLock.Lock()
			defer keysLock.Unlock()

			keys[newId] = data
			cleanKeyTimer := time.NewTimer(time.Second * 5)
			go func() {
				<-cleanKeyTimer.C
				keysLock.Lock()
				defer keysLock.Unlock()
				delete(keys, newId)
			}()

			t, err := template.ParseFiles("templates/layout.tmpl", "templates/authenticated.tmpl")
			if err != nil {
				w.Write([]byte(err.Error()))
			} else {
				err = t.Execute(w, ConfigTemplateVars{ConfigId: newId, IndexTemplateVars: buildIndexTemplateVars(session)})
				if err != nil {
					w.Write([]byte(err.Error()))
				}
			}

		} else {
			w.Write([]byte(err.Error()))
		}
	default:
		w.Write([]byte("Error value for state: " + stateVal))
	}

}
