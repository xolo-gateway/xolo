# Kept at or above the Go version goreleaser@latest requires: the Makefile
# installs it unpinned, so a toolchain older than its floor fails the build.
FROM golang:1.27.1 AS build

RUN apt-get update \
  && apt-get install -y make git

# The plugin SDK is a nested module pulled in by a local `replace`, so its own
# go.mod has to be readable here too — otherwise this layer resolves the
# replacement to a path that does not exist yet and the download fails.
RUN --mount=type=cache,target=/go/pkg/mod/ \
  --mount=type=bind,source=go.sum,target=go.sum \
  --mount=type=bind,source=go.mod,target=go.mod \
  --mount=type=bind,source=pkg/pluginsdk/go.sum,target=pkg/pluginsdk/go.sum \
  --mount=type=bind,source=pkg/pluginsdk/go.mod,target=pkg/pluginsdk/go.mod \
  go mod download -x

WORKDIR /src

COPY . /src

ARG GOCACHE=/root/.cache/go-build

RUN --mount=type=cache,target=/go/pkg/mod/ \
    --mount=type=cache,target="/root/.cache/go-build" \
    --mount=type=cache,target=./tools \
    make goreleaser GORELEASER_ARGS="build --clean --auto-snapshot --single-target"

FROM alpine:3.23 AS runtime

RUN apk add \
    ca-certificates \
    openssl \
    gcompat \
    tzdata \
  && update-ca-certificates

COPY --from=build /src/dist/xolo-server_linux_amd64_v1/xolo-server /usr/local/bin/xolo-server
COPY --from=build /src/dist/xolo-adoption_linux_amd64_v1/xolo-adoption /usr/local/bin/xolo-adoption
COPY --from=build /src/dist/xolo-plugin-time-restriction_linux_amd64_v1/xolo-plugin-time-restriction /plugins/time-restriction
COPY --from=build /src/dist/xolo-plugin-dummy-model_linux_amd64_v1/xolo-plugin-dummy-model /plugins/dummy-model
COPY --from=build /src/dist/xolo-plugin-fuzzy-evaluator_linux_amd64_v1/xolo-plugin-fuzzy-evaluator /plugins/fuzzy-evaluator
COPY --from=build /src/dist/xolo-plugin-request-inspector_linux_amd64_v1/xolo-plugin-request-inspector /plugins/request-inspector
COPY --from=build /src/dist/xolo-plugin-complexity-scorer_linux_amd64_v1/xolo-plugin-complexity-scorer /plugins/complexity-scorer
COPY --from=build /src/dist/xolo-plugin-text-classifier_linux_amd64_v1/xolo-plugin-text-classifier /plugins/text-classifier
COPY --from=build /src/dist/xolo-plugin-prompt-guard_linux_amd64_v1/xolo-plugin-prompt-guard /plugins/prompt-guard
COPY --from=build /src/dist/xolo-plugin-llm-classifier_linux_amd64_v1/xolo-plugin-llm-classifier /plugins/llm-classifier
COPY --from=build /src/dist/xolo-plugin-energy-estimator_linux_amd64_v1/xolo-plugin-energy-estimator /plugins/energy-estimator
COPY --from=build /src/dist/xolo-plugin-budget-pressure_linux_amd64_v1/xolo-plugin-budget-pressure /plugins/budget-pressure
COPY --from=build /src/dist/xolo-plugin-script-processor_linux_amd64_v1/xolo-plugin-script-processor /plugins/script-processor
COPY --from=build /src/dist/xolo-plugin-pseudonymizer_linux_amd64_v1/xolo-plugin-pseudonymizer /plugins/pseudonymizer

ENV XOLO_STORAGE_DATABASE_DSN=/data/data.sqlite
ENV XOLO_PLUGINS_DIR=/plugins

VOLUME /data
VOLUME /root/.cache/go-anon

CMD ["/usr/local/bin/xolo-server"]