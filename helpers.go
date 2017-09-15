package main

import (
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strings"
	"sync"
	"time"

	"k8s.io/api/core/v1"

	oidc "github.com/coreos/go-oidc"
	"github.com/gorilla/sessions"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

//keep config file to be requested later by JS
var keys = map[string][]byte{}

//service mappings
var serviceMappings map[string]int

var markMutex = &sync.Mutex{}
var scaleMutex = &sync.Mutex{}

//OIDC states
var states = map[string]string{}

var keysLock = sync.RWMutex{}
var statesLock = sync.RWMutex{}

type PrpUser struct {
	UserID string
}

type IndexTemplateVars struct {
	User PrpUser
}

type ConfigTemplateVars struct {
	IndexTemplateVars
	ConfigId string
}

type ServicesTemplateVars struct {
	IndexTemplateVars
	JupyterUrl string
	PodStatus  bool
}

func buildIndexTemplateVars(session *sessions.Session) IndexTemplateVars {
	var userId string
	if session.Values["userid"] != nil {
		userId = session.Values["userid"].(string)
	}
	returnVars := IndexTemplateVars{User: PrpUser{UserID: userId}}
	return returnVars
}

func getUserNamespace(userInfo *oidc.UserInfo) string {

	userDomain := strings.Split(userInfo.Email, "@")[1]

	reg, _ := regexp.Compile("[^a-zA-Z0-9-]+")
	userNamespace := reg.ReplaceAllString(userDomain, "-")
	return userNamespace
}

func FindUsersPod(userID string) (*v1.Pod, error) {
	pods, err := clientset.Pods("default").List(metav1.ListOptions{LabelSelector: "k8s-app=bigdipa, bigdipa_user=" + userID})
	if err != nil {
		return nil, err
	}
	for _, pod := range pods.Items {
		return &pod, nil
	}
	return nil, nil
}

func MarkFreePod(userStringID string) (*v1.Pod, error) {
	markMutex.Lock()
	defer markMutex.Unlock()

	pods, err := clientset.Pods("default").List(metav1.ListOptions{LabelSelector: "k8s-app=bigdipa, !bigdipa_user"})
	if err != nil {
		return nil, err
	}
	for _, pod := range pods.Items {
		log.Printf("Assigning label to a pod %s", pod.GetName())
		labelStr, _ := json.Marshal([]map[string]string{map[string]string{"op": "add", "path": "/metadata/labels/bigdipa_user", "value": userStringID}})
		clientset.Pods("default").Patch(pod.GetName(), types.JSONPatchType, labelStr, "")
		return &pod, nil
	}
	return nil, nil
}

func ScaleSet() error {
	scaleMutex.Lock()
	defer scaleMutex.Unlock()
	depl, err := clientset.AppsV1beta1().Deployments("default").Get("bigdipa", metav1.GetOptions{})
	if err != nil {
		return err
	}

	wantReplicas := *depl.Spec.Replicas + 1

	scaleStr, _ := json.Marshal([]map[string]string{map[string]string{"op": "replace", "path": "/spec/replicas", "value": fmt.Sprintf("%d", wantReplicas)}})

	clientset.AppsV1beta1().Deployments("default").Patch("bigdipa", types.JSONPatchType, scaleStr, "")

	for depl.Status.ReadyReplicas < wantReplicas {
		log.Printf("Waiting for replicas to increase %d\n", depl.Status.ReadyReplicas)
		time.Sleep(time.Second * 5)
		depl, err = clientset.AppsV1beta1().Deployments("default").Get("bigdipa", metav1.GetOptions{})
		if err != nil {
			return err
		}
	}
	return nil
}
