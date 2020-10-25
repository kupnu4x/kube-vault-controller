# build
Install [code-generator](https://github.com/kubernetes/code-generator)
```shell
export CODEGEN_PKG=<path/to/codegen>
./codegen/update-codegen.sh
go build
```

# docker build
Install [code-generator](https://github.com/kubernetes/code-generator)
```shell
export CODEGEN_PKG=<path/to/codegen>
./codegen/update-codegen.sh
TAG=1.0.0
docker build . \
  -t kupnu4x/kube-vault-controller:${TAG} \
  -t quay.io/kupnu4x/kube-vault-controller:${TAG}
docker push kupnu4x/kube-vault-controller:${TAG} ;\
docker push quay.io/kupnu4x/kube-vault-controller:${TAG}
```