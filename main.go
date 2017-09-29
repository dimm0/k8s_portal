package main

import (
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"path"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	oidc "github.com/coreos/go-oidc"
	"github.com/gorilla/sessions"

	"github.com/spf13/viper"
	"golang.org/x/net/context"
	"golang.org/x/oauth2"
)

const letterBytes = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"

var config oauth2.Config
var pubconfig oauth2.Config
var provider *oidc.Provider
var clientset *kubernetes.Clientset
var filestore *sessions.FilesystemStore

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
	viper.AddConfigPath("config")

	viper.SetDefault("cluster_name", "kubernetes")
	viper.SetDefault("storage_path", "/")

	err := viper.ReadInConfig()
	if err != nil {
		panic(fmt.Errorf("fatal error config file: %s", err))
	}

	os.Mkdir(path.Join(viper.GetString("storage_path"), "sessions"), 0777)
	filestore = sessions.NewFilesystemStore(path.Join(viper.GetString("storage_path"), "sessions"), []byte(viper.GetString("session_auth_key")), []byte(viper.GetString("session_enc_key")))

	provider, err = oidc.NewProvider(ctx, viper.GetString("oidc_provider"))
	if err != nil {
		log.Fatal(err)
	}
	config = oauth2.Config{
		ClientID:     viper.GetString("client_id"),
		ClientSecret: viper.GetString("client_secret"),
		Endpoint:     provider.Endpoint(),
		RedirectURL:  "https://" + viper.GetString("cluster_url") + "/callback",
		Scopes:       []string{oidc.ScopeOpenID, "profile", "email", "org.cilogon.userinfo"},
	}

	pubconfig = oauth2.Config{
		ClientID:     viper.GetString("pub_client_id"),
		ClientSecret: viper.GetString("pub_client_secret"),
		Endpoint:     provider.Endpoint(),
		RedirectURL:  "https://" + viper.GetString("cluster_url") + "/callback",
		Scopes:       []string{oidc.ScopeOpenID},
	}

	k8sconfig, err := rest.InClusterConfig()
	if err != nil {
		log.Fatal("Failed to do inclusterconfig: " + err.Error())
		return
	}

	clientset, err = kubernetes.NewForConfig(k8sconfig)
	if err != nil {
		log.Fatal("Failed to do inclusterconfig new client: " + err.Error())
	}

	// clusterinfo, err := clientset.CoreV1().ConfigMaps(metav1.NamespacePublic).Get("cluster-info", metav1.GetOptions{})
	// if err != nil {
	// 	log.Fatal("Failed to get clusterinfo: " + err.Error())
	// }
	// fmt.Printf("Clusterinfo: %v", clientset)

	http.HandleFunc("/", RootHandler)
	http.HandleFunc("/services", ServicesHandler)

	http.HandleFunc("/authConfig", func(w http.ResponseWriter, r *http.Request) {
		statesLock.Lock()
		defer statesLock.Unlock()

		newState := randStringBytes(36)
		states[newState] = "config"
		cleanStateTimer := time.NewTimer(time.Minute * 10)
		go func() {
			<-cleanStateTimer.C
			statesLock.Lock()
			defer statesLock.Unlock()
			delete(states, newState)
		}()
		http.Redirect(w, r, pubconfig.AuthCodeURL(newState), http.StatusFound)
	})

	http.HandleFunc("/auth", func(w http.ResponseWriter, r *http.Request) {
		statesLock.Lock()
		defer statesLock.Unlock()

		newState := randStringBytes(36)
		states[newState] = "auth"
		cleanStateTimer := time.NewTimer(time.Minute * 10)
		go func() {
			<-cleanStateTimer.C
			statesLock.Lock()
			defer statesLock.Unlock()
			delete(states, newState)
		}()
		http.Redirect(w, r, config.AuthCodeURL(newState), http.StatusFound)
	})

	http.HandleFunc("/getConfig", GetConfigHandler)
	http.HandleFunc("/callback", AuthenticateHandler)
	http.HandleFunc("/logout", LogoutHandler)

	log.Printf("listening on http://%s/", "127.0.0.1")

	log.Fatal(http.ListenAndServe(":80", nil))
}
