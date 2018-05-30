package main

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/prometheus/client_golang/api/prometheus"
	"github.com/prometheus/common/model"
	"k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/remotecommand"
)

var startTime = time.Now()

var podGpusCache = make(map[types.UID][]string)

var podBothered = make(map[string]string)

var botherSampling = 6 * time.Hour

//https://github.com/zalando-incubator/postgres-operator/blob/master/pkg/cluster/exec.go
func WatchGpuPods() {

	if confMap, err := clientset.Core().ConfigMaps("kube-system").Get("pod-bothered", metav1.GetOptions{}); err == nil {
		podBothered = confMap.Data
	} else {
		log.Printf("Error reading the config pod-bothered: %s", err.Error())
	}

	go func() {
		for range time.Tick(time.Minute) {
			if confMap, err := clientset.Core().ConfigMaps("kube-system").Get("pod-bothered", metav1.GetOptions{}); err == nil {
				confMap.Data = podBothered
				if _, err := clientset.Core().ConfigMaps("kube-system").Update(confMap); err != nil {
					log.Printf("Error updating confMap for podsBothered: %s", err.Error())
				}
			} else {
				if _, err := clientset.Core().ConfigMaps("kube-system").Create(&v1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name: "pod-bothered",
					},
					Data: podBothered,
				}); err != nil {
					log.Printf("Error submiting confMap for podsBothered: %s", err.Error())
				}
			}

		}
	}()

	lw := cache.NewListWatchFromClient(
		clientset.Core().RESTClient(),
		"pods",
		v1.NamespaceAll,
		fields.Everything())

	_, controller := cache.NewInformer(
		lw,
		&v1.Pod{},
		botherSampling,
		cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				pod, ok := obj.(*v1.Pod)
				if !ok {
					log.Printf("Expected Pod but other received %#v", obj)
					return
				}

				checkPod(pod)

			},
			DeleteFunc: func(obj interface{}) {
				pod, ok := obj.(*v1.Pod)
				if !ok {
					log.Printf("Expected Pod but other received %#v", obj)
					return
				}
				delete(podGpusCache, pod.UID)
				delete(podBothered, string(pod.UID))
			},
			UpdateFunc: func(oldObj, newObj interface{}) {
				pod, ok := newObj.(*v1.Pod)
				if !ok {
					log.Printf("Expected Pod but other received %#v", newObj)
					return
				}

				checkPod(pod)
			},
		},
	)

	stop := make(chan struct{})
	go controller.Run(stop)

	// Wait forever
	select {}
}

func checkPod(pod *v1.Pod) {
	if pod.Status.Phase != v1.PodRunning || pod.Status.StartTime.UTC().After(time.Now().Add(time.Duration(-botherSampling))) {
		return
	}

	if botheredTimeStr, ok := podBothered[string(pod.UID)]; ok {
		var botheredTime time.Time
		if err := botheredTime.UnmarshalText([]byte(botheredTimeStr)); err == nil {
			if botheredTime.After(time.Now().Add(time.Duration(-botherSampling + time.Minute))) {
				log.Printf("Not bothering %s too soon", pod.Name)
				return
			}
		}
	}

	for _, cont := range pod.Spec.Containers {
		res := cont.Resources.Requests["nvidia.com/gpu"]
		if !res.IsZero() {
			podGpusCacheArr := []string{}
			if curGpusArr, ok := podGpusCache[pod.UID]; !ok {
				if curGpusStr, err := ExecCommand(pod.Name, pod.Namespace, "printenv", "NVIDIA_VISIBLE_DEVICES"); err != nil {
					log.Printf("Error getting assigned GPUs from pod %s %s : %s", pod.Namespace, pod.Name, err.Error())
				} else {
					podGpusCacheArr = strings.Split(strings.TrimSuffix(curGpusStr, "\n"), ",")
					podGpusCache[pod.UID] = podGpusCacheArr
				}
			} else {
				podGpusCacheArr = curGpusArr
			}

			if len(podGpusCacheArr) == 0 {
				return
			}

			client, err := prometheus.New(prometheus.Config{Address: "http://prometheus-k8s.monitoring.svc.cluster.local:9090"})
			if err != nil {
				log.Printf("%v", err)
				return
			}

			q := prometheus.NewQueryAPI(client)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			val, err := q.Query(ctx, fmt.Sprintf("avg_over_time(nvml_gpu_percent{device_uuid=~\"%s\"}[6h])", strings.Join(podGpusCacheArr, "|")), time.Now())
			if err != nil {
				log.Printf("%v", err)
				return
			}

			//https://github.com/prometheus/client_golang/issues/194
			alert := false
			switch {
			case val.Type() == model.ValVector:
				vectorVal := val.(model.Vector)
				for _, elem := range vectorVal {
					if elem.Value < 2 { // less than 2% avg usage
						alert = true
					}
				}
			}

			if alert {
				userEmails := []string{}
				if userBindings, err := clientset.Rbac().RoleBindings(pod.Namespace).Get("nautilus-admin", metav1.GetOptions{}); err == nil {
					if len(userBindings.Subjects) > 0 {
						for _, userBinding := range userBindings.Subjects {
							if user, err := GetUser(userBinding.Name); err == nil {
								userEmails = append(userEmails, fmt.Sprintf("%s <%s>", user.Spec.Name, user.Spec.Email))
							} else {
								log.Printf("Error getting admins to send emails: %s", err.Error())
							}
						}

					} else {
						log.Printf("No admins found in namespace: %s", pod.Namespace)
					}
				}
				if userBindings, err := clientset.Rbac().RoleBindings(pod.Namespace).Get("nautilus-user", metav1.GetOptions{}); err == nil {
					if len(userBindings.Subjects) > 0 {
						for _, userBinding := range userBindings.Subjects {
							if user, err := GetUser(userBinding.Name); err == nil {
								userEmails = append(userEmails, fmt.Sprintf("%s <%s>", user.Spec.Name, user.Spec.Email))
							} else {
								log.Printf("Error getting users to send emails: %s", err.Error())
							}
						}

					} else {
						log.Printf("No users found in namespace: %s", pod.Namespace)
					}
				}
				if len(userEmails) > 0 {
					botherUsersAboutGpus(userEmails, pod, val.(model.Vector))
				}
			}
		}
	}
}

func botherUsersAboutGpus(destination []string, pod *v1.Pod, values model.Vector) {
	if botherTimeBytes, err := time.Now().MarshalText(); err == nil {
		podBothered[string(pod.UID)] = fmt.Sprintf("%s", botherTimeBytes)
	}
	destination = append(destination, "Dmitry Mishin <dmishin@ucsd.edu>")
	destination = append(destination, "John Graham <jjgraham@ucsd.edu>")
	r := NewMailRequest(destination, "Nautilus cluster: GPUs not utilized")
	// r := NewMailRequest([]string{"dmishin@ucsd.edu", "jjgraham@ucsd.edu"}, "Nautilus cluster: GPUs not utilized")

	log.Printf("Bothering %s", destination)

	gpusArr := []string{}
	for _, elem := range values {
		gpusArr = append(gpusArr, fmt.Sprintf("%s", elem.Metric["device_uuid"]))
	}

	err := r.parseTemplate("templates/gpumail.tmpl", map[string]interface{}{
		"users":      destination,
		"pod":        pod,
		"values":     values,
		"gpusString": strings.Join(gpusArr, "|"),
	})
	if err != nil {
		log.Printf("Error parsing the email template: %s", err.Error())
	}
	log.Printf("Bothering user %s", r.to)
	if err := r.sendMail(); err != nil {
		log.Printf("Failed to send the email to %s : %s\n", r.to, err.Error())
	} else {
		log.Printf("Email has been sent to %s\n", r.to)
	}
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
