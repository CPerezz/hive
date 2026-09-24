FROM golang:1-alpine as builder
ARG GOPROXY
ENV GOPROXY=${GOPROXY}

RUN apk add --update git gcc musl-dev linux-headers

ADD . /pbt-artifacts
WORKDIR /pbt-artifacts
RUN go build -v .

# The fixture set is generated here, not checked in. geth's converter at a
# pinned commit writes the valid pair, and fixtures/gen, compiled inside that
# checkout, holds it to the allocation, the strict decoder and the pinned
# digests before cutting the cases from it. Moving the commit is safe: a
# converter that writes different bytes fails this stage.
FROM golang:1.25-alpine as fixtures
ARG GOPROXY
ENV GOPROXY=${GOPROXY}
ARG geth=CPerezz/go-ethereum
ARG geth_commit=afcaff8749a477a38ed9dd94ed29cd6a71fdb56b

RUN apk add --update git gcc musl-dev linux-headers
RUN git init -q /go-ethereum && \
    git -C /go-ethereum fetch -q --depth 1 https://github.com/${geth} ${geth_commit} && \
    git -C /go-ethereum checkout -q FETCH_HEAD
WORKDIR /go-ethereum
RUN go build -o /usr/local/bin/geth ./cmd/geth
COPY fixtures/gen/*.go fixtures/gen/pbt_root.py ./cmd/pbt-fixtures/
RUN mkdir /fixtures && go run ./cmd/pbt-fixtures -geth /usr/local/bin/geth -out /fixtures

FROM alpine:latest
RUN apk add --update tar
WORKDIR /pbt-artifacts
COPY shims ./shims
COPY --from=builder /pbt-artifacts/pbt-artifacts ./pbt-artifacts
COPY --from=fixtures /fixtures ./fixtures

# One tar of the whole fixture tree: a client container takes its files at
# start, and uploading ninety of them one form-part at a time is slower than
# uploading one and untarring it in the shim.
RUN tar -cf fixtures.tar -C fixtures genesis.json manifest.json valid preimages snapshot

ENTRYPOINT ["./pbt-artifacts"]
