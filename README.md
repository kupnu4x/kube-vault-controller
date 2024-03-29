# usage
```yaml
apiVersion: vaultproject.io/v1
kind: SecretClaim
metadata:
  name: test
  namespace: default
spec:
  path: kv2/sandbox/consent
  kv: v2
```
will create secret with same name

# install
create vault policy file policy.hcl:
```hcl
path "kv1/*" {
  capabilities = ["read"]
}
path "kv2/data/*" {
  capabilities = ["read"]
}
```
create policy from file:
```shell script
vault write sys/policy/kube-vault-controller policy=@policy.hcl
```
create periodic renewable token for this policy:
```shell script
vault token create -policy=kube-vault-controller -display-name=kube-vault-controller -period=720h -renewable
```
then install kube-vault-controller
```shell script
helm upgrade --install \
    kube-vault-controller oci://ghcr.io/kupnu4x/helm/kube-vault-controller:1.2.0 -n kube-system \
    --set vaultAddr=https://vault.addr:port/ \
    --set vaultToken=<token>
```
(token will be auto renewed via kube-vault-controller)

## install crd manually
Usually, CustomResourceDefinitions are installed by helm chart and you don't need to install them manually.  
If anyway for some reason you want install them manually:
```shell script
kubectl apply -f https://raw.githubusercontent.com/kupnu4x/kube-vault-controller/1.2.0/helm/crd/crd.yaml
```
then install helm with `createCRD: false`
