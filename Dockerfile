FROM golang:1.15.3 as builder
COPY . /build
WORKDIR /build
RUN CGO_ENABLED=0 go build

#FROM scratch
FROM alpine:3.12
#FROM debian:10
COPY --from=builder /build/kube-vault-controller /kube-vault-controller
ENTRYPOINT ["/kube-vault-controller"]
