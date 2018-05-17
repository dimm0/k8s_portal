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

//https://github.com/zalando-incubator/postgres-operator/blob/master/pkg/cluster/exec.go
func WatchGpuPods() {
	time.Sleep(10 * time.Minute)
	podGpus := make(map[types.UID][]string)

	lw := cache.NewListWatchFromClient(
		clientset.Core().RESTClient(),
		"pods",
		v1.NamespaceAll,
		fields.Everything())

	_, controller := cache.NewInformer(
		lw,
		&v1.Pod{},
		time.Hour*6,
		cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				pod, ok := obj.(*v1.Pod)
				if !ok {
					log.Printf("Expected Pod but other received %#v", obj)
					return
				}

				if pod.Status.Phase != v1.PodRunning || pod.Status.StartTime.UTC().After(time.Now().Add(time.Duration(-6)*time.Hour)) {
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
								podGpusArr = strings.Split(strings.TrimSuffix(curGpusStr, "\n"), ",")
								podGpus[pod.UID] = podGpusArr
							}
						} else {
							podGpusArr = curGpusArr
						}

						if len(podGpusArr) == 0 {
							return
						}

						// for gpuId := range podGpuArr
						// log.Printf("Pod %s %s GPUS: %v", pod.Name, pod.Namespace, podGpusArr)

						client, err := prometheus.New(prometheus.Config{Address: "http://prometheus-k8s.monitoring.svc.cluster.local:9090"})
						if err != nil {
							log.Printf("%v", err)
							return
						}

						q := prometheus.NewQueryAPI(client)

						ctx, cancel := context.WithCancel(context.Background())
						defer cancel()

						val, err := q.Query(ctx, fmt.Sprintf("avg_over_time(nvml_gpu_percent{device_uuid=~\"%s\"}[6h])", strings.Join(podGpusArr, "|")), time.Now())
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
								if elem.Value < 2 {
									alert = true
								}
							}
						}

						if alert {
							if userBindings, err := clientset.Rbac().RoleBindings(pod.Namespace).Get("nautilus-admin", metav1.GetOptions{}); err == nil {
								if len(userBindings.Subjects) > 0 {
									userEmails := []string{}
									for _, userBinding := range userBindings.Subjects {
										if user, err := GetUser(userBinding.Name); err == nil {
											userEmails = append(userEmails, fmt.Sprintf("%s <%s>", user.Spec.Name, user.Spec.Email))
										} else {
											log.Printf("Error getting users to send emails: %s", err.Error())
										}
									}

									BotherUsersAboutGpus(userEmails, pod, val.(model.Vector))
								} else {
									log.Printf("No admins found in namespace: %s", pod.Namespace)
								}
							}
						}
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

func BotherUsersAboutGpus(destination []string, pod *v1.Pod, values model.Vector) {
	destination = append(destination, "Dmitry Mishin <dmishin@ucsd.edu>")
	destination = append(destination, "John Graham <jjgraham@ucsd.edu>")
	r := NewMailRequest(destination, "Nautilus cluster: GPUs not utilized")
	// r := NewMailRequest([]string{"dmishin@ucsd.edu", "jjgraham@ucsd.edu"}, "Nautilus cluster: GPUs not utilized")

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
