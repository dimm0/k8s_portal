package main

import (
	"fmt"
	"html/template"
	"log"
	"net/http"
	"strings"
	"time"

	nautilusapi "github.com/dimm0/k8s_portal/pkg/apis/optiputer.net/v1alpha1"
	v1 "k8s.io/api/core/v1"

	authv1 "k8s.io/api/authorization/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextcs "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
)

type ProfileTemplateVars struct {
	IndexTemplateVars
	NamespaceBindings []NamespaceUserBinding
	PRPUsers          []nautilusapi.PRPUser
}

type NamespaceUserBinding struct {
	Namespace v1.Namespace
	ConfigMap v1.ConfigMap
}

func GetCrd() {
	k8sconfig, err := rest.InClusterConfig()
	if err != nil {
		log.Fatal("Failed to do inclusterconfig: " + err.Error())
		return
	}

	crdclientset, err := apiextcs.NewForConfig(k8sconfig)
	if err != nil {
		panic(err.Error())
	}

	if err := nautilusapi.CreateCRD(crdclientset); err != nil {
		log.Printf("Error creating CRD: %s", err.Error())
	}

	// Wait for the CRD to be created before we use it (only needed if its a new one)
	time.Sleep(3 * time.Second)

	_, controller := cache.NewInformer(
		crdclient.NewListWatch(),
		&nautilusapi.PRPUser{},
		time.Minute*5,
		cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				user, ok := obj.(*nautilusapi.PRPUser)
				if !ok {
					log.Printf("Expected PRPUser but other received %#v", obj)
					return
				}

				updateClusterUserPrivileges(user)
			},
			DeleteFunc: func(obj interface{}) {
				user, ok := obj.(*nautilusapi.PRPUser)
				if !ok {
					log.Printf("Expected PRPUser but other received %#v", obj)
					return
				}

				if rb, err := clientset.Rbac().ClusterRoleBindings().Get("nautilus-cluster-user", metav1.GetOptions{}); err == nil {
					allSubjects := []rbacv1.Subject{} // to filter the user, in case we need to delete one

					found := false
					for _, subj := range rb.Subjects {
						if subj.Name == user.Spec.UserID {
							found = true
						} else {
							allSubjects = append(allSubjects, subj)
						}
					}
					if found {
						rb.Subjects = allSubjects
						if _, err := clientset.Rbac().ClusterRoleBindings().Update(rb); err != nil {
							log.Printf("Error updating user %s: %s", user.Name, err.Error())
						}
					}
				}
			},
			UpdateFunc: func(oldObj, newObj interface{}) {
				oldUser, ok := oldObj.(*nautilusapi.PRPUser)
				if !ok {
					log.Printf("Expected PRPUser but other received %#v", oldObj)
					return
				}
				newUser, ok := newObj.(*nautilusapi.PRPUser)
				if !ok {
					log.Printf("Expected PRPUser but other received %#v", newObj)
					return
				}
				if oldUser.Spec.Role != newUser.Spec.Role {
					updateClusterUserPrivileges(newUser)
				}
			},
		},
	)

	stop := make(chan struct{})
	go controller.Run(stop)

	// Wait forever
	select {}
}

func updateClusterUserPrivileges(user *nautilusapi.PRPUser) error {
	allSubjects := []rbacv1.Subject{} // to filter the user, in case we need to delete one

	if rb, err := clientset.Rbac().ClusterRoleBindings().Get("nautilus-cluster-user", metav1.GetOptions{}); err == nil {
		found := false
		for _, subj := range rb.Subjects {
			if subj.Name == user.Spec.UserID {
				found = true
			} else {
				allSubjects = append(allSubjects, subj)
			}
		}
		if !user.IsGuest() && !found {
			rb.Subjects = append(rb.Subjects, rbacv1.Subject{
				Kind:     "User",
				APIGroup: "rbac.authorization.k8s.io",
				Name:     user.Spec.UserID})
			if _, err := clientset.Rbac().ClusterRoleBindings().Update(rb); err != nil {
				return err
			}
		} else if user.IsGuest() && found {
			rb.Subjects = allSubjects
			if _, err := clientset.Rbac().ClusterRoleBindings().Update(rb); err != nil {
				return err
			}
		}
	} else if !user.IsGuest() {
		if _, err := clientset.Rbac().ClusterRoleBindings().Create(&rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name: "nautilus-cluster-user",
			},
			RoleRef: rbacv1.RoleRef{
				APIGroup: "rbac.authorization.k8s.io",
				Kind:     "ClusterRole",
				Name:     "nautilus-cluster-user",
			},
			Subjects: []rbacv1.Subject{{
				Kind:     "User",
				APIGroup: "rbac.authorization.k8s.io",
				Name:     user.Spec.UserID}},
		}); err != nil {
			return err
		}
	}
	return nil
}

// Process the /profile path
func ProfileHandler(w http.ResponseWriter, r *http.Request) {

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

	if strings.ToLower(user.Spec.Role) != "admin" {
		session.AddFlash("Only admins can manage namespaces")
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

				if err := createNsRoleBinding(createNsName, user, clientset); err != nil {
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

	// User requested to add another user to namespace
	addUserName := r.URL.Query().Get("addusername")
	addUserNs := r.URL.Query().Get("adduserns")

	if addUserName != "" && addUserNs != "" {
		requser, err := GetUser(addUserName)
		if err != nil {
			session.AddFlash(fmt.Sprintf("Unexpected error: %s", err.Error()))
			session.Save(r, w)
			http.Redirect(w, r, "/", http.StatusFound)
			return
		}

		if err := createNsRoleBinding(addUserNs, requser, userclientset); err != nil {
			session.AddFlash(fmt.Sprintf("Error adding user to namespace: %s", err.Error()))
			session.Save(r, w)
		} else {
			session.AddFlash(fmt.Sprintf("Added user %s with role '%s' to namespace %s.", requser.Spec.Email, requser.Spec.Role, addUserNs))
			session.Save(r, w)
		}
	}

	// User requested to delete another user from namespace
	delUserName := r.URL.Query().Get("delusername")
	delUserNs := r.URL.Query().Get("deluserns")

	if delUserName != "" && delUserNs != "" {
		requser, err := GetUser(delUserName)
		if err != nil {
			session.AddFlash(fmt.Sprintf("Unexpected error: %s", err.Error()))
			session.Save(r, w)
			http.Redirect(w, r, "/", http.StatusFound)
			return
		}

		if err := delNsRoleBinding(delUserNs, requser, userclientset); err != nil {
			session.AddFlash(fmt.Sprintf("Error deleting user from namespace %s: %s", delUserNs, err.Error()))
			session.Save(r, w)
		} else {
			session.AddFlash(fmt.Sprintf("Deleted user %s from namespace %s.", requser.Spec.Email, delUserNs))
			session.Save(r, w)
		}
	}

	if delNsName != "" || createNsName != "" || addUserName != "" || delUserName != "" {
		http.Redirect(w, r, "/profile", 303)
		return
	}

	namespacesList, _ := clientset.Core().Namespaces().List(metav1.ListOptions{})

	nsList := []NamespaceUserBinding{}

	for _, ns := range namespacesList.Items {
		nsBind := NamespaceUserBinding{Namespace: ns}
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
				if metaConfig, err := clientset.CoreV1().ConfigMaps(ns.GetName()).Get("meta", metav1.GetOptions{}); err == nil {
					nsBind.ConfigMap = *metaConfig
				}
				nsList = append(nsList, nsBind)
			}
		}
	}

	usersList, _ := crdclient.List(metav1.ListOptions{})

	nsVars := ProfileTemplateVars{NamespaceBindings: nsList, PRPUsers: usersList.Items, IndexTemplateVars: buildIndexTemplateVars(session, w, r)}

	t, err := template.New("layout.tmpl").ParseFiles("templates/layout.tmpl", "templates/profile.tmpl")
	if err != nil {
		w.Write([]byte(err.Error()))
	} else {
		err = t.ExecuteTemplate(w, "layout.tmpl", nsVars)
		if err != nil {
			w.Write([]byte(err.Error()))
		}
	}
}

func NsMetaHandler(w http.ResponseWriter, r *http.Request) {
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

	if strings.ToLower(user.Spec.Role) != "admin" {
		session.AddFlash("Only admins can manage namespaces")
		session.Save(r, w)
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}

	if err := r.ParseForm(); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(err.Error()))
		return
	}

	updateNsName := r.PostFormValue("pk")
	if updateNsName != "" {
		if confMap, err := userclientset.Core().ConfigMaps(updateNsName).Get("meta", metav1.GetOptions{}); err == nil {
			switch r.PostFormValue("name") {
			case "PI":
				confMap.Data["PI"] = r.PostFormValue("value")
			case "Grant":
				confMap.Data["Grant"] = r.PostFormValue("value")
			}
			if _, err := userclientset.Core().ConfigMaps(updateNsName).Update(confMap); err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				w.Write([]byte(err.Error()))
				return
			}
		} else {
			dataMap := map[string]string{"PI": "", "Grant": ""}
			switch r.PostFormValue("name") {
			case "PI":
				dataMap["PI"] = r.PostFormValue("value")
			case "Grant":
				dataMap["Grant"] = r.PostFormValue("value")
			}
			if _, err := userclientset.Core().ConfigMaps(updateNsName).Create(&v1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name: "meta",
				},
				Data: dataMap,
			}); err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				w.Write([]byte(err.Error()))
				return
			}
		}
	}
}

// Creates a new rolebinding
func createNsRoleBinding(nsName string, user *nautilusapi.PRPUser, userclientset *kubernetes.Clientset) error {
	if rb, err := userclientset.Rbac().RoleBindings(nsName).Get("psp:nautilus-user", metav1.GetOptions{}); err == nil {
		found := false
		for _, subj := range rb.Subjects {
			if subj.Name == user.Spec.UserID {
				found = true
			}
		}
		if !found {
			rb.Subjects = append(rb.Subjects, rbacv1.Subject{
				Kind:     "User",
				APIGroup: "rbac.authorization.k8s.io",
				Name:     user.Spec.UserID})
			if _, err := userclientset.Rbac().RoleBindings(nsName).Update(rb); err != nil {
				return err
			}
		}
	} else {
		if _, err := userclientset.Rbac().RoleBindings(nsName).Create(&rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name: "psp:nautilus-user",
			},
			RoleRef: rbacv1.RoleRef{
				APIGroup: "rbac.authorization.k8s.io",
				Kind:     "ClusterRole",
				Name:     "psp:nautilus-user",
			},
			Subjects: []rbacv1.Subject{
				{
					Kind: "ServiceAccount",
					Name: "default"},
				{
					Kind:     "User",
					APIGroup: "rbac.authorization.k8s.io",
					Name:     user.Spec.UserID},
			},
		}); err != nil {
			return err
		}
	}

	if user.Spec.Role == "admin" {
		if rb, err := userclientset.Rbac().RoleBindings(nsName).Get("nautilus-admin-ext", metav1.GetOptions{}); err == nil {
			found := false
			for _, subj := range rb.Subjects {
				if subj.Name == user.Spec.UserID {
					found = true
				}
			}
			if !found {
				rb.Subjects = append(rb.Subjects, rbacv1.Subject{
					Kind:     "User",
					APIGroup: "rbac.authorization.k8s.io",
					Name:     user.Spec.UserID})
				if _, err := userclientset.Rbac().RoleBindings(nsName).Update(rb); err != nil {
					return err
				}
			}
		} else {
			if _, err := userclientset.Rbac().RoleBindings(nsName).Create(&rbacv1.RoleBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: "nautilus-admin-ext",
				},
				RoleRef: rbacv1.RoleRef{
					APIGroup: "rbac.authorization.k8s.io",
					Kind:     "ClusterRole",
					Name:     "nautilus-admin",
				},
				Subjects: []rbacv1.Subject{{
					Kind:     "User",
					APIGroup: "rbac.authorization.k8s.io",
					Name:     user.Spec.UserID}},
			}); err != nil {
				return err
			}
		}
	}

	if rb, err := userclientset.Rbac().RoleBindings(nsName).Get("nautilus-"+user.Spec.Role, metav1.GetOptions{}); err == nil {
		found := false
		for _, subj := range rb.Subjects {
			if subj.Name == user.Spec.UserID {
				found = true
			}
		}
		if !found {
			rb.Subjects = append(rb.Subjects, rbacv1.Subject{
				Kind:     "User",
				APIGroup: "rbac.authorization.k8s.io",
				Name:     user.Spec.UserID})
			_, err := userclientset.Rbac().RoleBindings(nsName).Update(rb)
			return err
		}
	} else {
		clusterRoleName := ""
		switch user.Spec.Role {
		case "user":
			clusterRoleName = "edit"
		case "admin":
			clusterRoleName = "admin"
		}
		_, err := userclientset.Rbac().RoleBindings(nsName).Create(&rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name: "nautilus-" + user.Spec.Role,
			},
			RoleRef: rbacv1.RoleRef{
				APIGroup: "rbac.authorization.k8s.io",
				Kind:     "ClusterRole",
				Name:     clusterRoleName,
			},
			Subjects: []rbacv1.Subject{{
				Kind:     "User",
				APIGroup: "rbac.authorization.k8s.io",
				Name:     user.Spec.UserID}},
		})
		return err
	}
	return nil

}

// Deletes a rolebinding
func delNsRoleBinding(nsName string, user *nautilusapi.PRPUser, userclientset *kubernetes.Clientset) error {
	if rb, err := userclientset.Rbac().RoleBindings(nsName).Get("psp:nautilus-user", metav1.GetOptions{}); err == nil {
		allSubjects := []rbacv1.Subject{} // to filter the user, in case we need to delete one
		found := false
		for _, subj := range rb.Subjects {
			if subj.Name == user.Spec.UserID {
				found = true
			} else {
				allSubjects = append(allSubjects, subj)
			}
		}
		if found {
			if len(allSubjects) == 0 {
				if err := userclientset.Rbac().RoleBindings(nsName).Delete(rb.GetName(), &metav1.DeleteOptions{}); err != nil {
					return err
				}
			} else {
				rb.Subjects = allSubjects
				if _, err := userclientset.Rbac().RoleBindings(nsName).Update(rb); err != nil {
					return err
				}
			}
		}
	}

	if user.Spec.Role == "admin" {
		if rb, err := userclientset.Rbac().RoleBindings(nsName).Get("nautilus-admin-ext", metav1.GetOptions{}); err == nil {
			allSubjects := []rbacv1.Subject{} // to filter the user, in case we need to delete one
			found := false
			for _, subj := range rb.Subjects {
				if subj.Name == user.Spec.UserID {
					found = true
				} else {
					allSubjects = append(allSubjects, subj)
				}
			}
			if found {
				if len(allSubjects) == 0 {
					if err := userclientset.Rbac().RoleBindings(nsName).Delete(rb.GetName(), &metav1.DeleteOptions{}); err != nil {
						return err
					}
				} else {
					rb.Subjects = allSubjects
					if _, err := userclientset.Rbac().RoleBindings(nsName).Update(rb); err != nil {
						return err
					}
				}
			}
		}
	}

	if rb, err := userclientset.Rbac().RoleBindings(nsName).Get("nautilus-"+user.Spec.Role, metav1.GetOptions{}); err == nil {
		allSubjects := []rbacv1.Subject{} // to filter the user, in case we need to delete one
		found := false
		for _, subj := range rb.Subjects {
			if subj.Name == user.Spec.UserID {
				found = true
			} else {
				allSubjects = append(allSubjects, subj)
			}
		}
		if found {
			if len(allSubjects) == 0 {
				if err := userclientset.Rbac().RoleBindings(nsName).Delete(rb.GetName(), &metav1.DeleteOptions{}); err != nil {
					return err
				}
			} else {
				rb.Subjects = allSubjects
				_, err := userclientset.Rbac().RoleBindings(nsName).Update(rb)
				return err
			}
		}
	}
	return nil
}

// Creates a namespace default limits
func createNsLimits(ns string) (*v1.LimitRange, error) {
	return clientset.Core().LimitRanges(ns).Create(&v1.LimitRange{
		ObjectMeta: metav1.ObjectMeta{Name: ns + "-mem"},
		Spec: v1.LimitRangeSpec{
			Limits: []v1.LimitRangeItem{
				{
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
