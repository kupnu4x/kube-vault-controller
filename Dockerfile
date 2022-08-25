FROM golang:1.19.0 as builder
COPY . /build
WORKDIR /build
RUN CGO_ENABLED=0 go build

#FROM scratch
FROM alpine:3.16.2
#FROM debian:10
COPY --from=builder /build/kube-vault-controller /kube-vault-controller
ENTRYPOINT ["/kube-vault-controller"]
