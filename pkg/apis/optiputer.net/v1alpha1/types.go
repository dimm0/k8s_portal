package v1alpha1

import (
	meta_v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	Foo string `json:"foo"`
	Bar bool   `json:"bar"`
	Baz int    `json:"baz,omitempty"`
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
