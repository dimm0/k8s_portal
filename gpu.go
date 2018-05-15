package main

import (
	"bytes"
	"fmt"
	"log"
	"strings"
	"time"

	"k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/remotecommand"
)

//https://github.com/zalando-incubator/postgres-operator/blob/master/pkg/cluster/exec.go

func WatchGpuPods() {
	podGpus := make(map[types.UID][]string)

	lw := cache.NewListWatchFromClient(
		clientset.Core().RESTClient(),
		"pods",
		v1.NamespaceAll,
		fields.Everything())

	_, controller := cache.NewInformer(
		lw,
		&v1.Pod{},
		time.Minute*10,
		cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				pod, ok := obj.(*v1.Pod)
				if !ok {
					log.Printf("Expected Pod but other received %#v", obj)
					return
				}

				for _, cont := range pod.Spec.Containers {
					res := cont.Resources.Requests["nvidia.com/gpu"]
					if !res.IsZero() {
						podGpusArr := []string{}
						if curGpusArr, ok := podGpus[pod.UID]; !ok {
							if curGpusStr, err := ExecCommand(pod.Name, pod.Namespace, "printenv", "NVIDIA_VISIBLE_DEVICES"); err != nil {
								log.Printf("Error getting assigned GPUs from pod %s %s : %s", pod.Namespace, pod.Name, err.Error())
							} else {
								podGpusArr = strings.Split(curGpusStr, ",")
								podGpus[pod.UID] = podGpusArr
							}
						} else {
							podGpusArr = curGpusArr
						}

						// for gpuId := range podGpuArr
						log.Printf("Pod %s %s GPUS: %v", pod.Name, pod.Namespace, podGpusArr)
					}
				}
			},
			DeleteFunc: func(obj interface{}) {
				pod, ok := obj.(*v1.Pod)
				if !ok {
					log.Printf("Expected Pod but other received %#v", obj)
					return
				}
				delete(podGpus, pod.UID)
			},
		},
	)

	stop := make(chan struct{})
	go controller.Run(stop)

	// Wait forever
	select {}
}

//ExecCommand executes arbitrary command inside the pod
func ExecCommand(podName string, namespace string, command ...string) (string, error) {
	var (
		execOut bytes.Buffer
		execErr bytes.Buffer
	)

	pod, err := clientset.Core().Pods(namespace).Get(podName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("could not get pod info: %v", err)
	}

	req := clientset.Core().RESTClient().Post().
		Resource("pods").
		Name(podName).
		Namespace(namespace).
		SubResource("exec")
	req.VersionedParams(&v1.PodExecOptions{
		Container: pod.Spec.Containers[0].Name,
		Command:   command,
		Stdout:    true,
		Stderr:    true,
	}, scheme.ParameterCodec)

	k8sconfig, err := rest.InClusterConfig()
	if err != nil {
		return "", fmt.Errorf("Failed to do inclusterconfig: %v", err)
	}

	exec, err := remotecommand.NewSPDYExecutor(k8sconfig, "POST", req.URL())
	if err != nil {
		return "", fmt.Errorf("failed to init executor: %v", err)
	}

	err = exec.Stream(remotecommand.StreamOptions{
		// SupportedProtocols: remotecommandconsts.SupportedStreamingProtocols,
		Stdout: &execOut,
		Stderr: &execErr,
	})

	if err != nil {
		return "", fmt.Errorf("could not execute: %v", err)
	}

	if execErr.Len() > 0 {
		return "", fmt.Errorf("stderr: %v", execErr.String())
	}

	return execOut.String(), nil
}
