# syntax=docker/dockerfile:1.7
#
# Regnovum — the production image (P19-T01).
#
# The image is composed of three stages, and each one exists for a reason that
# its absence would break:
#
#   web      compiles TypeScript into native ESM with the official `tsc`; the
#            plan forbids a bundler, so the build is `npm ci` + `tsc` and
#            nothing else. Node lives here and never leaves this stage.
#   build    compiles the Go binary and runs `assetgen`, the Go tool that turns
#            the emitted module graph plus `web/src` into immutable hashed
#            asset names and the manifest the server reads back at boot. It
#            consumes `web/generated` from the `web` stage, so the two build
#            systems meet exactly once, in a directory neither of them owns.
#   runtime  contains the binary, the hashed asset build and the distroless
#            base — no package manager, no compiler, no shell, no Node.
#
# Base images are pinned by the *manifest list digest*, never by a floating
# tag: a tag can be repointed under a review, a digest cannot. The tag stays in
# the reference because a digest alone does not say which image it is, and a
# reviewer upgrading a base must not have to reverse a hash to learn what it
# names. `tools/imageaudit` fails the build gate if a `FROM` loses its digest.
#
# The build args carry metadata only — never a credential. `tools/imageaudit`
# refuses a secret-shaped `ARG`/`ENV` name, so a password cannot be smuggled in
# as a build argument and baked into a layer.

# --------------------------------------------------------------------------
# Stage 1 — web: TypeScript 7 -> native ESM (no bundler, by policy)
# --------------------------------------------------------------------------
FROM node:24-bookworm-slim@sha256:0e0ff40c39bc087845bfb27465a0df4ea419520094bc35842ff83dd8cbe6f9b6 AS web

WORKDIR /src/web

# Dependencies first, from the lockfile, so the layer is cached until the lock
# itself changes. `--ignore-scripts` refuses lifecycle scripts: the frontend
# build is `tsc`, and a dependency's install hook is not part of it.
COPY web/package.json web/package-lock.json ./
RUN npm ci --ignore-scripts

# Only what the compiler reads. `web/generated` is an output and is never
# copied in: the stage produces it.
COPY web/tsconfig.json ./
COPY web/src ./src
RUN npm run build

# --------------------------------------------------------------------------
# Stage 2 — build: Go binary + hashed asset build
# --------------------------------------------------------------------------
FROM golang:1.27.1-bookworm@sha256:69a7b9788769bec032d238959b61854e9ae87f57be9029ec04e9885fabf99195 AS build

WORKDIR /src

# Metadata only. The defaults keep the stage runnable by hand; the pipeline
# passes the real values. None of these is a secret.
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown

# The module graph first, so a source edit does not re-download it. `go mod
# download` reads go.sum and refuses a checksum that does not match.
COPY go.mod go.sum ./
RUN go mod download

# Explicit copies, never `COPY . .`: the build context is a directory a person
# controls, and a wildcard copy would ship whatever happens to be in it — a
# local `.env`, a `.git`, a report. `tools/imageaudit` refuses a root copy.
COPY cmd ./cmd
COPY internal ./internal

# The emitted ESM graph comes from stage 1; `web/src` carries the stylesheets
# and the untouched sources the manifest addresses.
COPY --from=web /src/web/generated ./web/generated
COPY web/src ./web/src
RUN go run ./cmd/assetgen \
        -input web/generated \
        -input web/src \
        -output web/dist \
        -manifest web/dist/manifest.json

# CGO_ENABLED=0 produces a static binary, which is what lets the runtime stage
# be distroless/static: no libc, no dynamic loader, nothing to keep patched
# beyond the base itself. `-trimpath` strips the build machine's paths so two
# builds of the same commit agree, and `-buildvcs=false` keeps Git metadata
# (absent from the context anyway) out of the binary.
RUN CGO_ENABLED=0 go build \
        -trimpath \
        -buildvcs=false \
        -ldflags "-s -w \
            -X github.com/AlexandreZanata/Regnovum/internal/buildinfo.version=${VERSION} \
            -X github.com/AlexandreZanata/Regnovum/internal/buildinfo.commit=${COMMIT} \
            -X github.com/AlexandreZanata/Regnovum/internal/buildinfo.date=${BUILD_DATE}" \
        -o /out/arena ./cmd/arena

# --------------------------------------------------------------------------
# Stage 3 — runtime: the binary, the asset build and nothing else
# --------------------------------------------------------------------------
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab AS runtime

# The binary embeds the migrations, the typed i18n catalogs, the billing
# catalog and the IANA timezone database, so the container carries no source
# tree, no `locales/` directory and no system tzdata.
COPY --from=build /out/arena /arena
COPY --from=build /src/web/dist /web/dist

# The container is reached over the Compose network, never from the host: the
# config default (127.0.0.1) would make the process unreachable inside its own
# network namespace, and only Caddy publishes a port (P19-T02/P19-T03). The
# asset directory is absolute because the workdir is the base image's.
ENV ARENA_ADDR=0.0.0.0:8080 \
    ARENA_ASSETS_DIR=/web/dist

# Explicit, even though the `:nonroot` tag already sets it: the user is a
# property of this image, not of whichever base tag a future upgrade chooses.
USER 65532:65532

# Documentation only — publishing is a property of the deployment, not of the
# image.
EXPOSE 8080

ENTRYPOINT ["/arena"]
CMD ["server"]
