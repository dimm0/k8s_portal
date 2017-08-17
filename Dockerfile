FROM scratch
ADD k8s_oidc /
ADD templates /templates
ADD ca-bundle.crt /etc/ssl/certs/
RUN mkdir /sessions
CMD ["/k8s_oidc"]
EXPOSE 80
