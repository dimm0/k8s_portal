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

	humanize "github.com/dustin/go-humanize"
)

func main() {

	snode := "fiona.sdsu.edu"
	dnode := "k8s-nvme-01.ultralight.org"
	pschedUrl := fmt.Sprintf("https://%s:8443/pscheduler/tasks", snode)

	task := Task{
		Schema: 1,
		Test: Test{
			Type: "throughput",
			Spec: TestSpec{
				Schema:     1,
				SourceNode: snode + ":8443",
				Dest:       dnode,
				// Duration:   "PT3M",
			},
		},
		Schedule: Schedule{},
	}

	defaultTransport := http.DefaultTransport.(*http.Transport)

	fmt.Printf("Measuring throughput %s to %s\n", snode, dnode)

	// Create new Transport that ignores self-signed SSL
	httpClientWithSelfSignedTLS := &http.Transport{
		Proxy:                 defaultTransport.Proxy,
		DialContext:           defaultTransport.DialContext,
		MaxIdleConns:          defaultTransport.MaxIdleConns,
		IdleConnTimeout:       defaultTransport.IdleConnTimeout,
		ExpectContinueTimeout: defaultTransport.ExpectContinueTimeout,
		TLSHandshakeTimeout:   defaultTransport.TLSHandshakeTimeout,
		TLSClientConfig:       &tls.Config{InsecureSkipVerify: true},
	}
	client := &http.Client{Transport: httpClientWithSelfSignedTLS}

	if buf, err := json.Marshal(&task); err == nil {
		if resp, err := client.Post(pschedUrl, "application/json", bytes.NewBuffer(buf)); err == nil {
			defer resp.Body.Close()
			body, _ := ioutil.ReadAll(resp.Body)
			resUrl := fmt.Sprintf("%s/runs/first?wait=30", body)
			resUrl = strings.Replace(resUrl, "\"", "", -1)
			resUrl = strings.Replace(resUrl, "\n", "", -1)

			var result Result

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
				fmt.Printf("error: %s\n", result.Errors)
			} else if result.State == "finished" {
				fmt.Printf("Result throughput: %s/s\n", humanize.Bytes(result.ResultMerged.Summary.Summary.ThroughputBytes))
			} else {
				fmt.Printf("Result: %s\n", result)
			}

		} else {
			fmt.Printf("Error submitting the test: %s\n", err)
		}

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
	Errors       string       `json:"errors"`
	State        string       `json:"state"`
	ResultMerged ResultMerged `json:"result-merged"`
}

type ResultMerged struct {
	Summary Summary `json:"summary"`
}

type Summary struct {
	Summary SSumary `json:"summary"`
}

type SSumary struct {
	ThroughputBytes uint64 `json:"throughput-bytes"`
}

type Task struct {
	Schema   uint16   `json:"schema"`
	Test     Test     `json:"test"`
	Schedule Schedule `json:"schedule"`
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
