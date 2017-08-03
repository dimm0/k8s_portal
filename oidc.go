package main

import (
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/clientcmd/api"
	clientcmdlatest "k8s.io/client-go/tools/clientcmd/api/latest"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	oidc "github.com/coreos/go-oidc"

	"github.com/spf13/viper"
	"golang.org/x/net/context"
	"golang.org/x/oauth2"
)

const letterBytes = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"

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

	provider, err := oidc.NewProvider(ctx, "https://cilogon.org")
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
		// panic(err.Error())
	}

	clientset, err := kubernetes.NewForConfig(k8sconfig)
	if err != nil {
		// panic(err.Error())
	}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, config.AuthCodeURL(state), http.StatusFound)
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

		co := api.Config{
			APIVersion: "v1",
			Clusters: map[string]*api.Cluster{
				"calit2": &api.Cluster{
					CertificateAuthorityData: []byte(viper.GetString("certificate_authority_data")),
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
						"idp-issuer-url": "https://cilogon.org",
					},
				},
			}},
			CurrentContext: "calit2",
		}

		data, err := runtime.Encode(clientcmdlatest.Codec, &co)
		if err == nil {
			w.Write(data)
		} else {
			w.Write([]byte(err.Error()))
		}

		binding, err := clientset.Rbac().ClusterRoleBindings().Get("admin", nil)

		return

	})

	log.Printf("listening on http://%s/", "127.0.0.1")
	log.Fatal(http.ListenAndServe(":80", nil))
}
