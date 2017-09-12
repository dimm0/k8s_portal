package main

import (
	"encoding/json"
	"fmt"
	"html/template"
	"io/ioutil"
	"log"
	"net/http"
	"regexp"
	"time"

	clientcmdlatest "k8s.io/client-go/tools/clientcmd/api/latest"

	"github.com/spf13/viper"
	"golang.org/x/oauth2"
	"k8s.io/api/core/v1"
	rbacv1beta1 "k8s.io/api/rbac/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd/api"
)

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

//handles the http requests for get services
func ServicesHandler(w http.ResponseWriter, r *http.Request) {

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

	reg, _ := regexp.Compile("[^a-zA-Z0-9]+")
	userStringID := reg.ReplaceAllString(session.Values["userid"].(string), "-")

	pod, err := FindUsersPod(userStringID)
	if err != nil {
		log.Printf("Error getting the user's POD status: %s", err.Error())
	}

	jupyterURL := "Not found"
	jupyterService, err := clientset.Services("default").Get(userStringID+"-jupyter", metav1.GetOptions{})
	if err != nil {
		log.Printf("Error getting Jupyter service: %s", err.Error())
	} else {
		for _, port := range jupyterService.Spec.Ports {
			if port.NodePort > 30000 {
				jupyterURL = fmt.Sprintf("%s:%d", viper.GetString("cluster_url"), port.NodePort)
			}
		}
	}

	t, err := template.ParseFiles("templates/layout.tmpl", "templates/services.tmpl")
	if err != nil {
		w.Write([]byte(err.Error()))
	} else {
		err = t.Execute(w, ServicesTemplateVars{JupyterUrl: jupyterURL, PodStatus: pod != nil, IndexTemplateVars: buildIndexTemplateVars(session)})
		if err != nil {
			w.Write([]byte(err.Error()))
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

	oauth2Token, err := config.Exchange(r.Context(), r.URL.Query().Get("code"))
	if err != nil {
		http.Error(w, "Failed to exchange token: "+err.Error()+" for code "+r.URL.Query().Get("code"), http.StatusInternalServerError)
		return
	}

	userInfo, err := provider.UserInfo(r.Context(), oauth2.StaticTokenSource(oauth2Token))
	if err != nil {
		http.Error(w, "Failed to get userinfo: "+err.Error(), http.StatusInternalServerError)
		return
	}

	userNamespace := getUserNamespace(userInfo)

	if _, err := clientset.Core().Namespaces().Get(userNamespace, metav1.GetOptions{}); err != nil {
		clientset.Core().Namespaces().Create(&v1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: userNamespace}})
	}

	userID := viper.GetString("issuer") + "#" + userInfo.Subject

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

	go func() {

		reg, _ := regexp.Compile("[^a-zA-Z0-9]+")
		userStringID := reg.ReplaceAllString(userInfo.Email, "-")

		if _, err := clientset.Services("default").Get(userStringID+"-jupyter", metav1.GetOptions{}); err == nil { // service is the mutex for the user, to avoid creating pods multiple times
			return
		}

		_, err = clientset.Services("default").Create(&v1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:   userStringID + "-jupyter",
				Labels: map[string]string{"k8s-app": "bigdipa"},
			},
			Spec: v1.ServiceSpec{
				Type:  v1.ServiceTypeNodePort,
				Ports: []v1.ServicePort{v1.ServicePort{Port: 8888}},
				Selector: map[string]string{
					"bigdipa_user": userStringID,
				},
			},
		})

		if err != nil {
			log.Printf("Error creating the service: %s", err.Error())
		} else {
			log.Printf("Assigned service %s to user %s", userStringID+"-jupyter", userStringID)
		}

		pod, err := FindUsersPod(userStringID)
		if err != nil {
			log.Printf("Error finding user's pod %s", err.Error())
			return
		}

		if pod == nil { // not created
			pod, err := MarkFreePod(userStringID)
			if err != nil {
				log.Printf("Error finding free pod %s", err.Error())
				return
			}

			if pod == nil { // Scale it by 1
				err := ScaleSet()
				if err != nil {
					log.Printf("Error scaling the set %s", err.Error())
					return
				}

				for pod == nil {
					log.Printf("Waiting for pod for user %s", userStringID)
					pod, err = MarkFreePod(userStringID)
					time.Sleep(time.Second * 5)
					if err != nil {
						log.Printf("Error finding free pod for %s: %s", userStringID, err.Error())
						return
					}
				}
			}

		}
	}()

	switch stateVal {
	case "auth":
		session.Values["userid"] = userInfo.Email
		session.Values["namespace"] = userNamespace
		if e := session.Save(r, w); e != nil {
			http.Error(w, "Failed to save session: "+e.Error(), http.StatusInternalServerError)
			return
		}
		log.Printf("Saved session")
		http.Redirect(w, r, "/", http.StatusFound)
	case "config":
		dat, err := ioutil.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/ca.crt")
		if err != nil {
			http.Error(w, "Failed to get ca cert: "+err.Error(), http.StatusInternalServerError)
			return
		}

		log.Printf("Got token: %v", oauth2Token)
		co := api.Config{
			APIVersion: "v1",
			Clusters: map[string]*api.Cluster{
				"calit2": &api.Cluster{
					CertificateAuthorityData: dat,
					Server: viper.GetString("kubernetes_server"),
				},
			},
			Contexts: map[string]*api.Context{
				"calit2": &api.Context{
					Cluster:   "calit2",
					AuthInfo:  userInfo.Subject,
					Namespace: userNamespace,
				},
			},
			AuthInfos: map[string]*api.AuthInfo{userInfo.Subject: {
				AuthProvider: &api.AuthProviderConfig{
					Name: "oidc",
					Config: map[string]string{
						"id-token":       oauth2Token.Extra("id_token").(string),
						"refresh-token":  oauth2Token.RefreshToken,
						"client-id":      viper.GetString("client_id"),
						"client-secret":  viper.GetString("client_secret"),
						"idp-issuer-url": viper.GetString("issuer"),
					},
				},
			}},
			CurrentContext: "calit2",
		}

		data, err := runtime.Encode(clientcmdlatest.Codec, &co)
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
