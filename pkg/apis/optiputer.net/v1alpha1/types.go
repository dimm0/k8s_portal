package v1alpha1

import (
	"strings"

	authv1 "k8s.io/api/authorization/v1"

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
	Spec               PRPUserSpec `json:"spec"`
}

type PRPUserSpec struct {
	UserID string `json:""`
	ISS    string `json:""`
	Email  string `json:""`
	Name   string `json:""`
	IDP    string `json:""`
	Role   string `json:""` // guest, user, admin
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// PRPUserList is a list of PRP users
type PRPUserList struct {
	meta_v1.TypeMeta `json:",inline"`
	meta_v1.ListMeta `json:"metadata"`
	Items            []PRPUser `json:"items"`
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

func (user PRPUser) IsGuest() bool {
	return strings.ToLower(user.Spec.Role) == "guest"
}

// Check if user can create accounts in the NS - so is he an admin
func (user PRPUser) IsAdmin(ns string) bool {
	userclientset, err := user.GetUserClientset()
	if err != nil {
		return false
	}

	if rev, err := userclientset.AuthorizationV1().SelfSubjectAccessReviews().Create(&authv1.SelfSubjectAccessReview{
		Spec: authv1.SelfSubjectAccessReviewSpec{
			ResourceAttributes: &authv1.ResourceAttributes{
				Namespace: ns,
				Verb:      "create",
				Group:     "rbac.authorization.k8s.io",
				Resource:  "rolebindings",
			},
		},
	}); err == nil {
		return rev.Status.Allowed
	}

	return false
}
