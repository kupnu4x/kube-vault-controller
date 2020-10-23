/*
Copyright 2017 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/diff"
	kubeinformers "k8s.io/client-go/informers"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	core "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"

	vaultprojectv1 "kube-vault-controller/pkg/apis/vaultproject/v1"
	"kube-vault-controller/pkg/generated/clientset/versioned/fake"
	informers "kube-vault-controller/pkg/generated/informers/externalversions"
)

var (
	alwaysReady        = func() bool { return true }
	noResyncPeriodFunc = func() time.Duration { return 0 }
)

type fixture struct {
	t *testing.T

	client     *fake.Clientset
	kubeclient *k8sfake.Clientset
	// Objects to put in the store.
	secretClaimLister []*vaultprojectv1.SecretClaim
	secretLister      []*corev1.Secret
	// Actions expected to happen on the client.
	kubeactions []core.Action
	actions     []core.Action
	// Objects from here preloaded into NewSimpleFake.
	kubeobjects []runtime.Object
	objects     []runtime.Object
}

func newFixture(t *testing.T) *fixture {
	f := &fixture{}
	f.t = t
	f.objects = []runtime.Object{}
	f.kubeobjects = []runtime.Object{}
	return f
}

func newSecretClaim(name string) *vaultprojectv1.SecretClaim {
	return &vaultprojectv1.SecretClaim{
		TypeMeta: metav1.TypeMeta{APIVersion: vaultprojectv1.SchemeGroupVersion.String()},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: metav1.NamespaceDefault,
		},
		Spec: vaultprojectv1.SecretClaimSpec{
			Path: fmt.Sprintf("stage/%s/data", name),
		},
	}
}

func (f *fixture) newController() (*Controller, informers.SharedInformerFactory, kubeinformers.SharedInformerFactory) {
	f.client = fake.NewSimpleClientset(f.objects...)
	f.kubeclient = k8sfake.NewSimpleClientset(f.kubeobjects...)

	i := informers.NewSharedInformerFactory(f.client, noResyncPeriodFunc())
	k8sI := kubeinformers.NewSharedInformerFactory(f.kubeclient, noResyncPeriodFunc())

	c := NewController(f.kubeclient, f.client,
		k8sI.Core().V1().Secrets(), i.Vaultproject().V1().SecretClaims())

	c.secretClaimsSynced = alwaysReady
	c.secretsSynced = alwaysReady
	c.recorder = &record.FakeRecorder{}

	for _, f := range f.secretClaimLister {
		i.Vaultproject().V1().SecretClaims().Informer().GetIndexer().Add(f)
	}

	for _, d := range f.secretLister {
		k8sI.Core().V1().Secrets().Informer().GetIndexer().Add(d)
	}

	return c, i, k8sI
}

func (f *fixture) run(secretClaimName string) {
	f.runController(secretClaimName, true, false)
}

func (f *fixture) runExpectError(secretClaimName string) {
	f.runController(secretClaimName, true, true)
}

func (f *fixture) runController(secretClaimName string, startInformers bool, expectError bool) {
	c, i, k8sI := f.newController()
	if startInformers {
		stopCh := make(chan struct{})
		defer close(stopCh)
		i.Start(stopCh)
		k8sI.Start(stopCh)
	}

	err := c.syncHandler(secretClaimName)
	if !expectError && err != nil {
		f.t.Errorf("error syncing secretClaim: %v", err)
	} else if expectError && err == nil {
		f.t.Error("expected error syncing secretClaim, got nil")
	}

	actions := filterInformerActions(f.client.Actions())
	for i, action := range actions {
		if len(f.actions) < i+1 {
			f.t.Errorf("%d unexpected actions: %+v", len(actions)-len(f.actions), actions[i:])
			break
		}

		expectedAction := f.actions[i]
		checkAction(expectedAction, action, f.t)
	}

	if len(f.actions) > len(actions) {
		f.t.Errorf("%d additional expected actions:%+v", len(f.actions)-len(actions), f.actions[len(actions):])
	}

	k8sActions := filterInformerActions(f.kubeclient.Actions())
	for i, action := range k8sActions {
		if len(f.kubeactions) < i+1 {
			f.t.Errorf("%d unexpected actions: %+v", len(k8sActions)-len(f.kubeactions), k8sActions[i:])
			break
		}

		expectedAction := f.kubeactions[i]
		checkAction(expectedAction, action, f.t)
	}

	if len(f.kubeactions) > len(k8sActions) {
		f.t.Errorf("%d additional expected actions:%+v", len(f.kubeactions)-len(k8sActions), f.kubeactions[len(k8sActions):])
	}
}

// checkAction verifies that expected and actual actions are equal and both have
// same attached resources
func checkAction(expected, actual core.Action, t *testing.T) {
	if !(expected.Matches(actual.GetVerb(), actual.GetResource().Resource) && actual.GetSubresource() == expected.GetSubresource()) {
		t.Errorf("Expected\n\t%#v\ngot\n\t%#v", expected, actual)
		return
	}

	if reflect.TypeOf(actual) != reflect.TypeOf(expected) {
		t.Errorf("Action has wrong type. Expected: %t. Got: %t", expected, actual)
		return
	}

	switch a := actual.(type) {
	case core.CreateActionImpl:
		e, _ := expected.(core.CreateActionImpl)
		expObject := e.GetObject()
		object := a.GetObject()

		if !reflect.DeepEqual(expObject, object) {
			t.Errorf("Action %s %s has wrong object\nDiff:\n %s",
				a.GetVerb(), a.GetResource().Resource, diff.ObjectGoPrintSideBySide(expObject, object))
		}
	case core.UpdateActionImpl:
		e, _ := expected.(core.UpdateActionImpl)
		expObject := e.GetObject()
		object := a.GetObject()

		if !reflect.DeepEqual(expObject, object) {
			t.Errorf("Action %s %s has wrong object\nDiff:\n %s",
				a.GetVerb(), a.GetResource().Resource, diff.ObjectGoPrintSideBySide(expObject, object))
		}
	case core.PatchActionImpl:
		e, _ := expected.(core.PatchActionImpl)
		expPatch := e.GetPatch()
		patch := a.GetPatch()

		if !reflect.DeepEqual(expPatch, patch) {
			t.Errorf("Action %s %s has wrong patch\nDiff:\n %s",
				a.GetVerb(), a.GetResource().Resource, diff.ObjectGoPrintSideBySide(expPatch, patch))
		}
	default:
		t.Errorf("Uncaptured Action %s %s, you should explicitly add a case to capture it",
			actual.GetVerb(), actual.GetResource().Resource)
	}
}

// filterInformerActions filters list and watch actions for testing resources.
// Since list and watch don't change resource state we can filter it to lower
// nose level in our tests.
func filterInformerActions(actions []core.Action) []core.Action {
	var ret []core.Action
	for _, action := range actions {
		if len(action.GetNamespace()) == 0 &&
			(action.Matches("list", "secretClaims") ||
				action.Matches("watch", "secretClaims") ||
				action.Matches("list", "secrets") ||
				action.Matches("watch", "secrets")) {
			continue
		}
		ret = append(ret, action)
	}

	return ret
}

func (f *fixture) expectCreateSecretAction(secret *corev1.Secret) {
	f.kubeactions = append(f.kubeactions, core.NewCreateAction(schema.GroupVersionResource{Resource: "secrets"}, secret.Namespace, secret))
}

func (f *fixture) expectUpdateSecretAction(secret *corev1.Secret) {
	f.kubeactions = append(f.kubeactions, core.NewUpdateAction(schema.GroupVersionResource{Resource: "secrets"}, secret.Namespace, secret))
}

func getKey(secretClaim *vaultprojectv1.SecretClaim, t *testing.T) string {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(secretClaim)
	if err != nil {
		t.Errorf("Unexpected error getting key for secretClaim %v: %v", secretClaim.Name, err)
		return ""
	}
	return key
}

func TestCreatesSecret(t *testing.T) {
	f := newFixture(t)
	secretClaim := newSecretClaim("test")

	f.secretClaimLister = append(f.secretClaimLister, secretClaim)
	f.objects = append(f.objects, secretClaim)

	expSecret := newSecret(secretClaim)
	f.expectCreateSecretAction(expSecret)

	f.run(getKey(secretClaim, t))
}

func TestDoNothing(t *testing.T) {
	f := newFixture(t)
	secretClaim := newSecretClaim("test")
	secret := newSecret(secretClaim)

	f.secretClaimLister = append(f.secretClaimLister, secretClaim)
	f.objects = append(f.objects, secretClaim)
	f.secretLister = append(f.secretLister, secret)
	f.kubeobjects = append(f.kubeobjects, secret)

	f.run(getKey(secretClaim, t))
}

func TestUpdateSecret(t *testing.T) {
	f := newFixture(t)
	sc := newSecretClaim("test")
	s := newSecret(sc)

	// Update replicas
	sc.Spec.Path = "prod/prod/data"
	expSecret := newSecret(sc)

	f.secretClaimLister = append(f.secretClaimLister, sc)
	f.objects = append(f.objects, sc)
	f.secretLister = append(f.secretLister, s)
	f.kubeobjects = append(f.kubeobjects, s)

	f.expectUpdateSecretAction(expSecret)
	f.run(getKey(sc, t))
}

func TestNotControlledByUs(t *testing.T) {
	f := newFixture(t)
	secretClaim := newSecretClaim("test")
	d := newSecret(secretClaim)

	d.ObjectMeta.OwnerReferences = []metav1.OwnerReference{}

	f.secretClaimLister = append(f.secretClaimLister, secretClaim)
	f.objects = append(f.objects, secretClaim)
	f.secretLister = append(f.secretLister, d)
	f.kubeobjects = append(f.kubeobjects, d)

	f.runExpectError(getKey(secretClaim, t))
}
