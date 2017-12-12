package v1alpha1

import (
	"log"
	"strings"

	rbacv1 "k8s.io/api/rbac/v1"

	meta_v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// +genclient
// +genclient:noStatus
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

type PRPUser struct {
	meta_v1.TypeMeta   `json:",inline"`
	meta_v1.ObjectMeta `json:"metadata"`
	Spec               PRPUserSpec   `json:"spec"`
	Status             PRPUserStatus `json:"status,omitempty"`
}

type PRPUserSpec struct {
	UserID string `json:""`
	ISS    string `json:""`
	Email  string `json:""`
	Name   string `json:""`
	IDP    string `json:""`
}

type PRPUserStatus struct {
	State   string `json:"state,omitempty"`
	Message string `json:"message,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// PRPUserList is a list of PRP users
type PRPUserList struct {
	meta_v1.TypeMeta `json:",inline"`
	meta_v1.ListMeta `json:"metadata"`
	Items            []PRPUser `json:"items"`
}

func (user PRPUser) IsAdmin(namespace string) bool {
	if user.IsClusterAdmin() {
		return true
	}
	if namespace != "" {
		k8sconfig, err := rest.InClusterConfig()
		if err != nil {
			log.Fatal("Failed to do inclusterconfig: " + err.Error())
			return false
		}

		clientset, err := kubernetes.NewForConfig(k8sconfig)
		if err != nil {
			log.Printf("Error creating client: %s", err.Error())
			return false
		}

		if bindings, err := clientset.Rbac().RoleBindings(namespace).List(meta_v1.ListOptions{}); err != nil {
			return false
		} else {
			for _, bind := range bindings.Items {
				if bind.RoleRef.Name == "admin" || bind.RoleRef.Name == "cluster-admin" {
					for _, subj := range bind.Subjects {
						if subj.Kind == rbacv1.UserKind && user.Spec.ISS+"#"+user.Name == subj.Name {
							return true
						}
					}
				}
			}
		}
	}
	return false
}

func (user PRPUser) IsClusterAdmin() bool {
	k8sconfig, err := rest.InClusterConfig()
	if err != nil {
		log.Fatal("Failed to do inclusterconfig: " + err.Error())
		return false
	}

	clientset, err := kubernetes.NewForConfig(k8sconfig)
	if err != nil {
		log.Printf("Error creating client: %s", err.Error())
		return false
	}

	if bindings, err := clientset.Rbac().ClusterRoleBindings().List(meta_v1.ListOptions{}); err != nil {
		return false
	} else {
		for _, bind := range bindings.Items {
			if !strings.HasPrefix(bind.Name, "system") && (bind.RoleRef.Name == "admin" || bind.RoleRef.Name == "cluster-admin") {
				for _, subj := range bind.Subjects {
					if subj.Kind == rbacv1.UserKind && user.Spec.ISS+"#"+user.Name == subj.Name {
						return true
					}
				}
			}
		}
	}
	return false
}

func (user PRPUser) GetUserClientset() (*kubernetes.Clientset, error) {
	userk8sconfig, err := rest.InClusterConfig()
	if err != nil {
		return nil, err
	}

	userk8sconfig.Impersonate = rest.ImpersonationConfig{
		UserName: user.Spec.ISS + "#" + user.Spec.UserID,
	}

	return kubernetes.NewForConfig(userk8sconfig)

}
