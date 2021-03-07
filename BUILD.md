# build
Install [code-generator](https://github.com/kubernetes/code-generator)
```shell script
export CODEGEN_PKG=<path/to/codegen>
./codegen/update-codegen.sh
go build
```

# docker build
Install [code-generator](https://github.com/kubernetes/code-generator)
```shell script
export CODEGEN_PKG=<path/to/codegen>
./codegen/update-codegen.sh
TAG=1.0.1
docker build . \
  -t kupnu4x/kube-vault-controller:${TAG} \
  -t quay.io/kupnu4x/kube-vault-controller:${TAG}
docker push kupnu4x/kube-vault-controller:${TAG} ;\
docker push quay.io/kupnu4x/kube-vault-controller:${TAG}
```

#helm chart update
```shell script
export HELM_EXPERIMENTAL_OCI=1
TAG=1.0.1
helm chart save helm/ ghcr.io/kupnu4x/helm/kube-vault-controller:${TAG}
helm chart push ghcr.io/kupnu4x/helm/kube-vault-controller:${TAG}
```
