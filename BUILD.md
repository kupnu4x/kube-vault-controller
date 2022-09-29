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
TAG=1.1.0
docker build . \
  -t kupnu4x/kube-vault-controller:${TAG} \
  -t quay.io/kupnu4x/kube-vault-controller:${TAG}
docker push kupnu4x/kube-vault-controller:${TAG} ;\
docker push quay.io/kupnu4x/kube-vault-controller:${TAG}
```

#helm chart update
```shell script
TAG=1.1.0
helm package ./helm/
helm push kube-vault-controller-${TAG}.tgz oci://ghcr.io/kupnu4x/helm/
```
