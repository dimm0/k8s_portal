package main

import (
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"path"
	"time"

	nautilusapi "github.com/dimm0/k8s_portal/pkg/apis/optiputer.net/v1alpha1"

	"k8s.io/api/extensions/v1beta1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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

var crdclient *nautilusapi.CrdClient

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

	filestore.Options.Domain = viper.GetString("cluster_url")
	filestore.Options.Secure = true
	filestore.Options.Path = "/"
	filestore.Options.MaxAge = 86400 * 7
	filestore.Options.HttpOnly = true

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
		log.Printf("Error creating client: %s", err.Error())
	}

	// Create a new clientset which include our CRD schema
	crdcs, scheme, err := nautilusapi.NewClient(k8sconfig)
	if err != nil {
		log.Printf("Error creating CRD client: %s", err.Error())
	}

	// Create a CRD client interface
	crdclient = nautilusapi.MakeCrdClient(crdcs, scheme, "default")

	SetupSecurity()

	http.Handle("/media/", http.StripPrefix("/media/", http.FileServer(http.Dir("/media"))))

	http.HandleFunc("/favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "/media/favicon.ico")
	})

	http.HandleFunc("/", RootHandler)
	http.HandleFunc("/namespaces", NamespacesHandler)
	http.HandleFunc("/nodes", NodesHandler)
	http.HandleFunc("/profile", ProfileHandler)
	http.HandleFunc("/nsMeta", NsMetaHandler)
	http.HandleFunc("/tests", TestsHandler)

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
	http.HandleFunc("/users", UsersHandler)
	http.HandleFunc("/logout", LogoutHandler)

	log.Printf("listening on http://%s/", "127.0.0.1")

	go func() {
		GetCrd()
	}()

	go func() {
		WatchGpuPods()
	}()

	log.Fatal(http.ListenAndServe(":80", nil))
}

func SetupSecurity() error {
	if _, err := clientset.Extensions().PodSecurityPolicies().Get("nautilususerpolicy", metav1.GetOptions{}); err != nil {
		f := false
		if _, err := clientset.Extensions().PodSecurityPolicies().Create(&v1beta1.PodSecurityPolicy{
			ObjectMeta: metav1.ObjectMeta{
				Name: "nautilususerpolicy",
			},
			//https://kubernetes.io/docs/concepts/policy/pod-security-policy
			Spec: v1beta1.PodSecurityPolicySpec{
				Privileged:               false,
				AllowPrivilegeEscalation: &f,
				Volumes: []v1beta1.FSType{
					v1beta1.ConfigMap,
					v1beta1.EmptyDir,
					// v1beta1.Projected,
					v1beta1.Secret,
					v1beta1.NFS,
					v1beta1.DownwardAPI,
					v1beta1.PersistentVolumeClaim,
					v1beta1.GitRepo,
				},
				HostNetwork: false,
				HostIPC:     false,
				HostPID:     false,
				HostPorts: []v1beta1.HostPortRange{
					{
						Min: 1024,
						Max: 65536,
					},
				},
				RunAsUser: v1beta1.RunAsUserStrategyOptions{
					Rule: v1beta1.RunAsUserStrategyRunAsAny,
				},
				SELinux: v1beta1.SELinuxStrategyOptions{
					Rule: v1beta1.SELinuxStrategyRunAsAny,
				},
				SupplementalGroups: v1beta1.SupplementalGroupsStrategyOptions{
					Rule: v1beta1.SupplementalGroupsStrategyRunAsAny,
				},
				FSGroup: v1beta1.FSGroupStrategyOptions{
					Rule: v1beta1.FSGroupStrategyRunAsAny,
				},
				ReadOnlyRootFilesystem: false,
			},
		}); err != nil {
			log.Printf("Error creating PSP %s", err.Error())
			return err
		}
	}

	if _, err := clientset.Rbac().ClusterRoles().Get("psp:nautilus-user", metav1.GetOptions{}); err != nil {
		if _, err := clientset.Rbac().ClusterRoles().Create(&rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{
				Name: "psp:nautilus-user",
			},
			Rules: []rbacv1.PolicyRule{
				{
					APIGroups:     []string{"extensions"},
					Verbs:         []string{"use"},
					Resources:     []string{"podsecuritypolicies"},
					ResourceNames: []string{"nautilususerpolicy"},
				},
			},
		}); err != nil {
			log.Printf("Error creating PSP role %s", err.Error())
			return err
		}
	}

	if _, err := clientset.Rbac().ClusterRoles().Get("nautilus-cluster-user", metav1.GetOptions{}); err != nil {
		if _, err := clientset.Rbac().ClusterRoles().Create(&rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{
				Name: "nautilus-cluster-user",
			},
			Rules: []rbacv1.PolicyRule{
				{
					APIGroups: []string{""},
					Verbs:     []string{"get", "list"},
					Resources: []string{"nodes"},
				},
			},
		}); err != nil {
			log.Printf("Error creating cluster Nautilus admin role %s", err.Error())
			return err
		}
	}

	if _, err := clientset.Rbac().ClusterRoles().Get("nautilus-admin", metav1.GetOptions{}); err != nil {
		if _, err := clientset.Rbac().ClusterRoles().Create(&rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{
				Name: "nautilus-admin",
			},
			Rules: []rbacv1.PolicyRule{
				{
					APIGroups: []string{""},
					Verbs:     []string{"delete"},
					Resources: []string{"namespaces"},
				},
			},
		}); err != nil {
			log.Printf("Error creating Nautilus admin role %s", err.Error())
			return err
		}
	}

	return nil
}
