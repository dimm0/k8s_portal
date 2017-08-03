FROM scratch
ADD k8s_oidc config.toml /
ADD ca-bundle.crt /etc/ssl/certs/
CMD ["/k8s_oidc"]
EXPOSE 80
