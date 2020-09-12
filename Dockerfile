FROM golang:1.15.2-alpine3.12 as build-stage
MAINTAINER Daniel Carbone <daniel.p.carbone@gmail.com>
LABEL application=gipman
LABEL description="gipman build container"

RUN apk add --upgrade --no-cache gcc musl-dev git tzdata

COPY . /build
WORKDIR /build

RUN go build -o gipman

FROM alpine:3.12
MAINTAINER Daniel Carbone <daniel.p.carbone@gmail.com>
LABEL application=gipman
LABEL description="gipman service container"

RUN apk add --upgrade --no-cache tzdata

WORKDIR /opt/gipman
COPY --from=build-stage /build/gipman ./
COPY tmp /tmp

USER nobody

ENTRYPOINT [ "/opt/gipman/gipman" ]