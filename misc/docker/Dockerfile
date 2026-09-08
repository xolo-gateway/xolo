FROM golang:1.27.0 AS build

RUN apt-get update \
  && apt-get install -y make git

RUN --mount=type=cache,target=/go/pkg/mod/ \
  --mount=type=bind,source=go.sum,target=go.sum \
  --mount=type=bind,source=go.mod,target=go.mod \
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
COPY --from=build /src/dist/xolo-plugin-time-restriction_linux_amd64_v1/xolo-plugin-time-restriction /plugins/time-restriction
COPY --from=build /src/dist/xolo-plugin-dummy-model_linux_amd64_v1/xolo-plugin-dummy-model /plugins/dummy-model
COPY --from=build /src/dist/xolo-plugin-fuzzy-evaluator_linux_amd64_v1/xolo-plugin-fuzzy-evaluator /plugins/fuzzy-evaluator
COPY --from=build /src/dist/xolo-plugin-request-inspector_linux_amd64_v1/xolo-plugin-request-inspector /plugins/request-inspector
COPY --from=build /src/dist/xolo-plugin-complexity-scorer_linux_amd64_v1/xolo-plugin-complexity-scorer /plugins/complexity-scorer
COPY --from=build /src/dist/xolo-plugin-text-classifier_linux_amd64_v1/xolo-plugin-text-classifier /plugins/text-classifier
COPY --from=build /src/dist/xolo-plugin-energy-estimator_linux_amd64_v1/xolo-plugin-energy-estimator /plugins/energy-estimator
COPY --from=build /src/dist/xolo-plugin-budget-pressure_linux_amd64_v1/xolo-plugin-budget-pressure /plugins/budget-pressure
COPY --from=build /src/dist/xolo-plugin-script-processor_linux_amd64_v1/xolo-plugin-script-processor /plugins/script-processor
COPY --from=build /src/dist/xolo-plugin-pseudonymizer_linux_amd64_v1/xolo-plugin-pseudonymizer /plugins/pseudonymizer

ENV XOLO_STORAGE_DATABASE_DSN=/data/data.sqlite
ENV XOLO_PLUGINS_DIR=/plugins

VOLUME /data
VOLUME /root/.cache/go-anon

CMD ["/usr/local/bin/xolo-server"]