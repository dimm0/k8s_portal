default: buildrelease

buildgo:
	CGO_ENABLED=0 GOOS=linux go build -a -installsuffix "static" .

builddocker:
	docker build -t us.gcr.io/prp-k8s/oidc-auth:bigdipa .

pushdocker:
	gcloud docker -- push us.gcr.io/prp-k8s/oidc-auth:bigdipa

cleanup:
	rm k8s_oidc

buildrelease: buildgo builddocker pushdocker cleanup

builddevdocker:
	docker build -t us.gcr.io/prp-k8s/oidc-auth:bigdipa -f Dockerfile_dev .

builddevrelease: buildgo builddevdocker pushdocker cleanup

pushconfig:
	kubectl create configmap portal-config --from-file=config.toml -n kube-system
