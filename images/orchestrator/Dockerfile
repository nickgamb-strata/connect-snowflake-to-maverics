# Custom Orchestrator image that adds the lab root CA to the trust store.
#
# The base image is rebuilt from scratch to strip the VOLUME /etc/maverics
# declaration (added in v2026.03.2). That VOLUME causes Docker to create an
# anonymous volume at /etc/maverics on every container start, which shadows
# the bind-mounted config, certs, and policies that Compose injects into
# sub-paths. Rebuilding from scratch is the standard Docker pattern for
# removing inherited VOLUME declarations.
#
# If a future base image removes the VOLUME, this Dockerfile can simplify
# back to `FROM ${MAVERICS_IMAGE}` with a single COPY for the CA cert.
# Check with: docker inspect <image> --format '{{.Config.Volumes}}'

ARG MAVERICS_IMAGE=strataidentity/maverics:latest

FROM ${MAVERICS_IMAGE} AS maverics

FROM alpine:latest AS certs
COPY --from=maverics /etc/ssl/certs/ca-certificates.crt /certs/ca-certificates.crt
COPY ./certs/rootCA.pem /certs/rootCA.pem
RUN cat /certs/rootCA.pem >> /certs/ca-certificates.crt

FROM scratch
COPY --from=maverics /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=maverics /etc/passwd /etc/passwd
COPY --from=maverics /etc/group /etc/group
COPY --from=maverics /lib/ld-musl-x86_64.so.1 /lib/ld-musl-x86_64.so.1
COPY --from=maverics /usr/local/bin/maverics /usr/local/bin/maverics
COPY --from=certs /certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
EXPOSE 8443
USER maverics:maverics
WORKDIR /etc/maverics
ENTRYPOINT ["/usr/local/bin/maverics"]
