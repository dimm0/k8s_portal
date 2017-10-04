default: buildrelease

buildgo:
	CGO_ENABLED=0 GOOS=linux go build -a -installsuffix "static" .

builddocker:
	docker build -t us.gcr.io/prp-k8s/oidc-auth:latest .

pushdocker:
	gcloud docker -- push us.gcr.io/prp-k8s/oidc-auth

cleanup:
	rm k8s_portal

buildrelease: buildgo builddocker pushdocker cleanup

builddevdocker:
	docker build -t us.gcr.io/prp-k8s/oidc-auth:latest -f Dockerfile_dev .

builddevrelease: buildgo builddevdocker pushdocker cleanup

pushconfig:
	kubectl delete configmap portal-config -n kube-system
	kubectl create configmap portal-config --from-file=config.toml=config_k8s.toml -n kube-system
