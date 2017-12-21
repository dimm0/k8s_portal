package main

import (
	"fmt"
	"html/template"
	"log"
	"net/http"
	"strings"
	"time"

	client "github.com/dimm0/k8s_portal/pkg/apis/optiputer.net/v1alpha1"
	"k8s.io/api/core/v1"

	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
)

type NamespacesTemplateVars struct {
	IndexTemplateVars
	NamespaceBindings []NamespaceUserBinding
	PRPUsers          []client.PRPUser
}

type NamespaceUserBinding struct {
	Namespace           v1.Namespace
	RoleBindings        []rbacv1.RoleBinding
	ClusterRoleBindings []rbacv1.ClusterRoleBinding
}

func GetCrd() {

	if err := client.CreateCRD(crdclientset); err != nil {
		log.Printf("Error creating CRD: %s", err.Error())
	}

	// Wait for the CRD to be created before we use it (only needed if its a new one)
	time.Sleep(3 * time.Second)

	_, controller := cache.NewInformer(
		crdclient.NewListWatch(),
		&client.PRPUser{},
		time.Minute*10,
		cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				fmt.Printf("add: %s \n", obj)
			},
			DeleteFunc: func(obj interface{}) {
				fmt.Printf("delete: %s \n", obj)
			},
			UpdateFunc: func(oldObj, newObj interface{}) {
				fmt.Printf("Update old: %s \n      New: %s\n", oldObj, newObj)
			},
		},
	)

	stop := make(chan struct{})
	go controller.Run(stop)

	// Wait forever
	select {}
}

// Process the /namespaces path
func NamespacesHandler(w http.ResponseWriter, r *http.Request) {

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

	user, err := GetUser(session.Values["userid"].(string))
	if err != nil {
		session.AddFlash(fmt.Sprintf("Unexpected error: %s", err.Error()))
		session.Save(r, w)
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}

	userclientset, err := user.GetUserClientset()
	if err != nil {
		session.AddFlash(fmt.Sprintf("Unexpected error: %s", err.Error()))
		session.Save(r, w)
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}

	// User requested to create a new namespace
	var createNsName = r.URL.Query().Get("mkns")
	if createNsName != "" {
		if ns, err := clientset.Core().Namespaces().List(metav1.ListOptions{
			FieldSelector: fields.OneTermEqualSelector("metadata.name", createNsName).String()}); len(ns.Items) == 0 && err == nil {
			if _, err := clientset.Core().Namespaces().Create(&v1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: createNsName}}); err != nil {
				session.AddFlash(fmt.Sprintf("Error creating the namespace: %s", err.Error()))
				session.Save(r, w)
			} else {
				if _, err := createNsLimits(createNsName); err != nil {
					log.Printf("Error creating limits: %s", err.Error())
				}

				_, err := createNsRoleBinding(createNsName, "admin", user)
				if err != nil {
					log.Printf("Error creating userbinding %s", err.Error())
				}
			}
		} else {
			session.AddFlash(fmt.Sprintf("The namespace %s already exists or error %v", createNsName, err))
			session.Save(r, w)
		}
	}

	// User requested to delete a new namespace
	var delNsName = r.URL.Query().Get("delns")
	if delNsName != "" {
		if strings.HasPrefix(delNsName, "kube-") || delNsName == "default" {
			session.AddFlash(fmt.Sprintf("Can't delete standard namespace %s", delNsName))
			session.Save(r, w)
		} else {
			if err := userclientset.Core().Namespaces().Delete(delNsName, &metav1.DeleteOptions{}); err != nil {
				session.AddFlash(fmt.Sprintf("Error deleting the namespace: %s", err.Error()))
				session.Save(r, w)
			} else {
				session.AddFlash(fmt.Sprintf("The namespace %s is being deleted. Please update the page or use kubectl to see the result.", delNsName))
				session.Save(r, w)
			}
		}
	}

	if delNsName != "" || createNsName != "" {
		http.Redirect(w, r, "/namespaces", 303)
		return
	}

	// User requested to add another user to namespace
	// var addUserName = r.URL.Query().Get("addusername"),
	//   addUserNs = r.URL.Query().Get("adduserns")
	// if addUserName != "" && addUserNs != "" {
	//
	// } else {
	//   session.AddFlash(fmt.Sprintf("Not enough arguments"))
	//   session.Save(r, w)
	// }

	namespacesList, _ := clientset.Core().Namespaces().List(metav1.ListOptions{})

	nsList := []NamespaceUserBinding{}

	for _, ns := range namespacesList.Items {
		nsBind := NamespaceUserBinding{Namespace: ns, RoleBindings: []rbacv1.RoleBinding{}}
		if nsBind.RoleBindings, err = getUserNamespaceBindings(user.Spec.UserID, ns, userclientset); err == nil {
			nsList = append(nsList, nsBind)
		}
	}

	//Cluster ones
	nsBind := NamespaceUserBinding{ClusterRoleBindings: []rbacv1.ClusterRoleBinding{}}

	if nsBind.ClusterRoleBindings = getUserClusterBindings(user.Spec.UserID); len(nsBind.ClusterRoleBindings) > 0 {
		nsList = append(nsList, nsBind)
	}

	usersList, _ := crdclient.List(metav1.ListOptions{})

	nsVars := NamespacesTemplateVars{NamespaceBindings: nsList, PRPUsers: usersList.Items, IndexTemplateVars: buildIndexTemplateVars(session, w, r)}

	t, err := template.New("layout.tmpl").ParseFiles("templates/layout.tmpl", "templates/namespaces.tmpl")
	if err != nil {
		w.Write([]byte(err.Error()))
	} else {
		err = t.ExecuteTemplate(w, "layout.tmpl", nsVars)
		if err != nil {
			w.Write([]byte(err.Error()))
		}
	}
}

// Returns the rolebindings for a user and a namespace
func getUserNamespaceBindings(userId string, ns v1.Namespace, userclientset *kubernetes.Clientset) ([]rbacv1.RoleBinding, error) {
	ret := []rbacv1.RoleBinding{}
	rbList, err := userclientset.Rbac().RoleBindings(ns.GetName()).List(metav1.ListOptions{})
	if err != nil {
		return ret, err
	}

	for _, rb := range rbList.Items {
		for _, subj := range rb.Subjects {
			var subjStr = subj.Name
			if strings.Contains(subjStr, "#") {
				subjStr = strings.Split(subjStr, "#")[1]
			}
			if subjStr == userId {
				ret = append(ret, rb)
			}
		}
	}
	return ret, nil
}

// Returns clusterrolebindings for a user
func getUserClusterBindings(userId string) []rbacv1.ClusterRoleBinding {
	ret := []rbacv1.ClusterRoleBinding{}
	rbList, _ := clientset.Rbac().ClusterRoleBindings().List(metav1.ListOptions{})
	for _, rb := range rbList.Items {
		for _, subj := range rb.Subjects {
			var subjStr = subj.Name
			if strings.Contains(subjStr, "#") {
				subjStr = strings.Split(subjStr, "#")[1]
			}
			if subjStr == userId {
				ret = append(ret, rb)
			}
		}
	}
	return ret
}

// Creates a new rolebinding
func createNsRoleBinding(nsName string, roleName string, user *client.PRPUser) (*rbacv1.RoleBinding, error) {
	if rb, err := clientset.Rbac().RoleBindings(nsName).Create(&rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cilogon-psp",
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     "prpuserpsp",
		},
		Subjects: []rbacv1.Subject{rbacv1.Subject{
			Kind:     "User",
			APIGroup: "rbac.authorization.k8s.io",
			Name:     user.Spec.ISS + "#" + user.Spec.UserID}},
	}); err != nil {
		return rb, err
	}

	return clientset.Rbac().RoleBindings(nsName).Create(&rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cilogon2",
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     roleName,
		},
		Subjects: []rbacv1.Subject{rbacv1.Subject{
			Kind:     "User",
			APIGroup: "rbac.authorization.k8s.io",
			Name:     user.Spec.ISS + "#" + user.Spec.UserID}},
	})
}

// Creates a namespace default limits
func createNsLimits(ns string) (*v1.LimitRange, error) {
	return clientset.Core().LimitRanges(ns).Create(&v1.LimitRange{
		ObjectMeta: metav1.ObjectMeta{Name: ns + "-mem"},
		Spec: v1.LimitRangeSpec{
			Limits: []v1.LimitRangeItem{
				v1.LimitRangeItem{
					Type: v1.LimitTypeContainer,
					Default: map[v1.ResourceName]resource.Quantity{
						v1.ResourceMemory: resource.MustParse("4Gi"),
					},
					DefaultRequest: map[v1.ResourceName]resource.Quantity{
						v1.ResourceMemory: resource.MustParse("256Mi"),
					},
				},
			},
		},
	})
}
