default: buildrelease

buildgo:
	CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo .

builddocker:
	docker build -t us.gcr.io/prp-k8s/oidc-auth:latest .

pushdocker:
	gcloud docker -- push us.gcr.io/prp-k8s/oidc-auth

cleanup:
	rm k8s_oidc

buildrelease: buildgo builddocker pushdocker cleanup
