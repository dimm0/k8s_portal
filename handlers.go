package main

import (
	"fmt"
	"html/template"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	authv1 "k8s.io/api/authorization/v1"

	oidc "github.com/coreos/go-oidc"
	nautilusapi "github.com/dimm0/k8s_portal/pkg/apis/optiputer.net/v1alpha1"
	"github.com/gorilla/sessions"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/tools/clientcmd/api"
	clientcmdlatest "k8s.io/client-go/tools/clientcmd/api/latest"

	"github.com/spf13/viper"
	"golang.org/x/oauth2"
	"k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/clientcmd"
)

//keep config file to be requested later by JS
var keys = map[string][]byte{}

//OIDC states
var states = map[string]string{}

var keysLock = sync.RWMutex{}
var statesLock = sync.RWMutex{}

type IndexTemplateVars struct {
	User       *nautilusapi.PRPUser
	ClusterUrl string
	Flashes    []string
}

type ConfigTemplateVars struct {
	IndexTemplateVars
	ConfigId string
}

type NamespacesTemplateVars struct {
	IndexTemplateVars
	Pods       []v1.Pod
	Namespace  string
	Namespaces []v1.Namespace
}

type NodesTemplateVars struct {
	IndexTemplateVars
	Nodes []v1.Node
}

func buildIndexTemplateVars(session *sessions.Session, w http.ResponseWriter, r *http.Request) IndexTemplateVars {
	returnVars := IndexTemplateVars{User: &nautilusapi.PRPUser{}, ClusterUrl: viper.GetString("cluster_url")}
	if session.Values["userid"] == nil {
		return returnVars
	}

	if user, err := GetUser(session.Values["userid"].(string)); err != nil {
		log.Printf("Error getting the user: %s", err.Error())
	} else {
		returnVars.User = user
	}

	if flashes := session.Flashes(); len(flashes) > 0 {
		returnVars.Flashes = []string{}
		for _, fl := range flashes {
			returnVars.Flashes = append(returnVars.Flashes, fl.(string))
		}
		session.Save(r, w)
	}

	return returnVars
}

func GetUser(userID string) (*nautilusapi.PRPUser, error) {
	userName := strings.Replace(userID, "://", "-", -1)
	userName = strings.Replace(userName, "/", "-", -1)
	userName = strings.Replace(userName, ".", "-", -1)

	return crdclient.Get(strings.ToLower(userName))
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

//handles the http requests for get namespace
func NamespacesHandler(w http.ResponseWriter, r *http.Request) {

	if r.Method != "GET" {
		return
	}

	session, err := filestore.Get(r, "prp-session")
	if err != nil {
		log.Printf("Error getting the session: %s", err.Error())
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}

	if session.IsNew || session.Values["userid"] == nil {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}

	nss := []v1.Namespace{}

	user, err := GetUser(session.Values["userid"].(string))
	if err != nil {
		session.AddFlash(fmt.Sprintf("Unexpected error: %s", err.Error()))
		session.Save(r, w)
		http.Redirect(w, r, "/", http.StatusFound)
	}

	var reqNsName = r.URL.Query().Get("req")
	if reqNsName != "" {
		session.AddFlash(fmt.Sprintf("Requesting the membership is not implemented yet. Please send email to the owner directly."))
		session.Save(r, w)
	}

	userclientset, err := user.GetUserClientset()
	if err != nil {
		session.AddFlash(fmt.Sprintf("Unexpected error: %s", err.Error()))
		session.Save(r, w)
		http.Redirect(w, r, "/", http.StatusFound)
	}

	nsList, err := clientset.Core().Namespaces().List(metav1.ListOptions{})
	if err != nil {
		session.AddFlash(fmt.Sprintf("Unexpected error: %s", err.Error()))
		session.Save(r, w)
		http.Redirect(w, r, "/", http.StatusFound)
	}

	nss = nsList.Items
	var ns = getUserNamespace(*user)
	if r.URL.Query().Get("namespace") != "" {
		ns = r.URL.Query().Get("namespace")
	}

	podsList, err := userclientset.Core().Pods(ns).List(metav1.ListOptions{})
	if err != nil {
		session.AddFlash(fmt.Sprintf("Unexpected error: %s", err.Error()))
		session.Save(r, w)
	}

	stVars := NamespacesTemplateVars{Pods: podsList.Items, Namespaces: nss, Namespace: ns, IndexTemplateVars: buildIndexTemplateVars(session, w, r)}

	t, err := template.New("layout.tmpl").Funcs(template.FuncMap{
		"hostToIp": hostToIp,
	}).ParseFiles("templates/layout.tmpl", "templates/namespaces.tmpl")
	if err != nil {
		session.AddFlash(fmt.Sprintf("Unexpected error: %s", err.Error()))
		session.Save(r, w)
		http.Redirect(w, r, "/", http.StatusFound)
	} else {
		err = t.ExecuteTemplate(w, "layout.tmpl", stVars)
		if err != nil {
			session.AddFlash(fmt.Sprintf("Unexpected error: %s", err.Error()))
			session.Save(r, w)
			http.Redirect(w, r, "/", http.StatusFound)
		}
	}

}

func hostToIp(host string) string {
	ips, err := net.LookupIP(host)
	if err != nil {
		return host
	}
	return ips[0].String()
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
		"hostToIp": hostToIp,
		"isGPU": func(res v1.ResourceList) bool {
			gpus := res["nvidia.com/gpu"]
			return !gpus.IsZero()
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

func getUserNamespace(user nautilusapi.PRPUser) string {
	if userclientset, err := user.GetUserClientset(); err != nil {
		log.Printf("Error getting the user clientset: %s", err.Error())
		return "default"
	} else {
		if nslist, err := clientset.Core().Namespaces().List(metav1.ListOptions{}); err == nil {
			for _, ns := range nslist.Items {
				if rev, err := userclientset.AuthorizationV1().SelfSubjectAccessReviews().Create(&authv1.SelfSubjectAccessReview{
					Spec: authv1.SelfSubjectAccessReviewSpec{
						ResourceAttributes: &authv1.ResourceAttributes{
							Namespace: ns.ObjectMeta.Name,
							Verb:      "list",
							Group:     "",
							Resource:  "pods",
						},
					},
				}); err == nil {
					if rev.Status.Allowed {
						return ns.ObjectMeta.Name
					}
				}
			}
			return "default"
		} else {
			log.Printf("Error getting the user namespaces: %s", err.Error())
			return "default"
		}
	}
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

	switch stateVal {
	case "auth":
		userInfo, err := provider.UserInfo(r.Context(), oauth2.StaticTokenSource(oauth2Token))
		if err != nil {
			http.Error(w, "Failed to get userinfo: "+err.Error(), http.StatusInternalServerError)
			return
		}

		session.Values["userid"] = userInfo.Subject
		if e := session.Save(r, w); e != nil {
			http.Error(w, "Failed to save session: "+e.Error(), http.StatusInternalServerError)
			return
		}

		var Claims struct {
			Name      string `json:"name"`
			FirstName string `json:"given_name"`
			LastName  string `json:"family_name"`
			IDP       string `json:"idp_name"`
		}
		if err = userInfo.Claims(&Claims); err != nil {
			log.Printf("Error getting userInfo from claims %s", err.Error())
		}

		userName := strings.Replace(userInfo.Subject, "://", "-", -1)
		userName = strings.Replace(userName, "/", "-", -1)
		userName = strings.Replace(userName, ".", "-", -1)

		user := &nautilusapi.PRPUser{
			ObjectMeta: metav1.ObjectMeta{
				Name: strings.ToLower(userName),
			},
			Spec: nautilusapi.PRPUserSpec{
				UserID: userInfo.Subject,
				ISS:    idToken.Issuer,
				Email:  userInfo.Email,
				Name:   Claims.Name,
				IDP:    Claims.IDP,
				Role:   "guest",
			},
		}

		if user.Spec.Name == "" {
			if Claims.FirstName != "" {
				user.Spec.Name = Claims.FirstName
				if Claims.LastName != "" {
					user.Spec.Name += " " + Claims.LastName
				}
			}
		}

		result, err := crdclient.Create(user)
		if err == nil {
			fmt.Printf("CREATED USER: %#v\n", result)
		} else if apierrors.IsAlreadyExists(err) {
			// fmt.Printf("ALREADY EXISTS USER: %#v\n", result)
		} else {
			fmt.Printf("ERROR CREATING USER: %s\n", err.Error())
		}

		http.Redirect(w, r, "/", http.StatusFound)
	case "config":
		clusterInfoConfig, err := clientset.Core().ConfigMaps("kube-public").Get("cluster-info", metav1.GetOptions{})
		if err != nil {
			http.Error(w, "Failed to get cluster config: "+err.Error(), http.StatusInternalServerError)
			return
		}

		co, err := clientcmd.Load([]byte(clusterInfoConfig.Data["kubeconfig"]))
		clust := *co.Clusters[""]
		co.Clusters[viper.GetString("cluster_name")] = &clust
		delete(co.Clusters, "")

		ns := "default"
		if user, err := GetUser(idToken.Subject); err != nil {
			log.Printf("Error getting the user: %s", err.Error())
		} else {
			ns = getUserNamespace(*user)
		}

		co.Contexts = map[string]*api.Context{
			viper.GetString("cluster_name"): {
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
