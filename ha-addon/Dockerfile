# CI builds this once per architecture, with the voidbridge-server binary
# from the "Desktop app & server" job copied in as voidbridge-server-$BUILD_ARCH.
FROM --platform=$BUILDPLATFORM alpine:3.20 AS certs

FROM scratch
ARG BUILD_ARCH
ARG BUILD_VERSION
# For Let's Encrypt (-domain): the server talks to the ACME API over HTTPS.
COPY --from=certs /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --chmod=755 voidbridge-server-${BUILD_ARCH} /usr/bin/voidbridge-server

LABEL io.hass.type="addon" \
      io.hass.arch="${BUILD_ARCH}" \
      io.hass.version="${BUILD_VERSION}" \
      org.opencontainers.image.source="https://github.com/TheFormOfVoid/VoidBridge"

ENTRYPOINT ["/usr/bin/voidbridge-server"]
CMD ["serve", "-options", "/data/options.json", "-data", "/data", "-no-console"]
