# Provision runtime tools separately from the network-free Worker build.
# Resolve the resulting local image ID and pass that immutable ID to the builder.
ARG BASE_IMAGE
FROM ${BASE_IMAGE}
RUN apk add --no-cache git bash ca-certificates
