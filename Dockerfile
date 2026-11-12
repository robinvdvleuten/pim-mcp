# Built by GoReleaser, which puts the binary for each platform under its
# TARGETPLATFORM directory. Distroless rather than scratch: the server checks
# server certificates, so it needs the CA bundle, and it runs as nonroot.
FROM gcr.io/distroless/static-debian12:nonroot

ARG TARGETPLATFORM

ENTRYPOINT ["/usr/bin/pim-mcp"]
CMD ["serve"]

COPY $TARGETPLATFORM/pim-mcp /usr/bin/
