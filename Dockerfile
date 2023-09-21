FROM golang:1.21.1 as builder
COPY . /kube-vault-controller
WORKDIR /kube-vault-controller
RUN git clone https://github.com/kubernetes/code-generator.git
RUN CODEGEN_PKG=./code-generator ./codegen/update-codegen.sh
RUN CGO_ENABLED=0 go build

#FROM scratch
FROM alpine:3.16.2
#FROM debian:10
COPY --from=builder /kube-vault-controller/kube-vault-controller /kube-vault-controller
ENTRYPOINT ["/kube-vault-controller"]
