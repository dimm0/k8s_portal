package main

import (
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net"
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
	rbacv1 "k8s.io/api/rbac/v1"
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
}

type IndexTemplateVars struct {
	User       PrpUser
	ClusterUrl string
	Flashes []string
}

type ConfigTemplateVars struct {
	IndexTemplateVars
	ConfigId string
}

type PodsTemplateVars struct {
	IndexTemplateVars
	Pods       []v1.Pod
	Namespace  string
	Namespaces []v1.Namespace
}

type NodesTemplateVars struct {
	IndexTemplateVars
	Nodes []v1.Node
}

type NamespacesTemplateVars struct {
	IndexTemplateVars
	NamespaceBindings []NamespaceUserBinding
}

type NamespaceUserBinding struct {
	Namespace v1.Namespace
	RoleBindings []rbacv1.RoleBinding
	ClusterRoleBindings []rbacv1.ClusterRoleBinding
}

func getUser(userid string) (PrpUser, error) {
	var user PrpUser
	if db, err := bolt.Open(path.Join(viper.GetString("storage_path"), "users.db"), 0600, &bolt.Options{Timeout: 5 * time.Second}); err == nil {
		defer db.Close()

		if err = db.View(func(tx *bolt.Tx) error {
			b := tx.Bucket([]byte("Users"))

			v := b.Get([]byte(userid))
			if err = json.Unmarshal(v, &user); err != nil {
				return err
			}

			return nil
		}); err != nil {
			return user, err
		}
	}

	return user, nil
}

func (user PrpUser) IsAdmin(namespace string) bool {
	if user.IsClusterAdmin() {
		return true
	}
	if namespace != "" {
		if bindings, err := clientset.Rbac().RoleBindings(namespace).List(metav1.ListOptions{}); err != nil {
			return false
		} else {
			for _, bind := range bindings.Items {
				if bind.RoleRef.Name == "admin" || bind.RoleRef.Name == "cluster-admin" {
					for _, subj := range bind.Subjects {
						if user.ISS + "#" + user.UserID == subj.Name {
							return true
						}
					}
				}
			}
		}
	}
	return false
}

func (user PrpUser) IsClusterAdmin() bool {
	if bindings, err := clientset.Rbac().ClusterRoleBindings().List(metav1.ListOptions{}); err != nil {
		return false
	} else {
		for _, bind := range bindings.Items {
			if !strings.HasPrefix(bind.Name, "system") && bind.RoleRef.Name == "admin" || bind.RoleRef.Name == "cluster-admin" {
				for _, subj := range bind.Subjects {
					if user.ISS + "#" + user.UserID == subj.Name {
						return true
					}
				}
			}
		}
	}
	return false
}

func buildIndexTemplateVars(session *sessions.Session, w http.ResponseWriter, r *http.Request) IndexTemplateVars {
	returnVars := IndexTemplateVars{User: PrpUser{}, ClusterUrl: viper.GetString("cluster_url")}
	if session.Values["userid"] == nil {
		return returnVars
	}

	if user, err := getUser(session.Values["userid"].(string)); err != nil {
		log.Printf("Error getting the user: %s", err.Error())
	} else {
		returnVars.User = user
	}

	if flashes := session.Flashes(); len(flashes) > 0 {
		returnVars.Flashes = []string{}
		for _, fl := range flashes {
			returnVars.Flashes = append(returnVars.Flashes, fl.(string))
		}
		session.Save(r,w)
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
		err = t.Execute(w, buildIndexTemplateVars(session, w, r))
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

//handles the http requests for get pods
func PodsHandler(w http.ResponseWriter, r *http.Request) {

	if r.Method != "GET" {
		return
	}

	session, err := filestore.Get(r, "prp-session")
	if err != nil {
		log.Printf("Error getting the session: %s", err.Error())
	}

	if session.IsNew || session.Values["userid"] == nil {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}

	var ns string
	nss := []v1.Namespace{}

	if user, err := getUser(session.Values["userid"].(string)); err == nil {
		ns = getUserNamespace(user.Email)
		if user.IsAdmin("") {
			nsList, _ := clientset.Core().Namespaces().List(metav1.ListOptions{})
			nss = nsList.Items
			if r.URL.Query().Get("namespace") != "" {
				ns = r.URL.Query().Get("namespace")
			}
		}
	} else {
		log.Printf("Error getting the user: %s", err.Error())
	}

	podsList, _ := clientset.Core().Pods(ns).List(metav1.ListOptions{})

	stVars := PodsTemplateVars{Pods: podsList.Items, Namespaces: nss, Namespace: ns, IndexTemplateVars: buildIndexTemplateVars(session, w, r)}

	t, err := template.New("layout.tmpl").ParseFiles("templates/layout.tmpl", "templates/pods.tmpl")
	if err != nil {
		w.Write([]byte(err.Error()))
	} else {
		err = t.ExecuteTemplate(w, "layout.tmpl", stVars)
		if err != nil {
			w.Write([]byte(err.Error()))
		}
	}
}

func NodesHandler(w http.ResponseWriter, r *http.Request) {

	if r.Method != "GET" {
		return
	}

	session, err := filestore.Get(r, "prp-session")
	if err != nil {
		log.Printf("Error getting the session: %s", err.Error())
	}

	if session.IsNew || session.Values["userid"] == nil {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}

	nodesList, _ := clientset.Core().Nodes().List(metav1.ListOptions{})

	stVars := NodesTemplateVars{Nodes: nodesList.Items, IndexTemplateVars: buildIndexTemplateVars(session, w, r)}

	t, err := template.New("layout.tmpl").Funcs(template.FuncMap{
		"hostToIp": func(host string) string {
			ips, err := net.LookupIP(host)
			if err != nil {
				return host
			}
			return ips[0].String()
		},
	}).ParseFiles("templates/layout.tmpl", "templates/nodes.tmpl")
	if err != nil {
		w.Write([]byte(err.Error()))
	} else {
		err = t.ExecuteTemplate(w, "layout.tmpl", stVars)
		if err != nil {
			w.Write([]byte(err.Error()))
		}
	}
}

func getUserNamespace(email string) string {

	userDomain := strings.Split(email, "@")[1]

	reg, _ := regexp.Compile("[^a-zA-Z0-9-]+")
	userNamespace := reg.ReplaceAllString(userDomain, "-")

	return strings.ToLower(userNamespace)
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

	clusterInfoConfig, err := clientset.Core().ConfigMaps("kube-public").Get("cluster-info", metav1.GetOptions{})
	if err != nil {
		http.Error(w, "Failed to get cluster config: "+err.Error(), http.StatusInternalServerError)
		return
	}

	switch stateVal {
	case "auth":
		userInfo, err := provider.UserInfo(r.Context(), oauth2.StaticTokenSource(oauth2Token))
		if err != nil {
			http.Error(w, "Failed to get userinfo: "+err.Error(), http.StatusInternalServerError)
			return
		}
		userID := idToken.Issuer + "#" + idToken.Subject

		userNamespace := getUserNamespace(userInfo.Email)

		if _, err := clientset.Core().Namespaces().Get(userNamespace, metav1.GetOptions{}); err != nil {
			clientset.Core().Namespaces().Create(&v1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: userNamespace}})
			if _, err := createNsLimits(userNamespace); err != nil {
				http.Error(w, "Failed to create namespace "+userNamespace+" limits: %s" + err.Error(), http.StatusInternalServerError)
				return
			}

		}

		binding, err := clientset.Rbac().RoleBindings(userNamespace).Get("cilogon", metav1.GetOptions{})
		if err != nil {
			binding, err = clientset.Rbac().RoleBindings(userNamespace).Create(&rbacv1.RoleBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: "cilogon",
				},
				RoleRef: rbacv1.RoleRef{
					APIGroup: "rbac.authorization.k8s.io",
					Kind:     "ClusterRole",
					Name:     "cilogon-edit",
				},
				Subjects: []rbacv1.Subject{rbacv1.Subject{Kind: "User", APIGroup: "rbac.authorization.k8s.io", Name: userID}},
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

		session.Values["userid"] = userInfo.Subject
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
				} else if err := b.Put([]byte(userInfo.Subject), buf); err != nil {
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

		ns := "default"
		if user, err := getUser(idToken.Subject); err != nil {
			log.Printf("Error getting the user: %s", err.Error())
		} else {
			ns = getUserNamespace(user.Email)
		}

		co.Contexts = map[string]*api.Context{
			viper.GetString("cluster_name"): &api.Context{
				Cluster:   viper.GetString("cluster_name"),
				AuthInfo:  idToken.Subject,
				Namespace: ns,
			},
		}
		co.AuthInfos = map[string]*api.AuthInfo{idToken.Subject: {
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
				err = t.Execute(w, ConfigTemplateVars{ConfigId: newId, IndexTemplateVars: buildIndexTemplateVars(session, w, r)})
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
