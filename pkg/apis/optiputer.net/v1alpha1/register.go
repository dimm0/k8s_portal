package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	SchemeBuilder      = runtime.NewSchemeBuilder(addKnownTypes)
	localSchemeBuilder = &SchemeBuilder
	AddToScheme        = SchemeBuilder.AddToScheme
)

// schemeGroupVersion is group version used to register these objects
var SchemeGroupVersion = schema.GroupVersion{Group: "optiputer.net", Version: "v1alpha1"}

// var NautilusResource = CustomResource{
// 	Name:    "prpuser",
// 	Plural:  "prpusers",
// 	Group:   "nautilus",
// 	Version: "v1alpha1",
// 	Scope:   apiextensionsv1beta1.NamespaceScoped,
// 	Kind:    reflect.TypeOf(PRPUser{}).Name(),
// }

func init() {
	// We only register manually written functions here. The registration of the
	// generated functions takes place in the generated files. The separation
	// makes the code compile even when the generated files are missing.
	localSchemeBuilder.Register(addKnownTypes)
}

// Resource takes an unqualified resource and returns back a Group qualified GroupResource
func Resource(resource string) schema.GroupResource {
	return SchemeGroupVersion.WithResource(resource).GroupResource()
}

// Adds the list of known types to api.Scheme.
func addKnownTypes(scheme *runtime.Scheme) error {
	scheme.AddKnownTypes(SchemeGroupVersion,
		&PRPUser{},
		&PRPUserList{},
	)
	metav1.AddToGroupVersion(scheme, SchemeGroupVersion)
	return nil
}
