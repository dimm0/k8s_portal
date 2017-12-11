package v1alpha1

import (
	"reflect"

	apiextv1beta1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1beta1"
	apiextcs "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	meta_v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
)

const (
	CRDPlural   string = "prpusers"
	CRDGroup    string = "optiputer.net"
	CRDVersion  string = "v1alpha1"
	FullCRDName string = CRDPlural + "." + CRDGroup
)

// Create the CRD resource, ignore error if it already exists
func CreateCRD(clientset apiextcs.Interface) error {
	crd := &apiextv1beta1.CustomResourceDefinition{
		ObjectMeta: meta_v1.ObjectMeta{Name: FullCRDName},
		Spec: apiextv1beta1.CustomResourceDefinitionSpec{
			Group:   CRDGroup,
			Version: CRDVersion,
			Scope:   apiextv1beta1.ClusterScoped,
			Names: apiextv1beta1.CustomResourceDefinitionNames{
				Plural: CRDPlural,
				Kind:   reflect.TypeOf(PRPUser{}).Name(),
			},
		},
	}

	_, err := clientset.ApiextensionsV1beta1().CustomResourceDefinitions().Create(crd)
	if err != nil && apierrors.IsAlreadyExists(err) {
		return nil
	}
	return err

	// Note the original apiextensions example adds logic to wait for creation and exception handling
}

func NewClient(cfg *rest.Config) (*rest.RESTClient, *runtime.Scheme, error) {
	scheme := runtime.NewScheme()
	SchemeBuilder := runtime.NewSchemeBuilder(addKnownTypes)
	if err := SchemeBuilder.AddToScheme(scheme); err != nil {
		return nil, nil, err
	}
	config := *cfg
	config.GroupVersion = &SchemeGroupVersion
	config.APIPath = "/apis"
	config.ContentType = runtime.ContentTypeJSON
	config.NegotiatedSerializer = serializer.DirectCodecFactory{
		CodecFactory: serializer.NewCodecFactory(scheme)}

	client, err := rest.RESTClientFor(&config)
	if err != nil {
		return nil, nil, err
	}
	return client, scheme, nil
}

func CrdClient(cl *rest.RESTClient, scheme *runtime.Scheme, namespace string) *crdclient {
	return &crdclient{cl: cl, ns: namespace, plural: CRDPlural,
		codec: runtime.NewParameterCodec(scheme)}
}

type crdclient struct {
	cl     *rest.RESTClient
	ns     string
	plural string
	codec  runtime.ParameterCodec
}

func (f *crdclient) Create(obj *PRPUser) (*PRPUser, error) {
	var result PRPUser
	err := f.cl.Post().
		Namespace(f.ns).Resource(f.plural).
		Body(obj).Do().Into(&result)
	return &result, err
}

func (f *crdclient) Update(obj *PRPUser) (*PRPUser, error) {
	var result PRPUser
	err := f.cl.Put().
		Namespace(f.ns).Resource(f.plural).
		Body(obj).Do().Into(&result)
	return &result, err
}

func (f *crdclient) Delete(name string, options *meta_v1.DeleteOptions) error {
	return f.cl.Delete().
		Namespace(f.ns).Resource(f.plural).
		Name(name).Body(options).Do().
		Error()
}

func (f *crdclient) Get(name string) (*PRPUser, error) {
	var result PRPUser
	err := f.cl.Get().
		Namespace(f.ns).Resource(f.plural).
		Name(name).Do().Into(&result)
	return &result, err
}

func (f *crdclient) List(opts meta_v1.ListOptions) (*PRPUserList, error) {
	var result PRPUserList
	err := f.cl.Get().
		Namespace(f.ns).Resource(f.plural).
		VersionedParams(&opts, f.codec).
		Do().Into(&result)
	return &result, err
}

// Create a new List watch for our TPR
func (f *crdclient) NewListWatch() *cache.ListWatch {
	return cache.NewListWatchFromClient(f.cl, f.plural, f.ns, fields.Everything())
}

// func createContext() (*opkit.Context, nautilusclient.NautilusV1alpha1Interface, error) {
// 	config, err := rest.InClusterConfig()
// 	if err != nil {
// 		return nil, nil, fmt.Errorf("failed to get k8s config. %+v", err)
// 	}
//
// 	clientset, err := kubernetes.NewForConfig(config)
// 	if err != nil {
// 		return nil, nil, fmt.Errorf("failed to get k8s client. %+v", err)
// 	}
//
// 	apiExtClientset, err := apiextensionsclient.NewForConfig(config)
// 	if err != nil {
// 		return nil, nil, fmt.Errorf("failed to create k8s API extension clientset. %+v", err)
// 	}
//
// 	sampleClientset, err := sampleclient.NewForConfig(config)
// 	if err != nil {
// 		return nil, nil, fmt.Errorf("failed to create sample clientset. %+v", err)
// 	}
//
// 	context := &opkit.Context{
// 		Clientset:             clientset,
// 		APIExtensionClientset: apiExtClientset,
// 		Interval:              500 * time.Millisecond,
// 		Timeout:               60 * time.Second,
// 	}
// 	return context, sampleClientset, nil
//
// }
