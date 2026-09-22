FROM golang:1-alpine as builder
ARG GOPROXY
ENV GOPROXY=${GOPROXY}

RUN apk add --update git gcc musl-dev linux-headers

ADD . /pbt-artifacts
WORKDIR /pbt-artifacts
RUN go build -v .

FROM alpine:latest
RUN apk add --update tar
ADD . /pbt-artifacts
WORKDIR /pbt-artifacts
COPY --from=builder /pbt-artifacts/pbt-artifacts ./pbt-artifacts

# One tar of the whole fixture tree: a client container takes its files at
# start, and uploading ninety of them one form-part at a time is slower than
# uploading one and untarring it in the shim.
RUN tar -cf fixtures.tar -C fixtures genesis.json manifest.json valid preimages snapshot

ENTRYPOINT ["./pbt-artifacts"]
