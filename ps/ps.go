package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"strings"
	"time"
)

var defaultTransport = http.DefaultTransport.(*http.Transport)

// Create new Transport that ignores self-signed SSL
var httpClientWithSelfSignedTLS = &http.Transport{
	Proxy:                 defaultTransport.Proxy,
	DialContext:           defaultTransport.DialContext,
	MaxIdleConns:          defaultTransport.MaxIdleConns,
	IdleConnTimeout:       defaultTransport.IdleConnTimeout,
	ExpectContinueTimeout: defaultTransport.ExpectContinueTimeout,
	TLSHandshakeTimeout:   defaultTransport.TLSHandshakeTimeout,
	TLSClientConfig:       &tls.Config{InsecureSkipVerify: true},
}
var client = &http.Client{Transport: httpClientWithSelfSignedTLS}

func main() {
	snode := "fiona.sdsu.edu"
	dnode := "k8s-nvme-01.ultralight.org"
	// res, _ := RunTest(snode, dnode, "throughput")
	// fmt.Println(res)

	// fmt.Println("\n\n\n")

	res, err := RunTest(snode, dnode, "trace")
	jsonRes, _ := json.MarshalIndent(res, "", "    ")
	fmt.Println(fmt.Printf("%s", jsonRes))
	fmt.Println(err)
}

func RunTest(snode string, dnode string, testType string) (map[string]interface{}, error) {
	pschedUrl := fmt.Sprintf("https://%s:8443/pscheduler/tasks", snode)
	if resp, err := client.Get(pschedUrl); err == nil && resp.StatusCode != 200 {
		pschedUrl = fmt.Sprintf("https://%s/pscheduler/tasks", snode)
	} else {
		snode += ":8443"
	}

	task := Task{
		Schema: 1,
		Test: Test{
			Type: testType,
			Spec: TestSpec{
				Schema:     1,
				SourceNode: snode,
				Dest:       dnode,
				// Duration:   "PT3M",
			},
		},
		Schedule: Schedule{},
	}

	if testType == "trace" {
		task.Tools = []string{"tracepath"}
	}

	fmt.Printf("Measuring %s %s to %s\n", testType, snode, dnode)

	var result Result = Result{ResultMerged: map[string]interface{}{}}

	if buf, err := json.Marshal(&task); err == nil {
		if resp, err := client.Post(pschedUrl, "application/json", bytes.NewBuffer(buf)); err == nil {
			defer resp.Body.Close()
			body, _ := ioutil.ReadAll(resp.Body)
			resUrl := fmt.Sprintf("%s/runs/first?wait=30", body)
			resUrl = strings.Replace(resUrl, "\"", "", -1)
			resUrl = strings.Replace(resUrl, "\n", "", -1)

			err = nil
			state := "pending"
			for err == nil && isExecuting(state) {
				// fmt.Printf("Querying url: %s\n", resUrl)
				if resp, err := client.Get(resUrl); err == nil {
					body, _ := ioutil.ReadAll(resp.Body)
					if merr := json.Unmarshal(body, &result); merr == nil {
						state = result.State
					} else {
						state = "error"
						fmt.Printf("Error getting the state: %s\n", merr)
					}
				} else {
					fmt.Printf("Error submitting the test: %s\n", err)
				}
				if isExecuting(state) {
					time.Sleep(5 * time.Second)
				}
			}

			if result.Errors != "" {
				return result.ResultMerged, fmt.Errorf("result.Errors")
			} else if result.State == "finished" {
				return result.ResultMerged, nil
			} else {
				return result.ResultMerged, fmt.Errorf("Got state: %s", result.State)
			}
		} else {
			return result.ResultMerged, err
		}

	} else {
		return result.ResultMerged, err
	}
}

func isExecuting(state string) bool {
	switch state {
	case "pending",
		"on-deck",
		"running",
		"cleanup":
		return true
	}
	return false
}

type Result struct {
	Errors       string                 `json:"errors"`
	State        string                 `json:"state"`
	ResultMerged map[string]interface{} `json:"result-merged"`
}

type Task struct {
	Schema   uint16   `json:"schema"`
	Test     Test     `json:"test"`
	Schedule Schedule `json:"schedule"`
	Tools    []string `json:"tools,omitempty"`
}

type Test struct {
	Type string   `json:"type"`
	Spec TestSpec `json:"spec"`
}

type TestSpec struct {
	Schema     uint16 `json:"schema"`
	SourceNode string `json:"source-node"`
	Dest       string `json:"dest"`
	Duration   string `json:"duration,omitempty"`
}

type Schedule struct {
}
