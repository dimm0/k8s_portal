package main

import (
	"fmt"
	"html/template"
	"log"
	"net/http"
	"strings"
	"time"

	client "github.com/dimm0/k8s_portal/pkg/apis/nautilus/v1alpha1"
	"k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	rbacv1 "k8s.io/api/rbac/v1"
	apiextcs "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
)

func GetCrd() {
	k8sconfig, err := rest.InClusterConfig()
	if err != nil {
		log.Fatal("Failed to do inclusterconfig: " + err.Error())
		return
	}

	clientset, err = kubernetes.NewForConfig(k8sconfig)
	if err != nil {
		log.Fatal("Failed to do inclusterconfig new client: " + err.Error())
	}

	// create clientset and create our CRD, this only need to run once
	clientset, err := apiextcs.NewForConfig(k8sconfig)
	if err != nil {
		panic(err.Error())
	}
	// note: if the CRD exist our CreateCRD function is set to exit without an error
	err = client.CreateCRD(clientset)
	if err != nil {
		panic(err)
	}

	// Wait for the CRD to be created before we use it (only needed if its a new one)
	time.Sleep(3 * time.Second)

	// Create a new clientset which include our CRD schema
	crdcs, scheme, err := client.NewClient(k8sconfig)
	if err != nil {
		panic(err)
	}

	// Create a CRD client interface
	crdclient := client.CrdClient(crdcs, scheme, "default")

	// Create a new Example object and write to k8s
	user := &client.PRPUser{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "example123",
			Labels: map[string]string{"mylabel": "test"},
		},
		Spec: client.PRPUserSpec{
			Foo: "example-text",
			Bar: true,
		},
		Status: client.PRPUserStatus{
			State:   "created",
			Message: "Created, not processed yet",
		},
	}

	result, err := crdclient.Create(user)
	if err == nil {
		fmt.Printf("CREATED: %#v\n", result)
	} else if apierrors.IsAlreadyExists(err) {
		fmt.Printf("ALREADY EXISTS: %#v\n", result)
	} else {
		fmt.Printf("ERROR CREATING: %s\n", err.Error())
	}

	// List all Example objects
	items, err := crdclient.List(metav1.ListOptions{})
	if err != nil {
		panic(err)
	}
	fmt.Printf("List:\n%s\n", items)

	// Example Controller
	// Watch for changes in Example objects and fire Add, Delete, Update callbacks
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

	// User requested to create a new namespace
	var createNsName = r.URL.Query().Get("mkns")
	if createNsName != "" {
		if ns, err := clientset.Core().Namespaces().List(
			metav1.ListOptions{
				FieldSelector: fields.OneTermEqualSelector("metadata.name", createNsName).String()}); len(ns.Items) == 0 && err == nil {
			if user, err := getUser(session.Values["userid"].(string)); err == nil {
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
				log.Printf("Error getting the user: %s", err.Error())
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
			if _, err := clientset.Core().Namespaces().List(metav1.SingleObject(metav1.ObjectMeta{Name: delNsName})); err == nil {
				if user, err := getUser(session.Values["userid"].(string)); err == nil {
					if !user.IsAdmin(delNsName) {
						session.AddFlash(fmt.Sprintf("You don't have permissions to delete namespace %s", delNsName))
						session.Save(r, w)
					} else {
						if err := clientset.Core().Namespaces().Delete(delNsName, &metav1.DeleteOptions{}); err != nil {
							session.AddFlash(fmt.Sprintf("Error deleting the namespace: %s", err.Error()))
							session.Save(r, w)
						} else {
							session.AddFlash(fmt.Sprintf("The namespace %s is being deleted. Please update the page or use kubectl to see the result.", delNsName))
							session.Save(r, w)
						}
					}
				} else {
					log.Printf("Error getting the user: %s", err.Error())
				}
			} else {
				session.AddFlash(fmt.Sprintf("The namespace %s does not exist", delNsName))
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
		if nsBind.RoleBindings = getUserNamespaceBindings(session.Values["userid"].(string), ns); len(nsBind.RoleBindings) > 0 {
			nsList = append(nsList, nsBind)
		}
	}

	//Cluster ones
	nsBind := NamespaceUserBinding{ClusterRoleBindings: []rbacv1.ClusterRoleBinding{}}

	if nsBind.ClusterRoleBindings = getUserClusterBindings(session.Values["userid"].(string)); len(nsBind.ClusterRoleBindings) > 0 {
		nsList = append(nsList, nsBind)
	}

	nsVars := NamespacesTemplateVars{NamespaceBindings: nsList, IndexTemplateVars: buildIndexTemplateVars(session, w, r)}

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
func getUserNamespaceBindings(userId string, ns v1.Namespace) []rbacv1.RoleBinding {
	ret := []rbacv1.RoleBinding{}
	rbList, _ := clientset.Rbac().RoleBindings(ns.GetName()).List(metav1.ListOptions{})
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
func createNsRoleBinding(nsName string, roleName string, user PrpUser) (*rbacv1.RoleBinding, error) {
	return clientset.Rbac().RoleBindings(nsName).Create(&rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cilogon",
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     roleName,
		},
		Subjects: []rbacv1.Subject{rbacv1.Subject{
			Kind:     "User",
			APIGroup: "rbac.authorization.k8s.io",
			Name:     user.ISS + "#" + user.UserID}},
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
