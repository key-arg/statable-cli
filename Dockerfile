# The image GoReleaser builds around the already-compiled binary. There is no
# build stage: the binary arrives in the build context, cross-compiled by the
# same release that produces the archives, so the image and the tarball are the
# same bytes rather than two separate builds that can drift.
#
# distroless/static rather than alpine: the binary is CGO_ENABLED=0, so it needs
# nothing from a distribution except CA certificates, which this image carries.
# No shell and no package manager means nothing to patch and nothing to exec.
FROM gcr.io/distroless/static:nonroot

# One build context serves every platform, so the binaries sit under their own
# platform directories and the COPY has to name the one buildx is currently
# building: `linux/amd64/statable`, `linux/arm64/statable`.
ARG TARGETPLATFORM
COPY $TARGETPLATFORM/statable /usr/bin/statable

# There is no keyring in a container and no home directory to fall back to, so
# the key comes from STATABLE_API_KEY. The CLI already refuses to prompt when it
# cannot see a terminal, which is what makes it usable in someone else's CI.
USER nonroot:nonroot
ENTRYPOINT ["/usr/bin/statable"]
