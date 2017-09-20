FROM scratch
ADD k8s_portal /
ADD templates /templates
ADD ca-bundle.crt /etc/ssl/certs/
CMD ["/k8s_portal"]
EXPOSE 80
