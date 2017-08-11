package main

import (
	"encoding/json"
	"fmt"
	"html/template"
	"io/ioutil"
	"log"
	"math/rand"
	"net/http"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/clientcmd/api"
	clientcmdlatest "k8s.io/client-go/tools/clientcmd/api/latest"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	oidc "github.com/coreos/go-oidc"

	"github.com/spf13/viper"
	"golang.org/x/net/context"
	"golang.org/x/oauth2"
	"k8s.io/apimachinery/pkg/types"
)

const letterBytes = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"

var keys = map[string][]byte{}
var lock = sync.RWMutex{}

type UserPatchJson struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value struct {
		Kind     string `json:"kind"`
		Name     string `json:"name"`
		ApiGroup string `json:"apiGroup"`
	} `json:"value"`
}

func randStringBytes(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = letterBytes[rand.Intn(len(letterBytes))]
	}
	return string(b)
}

func main() {
	rand.Seed(time.Now().UnixNano())
	ctx := context.Background()
	viper.SetConfigName("config")
	viper.AddConfigPath(".")

	err := viper.ReadInConfig()
	if err != nil {
		panic(fmt.Errorf("fatal error config file: %s", err))
	}

	provider, err := oidc.NewProvider(ctx, "https://test.cilogon.org")
	if err != nil {
		log.Fatal(err)
	}
	config := oauth2.Config{
		ClientID:     viper.GetString("client_id"),
		ClientSecret: viper.GetString("client_secret"),
		Endpoint:     provider.Endpoint(),
		RedirectURL:  viper.GetString("redirect_url"),
		Scopes:       []string{oidc.ScopeOpenID, "profile", "email", "org.cilogon.userinfo", "edu.uiuc.ncsa.myproxy.getcert"},
	}

	// oidcConfig := &oidc.Config{
	// 	ClientID: viper.GetString("client_id"),
	// }
	// verifier := provider.Verifier(oidcConfig)

	state := randStringBytes(36)

	k8sconfig, err := rest.InClusterConfig()
	if err != nil {
		log.Fatal("Failed to do inclusterconfig: " + err.Error())
		return
	}

	clientset, err := kubernetes.NewForConfig(k8sconfig)
	if err != nil {
		log.Fatal("Failed to do inclusterconfig new client: " + err.Error())
	}

	// clusterinfo, err := clientset.CoreV1().ConfigMaps(metav1.NamespacePublic).Get("cluster-info", metav1.GetOptions{})
	// if err != nil {
	// 	log.Fatal("Failed to get clusterinfo: " + err.Error())
	// }

	fmt.Printf("Clusterinfo: %v", clientset)

	http.HandleFunc("/oidc-auth", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, config.AuthCodeURL(state), http.StatusFound)
	})

	http.HandleFunc("/oidc-auth/getConfig", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
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
	})

	http.HandleFunc("/oidc-auth/callback", func(w http.ResponseWriter, r *http.Request) {

		if r.Method != "GET" {
			return
		}

		if r.URL.Query().Get("state") != state {
			http.Error(w, "state did not match", http.StatusBadRequest)
			return
		}

		oauth2Token, err := config.Exchange(ctx, r.URL.Query().Get("code"))
		if err != nil {
			http.Error(w, "Failed to exchange token: "+err.Error(), http.StatusInternalServerError)
			return
		}

		userInfo, err := provider.UserInfo(ctx, oauth2.StaticTokenSource(oauth2Token))
		if err != nil {
			http.Error(w, "Failed to get userinfo: "+err.Error(), http.StatusInternalServerError)
			return
		}

		dat, err := ioutil.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/ca.crt")
		if err != nil {
			http.Error(w, "Failed to get ca cert: "+err.Error(), http.StatusInternalServerError)
			return
		}

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
					Cluster:  "calit2",
					AuthInfo: userInfo.Subject,
				},
			},
			AuthInfos: map[string]*api.AuthInfo{userInfo.Subject: {
				AuthProvider: &api.AuthProviderConfig{
					Name: "oidc",
					Config: map[string]string{
						"id-token":       oauth2Token.Extra("id_token").(string),
						"client-id":      viper.GetString("client_id"),
						"idp-issuer-url": viper.GetString("issuer"),
					},
				},
			}},
			CurrentContext: "calit2",
		}

		userID := viper.GetString("issuer") + "#" + userInfo.Subject

		found := false
		binding, err := clientset.Rbac().ClusterRoleBindings().Get("cilogon-admin", v1.GetOptions{})
		if err == nil {
			for _, subj := range binding.Subjects {
				if subj.Name == userID {
					found = true
				}
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

			patchres, err := clientset.Rbac().ClusterRoleBindings().Patch("cilogon-admin", types.JSONPatchType, userStr, "")
			if err != nil {
				log.Printf("Error doing patch %s", err.Error())
			} else {
				log.Printf("Success doing patch %v", patchres)
			}
		}

		data, err := runtime.Encode(clientcmdlatest.Codec, &co)
		if err == nil {
			newId := randStringBytes(16)
			lock.Lock()
			defer lock.Unlock()

			keys[newId] = data
			cleanKeyTimer := time.NewTimer(time.Second * 5)
			go func() {
				<-cleanKeyTimer.C
				delete(keys, newId)
			}()

			t, err := template.ParseFiles("authenticated.tmpl")
			if err != nil {
				w.Write([]byte(err.Error()))
			} else {
				err = t.Execute(w, struct{ ID string }{ID: newId})
				if err != nil {
					w.Write([]byte(err.Error()))
				}
			}

		} else {
			w.Write([]byte(err.Error()))
		}

		return

	})

	log.Printf("listening on http://%s/", "127.0.0.1")
	log.Fatal(http.ListenAndServe(":80", nil))
}
