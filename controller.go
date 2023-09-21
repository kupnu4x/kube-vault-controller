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
	"context"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	coreinformers "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	vaultprojectv1 "kube-vault-controller/pkg/apis/vaultproject/v1"
	vaultprojectclientset "kube-vault-controller/pkg/generated/clientset/versioned"
	vaultprojectscheme "kube-vault-controller/pkg/generated/clientset/versioned/scheme"
	vaultprojectinformers "kube-vault-controller/pkg/generated/informers/externalversions/vaultproject/v1"
	vaultprojectlisters "kube-vault-controller/pkg/generated/listers/vaultproject/v1"

	vaultapi "github.com/hashicorp/vault/api"
)

const controllerAgentName = "kube-vault-controller"

const (
	SecretCreated    = "SecretCreated"
	SecretUpdated    = "SecretUpdated"
	ErrSecretExists  = "ErrSecretExists"
	ErrVaultRetrieve = "ErrVaultRetrieve"
)

// Controller is the controller implementation for SecretClaim resources
type Controller struct {
	// kubeclientset is a standard kubernetes clientset
	kubeclientset kubernetes.Interface
	// vaultprojectclientset is a clientset for our own API group
	vaultprojectclientset vaultprojectclientset.Interface

	secretsLister      corelisters.SecretLister
	secretsSynced      cache.InformerSynced
	secretClaimsLister vaultprojectlisters.SecretClaimLister
	secretClaimsSynced cache.InformerSynced

	// workqueue is a rate limited work queue. This is used to queue work to be
	// processed instead of performing it as soon as a change happens. This
	// means we can ensure we only process a fixed amount of resources at a
	// time, and makes it easy to ensure we are never processing the same item
	// simultaneously in two different workers.
	workqueue workqueue.RateLimitingInterface
	// recorder is an event recorder for recording Event resources to the
	// Kubernetes API.
	recorder record.EventRecorder

	vaultclient *vaultapi.Client
}

// NewController returns a new controller
func NewController(
	ctx context.Context,
	kubeclientset kubernetes.Interface,
	vaultprojectclientset vaultprojectclientset.Interface,
	secretInformer coreinformers.SecretInformer,
	secretClaimInformer vaultprojectinformers.SecretClaimInformer,
	vaultclient *vaultapi.Client) *Controller {
	logger := klog.FromContext(ctx)

	// Create event broadcaster
	// Add controller types to the default Kubernetes Scheme so Events can be
	// logged for controller types.
	utilruntime.Must(vaultprojectscheme.AddToScheme(scheme.Scheme))
	logger.V(4).Info("Creating event broadcaster")

	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartStructuredLogging(0)
	eventBroadcaster.StartRecordingToSink(&typedcorev1.EventSinkImpl{Interface: kubeclientset.CoreV1().Events("")})
	recorder := eventBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: controllerAgentName})

	controller := &Controller{
		kubeclientset:         kubeclientset,
		vaultprojectclientset: vaultprojectclientset,
		secretsLister:         secretInformer.Lister(),
		secretsSynced:         secretInformer.Informer().HasSynced,
		secretClaimsLister:    secretClaimInformer.Lister(),
		secretClaimsSynced:    secretClaimInformer.Informer().HasSynced,
		workqueue:             workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "SecretClaims"),
		recorder:              recorder,
		vaultclient:           vaultclient,
	}

	logger.Info("Setting up event handlers")
	// Set up an event handler for when SecretClaim resources change
	secretClaimInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: controller.enqueueSecretClaim,
		UpdateFunc: func(old, new interface{}) {
			controller.enqueueSecretClaim(new)
		},
	})
	// Set up an event handler for when Secret resources change. This
	// handler will lookup the owner of the given Secret, and if it is
	// owned by a SecretClaim resource will enqueue that SecretClaim resource for
	// processing. This way, we don't need to implement custom logic for
	// handling Secret resources. More info on this pattern:
	// https://github.com/kubernetes/community/blob/8cafef897a22026d42f5e5bb3f104febe7e29830/contributors/devel/controllers.md
	secretInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: controller.handleObject,
		UpdateFunc: func(old, new interface{}) {
			newSecret := new.(*corev1.Secret)
			oldSecret := old.(*corev1.Secret)
			if newSecret.ResourceVersion == oldSecret.ResourceVersion {
				// Periodic resync will send update events for all known Secrets.
				// Two different versions of the same Secret will always have different RVs.
				return
			}
			controller.handleObject(new)
		},
		DeleteFunc: controller.handleObject,
	})

	return controller
}

// Run will set up the event handlers for types we are interested in, as well
// as syncing informer caches and starting workers. It will block until stopCh
// is closed, at which point it will shutdown the workqueue and wait for
// workers to finish processing their current work items.
func (c *Controller) Run(ctx context.Context, workers int) error {
	defer utilruntime.HandleCrash()
	defer c.workqueue.ShutDown()
	logger := klog.FromContext(ctx)

	// Start the informer factories to begin populating the informer caches
	//logger.Info("Starting SecretClaim controller")

	// Wait for the caches to be synced before starting workers
	logger.Info("Waiting for informer caches to sync")

	if ok := cache.WaitForCacheSync(ctx.Done(), c.secretsSynced, c.secretClaimsSynced); !ok {
		return fmt.Errorf("failed to wait for caches to sync")
	}

	logger.Info("Starting workers", "count", workers)
	// Launch two workers to process SecretClaim resources
	for i := 0; i < workers; i++ {
		go wait.UntilWithContext(ctx, c.runWorker, time.Second)
	}

	logger.Info("Started workers")
	<-ctx.Done()
	logger.Info("Shutting down workers")

	return nil
}

// runWorker is a long-running function that will continually call the
// processNextWorkItem function in order to read and process a message on the
// workqueue.
func (c *Controller) runWorker(ctx context.Context) {
	for c.processNextWorkItem(ctx) {
	}
}

// processNextWorkItem will read a single work item off the workqueue and
// attempt to process it, by calling the syncHandler.
func (c *Controller) processNextWorkItem(ctx context.Context) bool {
	obj, shutdown := c.workqueue.Get()
	logger := klog.FromContext(ctx)

	if shutdown {
		return false
	}

	// We wrap this block in a func so we can defer c.workqueue.Done.
	err := func(obj interface{}) error {
		// We call Done here so the workqueue knows we have finished
		// processing this item. We also must remember to call Forget if we
		// do not want this work item being re-queued. For example, we do
		// not call Forget if a transient error occurs, instead the item is
		// put back on the workqueue and attempted again after a back-off
		// period.
		defer c.workqueue.Done(obj)
		var key string
		var ok bool
		// We expect strings to come off the workqueue. These are of the
		// form namespace/name. We do this as the delayed nature of the
		// workqueue means the items in the informer cache may actually be
		// more up to date that when the item was initially put onto the
		// workqueue.
		if key, ok = obj.(string); !ok {
			// As the item in the workqueue is actually invalid, we call
			// Forget here else we'd go into a loop of attempting to
			// process a work item that is invalid.
			c.workqueue.Forget(obj)
			utilruntime.HandleError(fmt.Errorf("expected string in workqueue but got %#v", obj))
			return nil
		}
		// Run the syncHandler, passing it the namespace/name string of the
		// SecretClaim resource to be synced.
		if err := c.syncHandler(ctx, key); err != nil {
			// Put the item back on the workqueue to handle any transient errors.
			c.workqueue.AddRateLimited(key)
			return fmt.Errorf("error syncing '%s': %s, requeuing", key, err.Error())
		}
		// Finally, if no error occurs we Forget this item so it does not
		// get queued again until another change happens.
		c.workqueue.Forget(obj)
		logger.Info("Synced", "secretClaimName", key)
		return nil
	}(obj)

	if err != nil {
		utilruntime.HandleError(err)
		return true
	}

	return true
}

func (c *Controller) getVaultSecret(kv string, path string) (map[string]string, error) {
	if kv != "v1" && kv != "v2" {
		return nil, fmt.Errorf("wrong kv version")
	}
	if kv == "v2" {
		path = strings.Replace(path, "/", "/data/", 1)
	}
	secret, err := c.vaultclient.Logical().Read(path)
	if err != nil {
		if resperr, ok := err.(*vaultapi.ResponseError); ok {
			if resperr.RawError {
				return nil, fmt.Errorf("vault error %d: %s", resperr.StatusCode, strings.Join(resperr.Errors, "\n"))
			} else if resperr.StatusCode == 403 {
				return nil, fmt.Errorf("permission denied")
			} else if resperr.StatusCode == 503 && len(resperr.Errors) > 0 {
				return nil, fmt.Errorf(resperr.Errors[0])
			}
		}
		return nil, err
	}
	if secret == nil {
		return nil, fmt.Errorf("wrong secret or kv version")
	}

	ifaceData := make(map[string]interface{})
	if kv == "v1" {
		ifaceData = secret.Data
	} else if kv == "v2" {
		if secret.Data["data"] == nil {
			return nil, fmt.Errorf("wrong secret or kv version")
		}
		ifaceData = secret.Data["data"].(map[string]interface{})
	}

	stringData := make(map[string]string)
	for key, value := range ifaceData {
		stringData[key] = value.(string)
	}
	return stringData, nil
}

// syncHandler compares the actual state with the desired, and attempts to
// converge the two. It then updates the Status block of the SecretClaim resource
// with the current status of the resource.
func (c *Controller) syncHandler(ctx context.Context, key string) error {
	// Convert the namespace/name string into a distinct namespace and name
	//logger := klog.LoggerWithValues(klog.FromContext(ctx), "resourceName", key)

	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("invalid resource key: %s", key))
		return nil
	}

	// Get the SecretClaim resource with this namespace/name
	secretClaim, err := c.secretClaimsLister.SecretClaims(namespace).Get(name)
	if err != nil {
		// The SecretClaim resource may no longer exist, in which case we stop
		// processing.
		if errors.IsNotFound(err) {
			utilruntime.HandleError(fmt.Errorf("secretClaim '%s' in work queue no longer exists", key))
			return nil
		}

		return err
	}

	path := secretClaim.Spec.Path
	kv := secretClaim.Spec.Kv
	if path == "" {
		// We choose to absorb the error here as the worker would requeue the
		// resource otherwise. Instead, the next time the resource is updated
		// the resource will be queued again.
		c.updateSecretClaimStatus(secretClaim, "Error", false, false)
		utilruntime.HandleError(fmt.Errorf("%s: path must be specified", key))
		return nil
	}
	if kv != "v1" && kv != "v2" {
		c.updateSecretClaimStatus(secretClaim, "Error", false, false)
		utilruntime.HandleError(fmt.Errorf("%s: wrong kv version", key))
		return nil
	}

	// Get the secret with the name specified in SecretClaim.spec
	secret, err := c.secretsLister.Secrets(secretClaim.Namespace).Get(secretClaim.Name)
	// If the resource doesn't exist, we'll create it
	if errors.IsNotFound(err) {
		vaultSecretData, err := c.getVaultSecret(kv, path)
		if err != nil {
			c.recorder.Event(
				secretClaim,
				corev1.EventTypeWarning,
				ErrVaultRetrieve,
				err.Error(),
			)
			err := c.updateSecretClaimStatus(secretClaim, "Error", false, false)
			//klog.Errorln(err)
			return err
		}
		secret, err = c.kubeclientset.CoreV1().Secrets(secretClaim.Namespace).Create(
			context.TODO(),
			newSecret(secretClaim, vaultSecretData),
			metav1.CreateOptions{},
		)
		if err != nil {
			c.updateSecretClaimStatus(secretClaim, "Error", false, false)
			return err
		}

		c.updateSecretClaimStatus(secretClaim, "InSync", true, true)
		msg := fmt.Sprintf("Secret %s/%s created",
			secret.Namespace,
			secret.Name,
		)
		c.recorder.Event(
			secretClaim,
			corev1.EventTypeNormal,
			SecretCreated,
			msg,
		)
		return nil
	} else if err != nil {
		// If an error occurs during Get/Create, we'll requeue the item so we can
		// attempt processing again later. This could have been caused by a
		// temporary network failure, or any other transient reason.
		c.updateSecretClaimStatus(secretClaim, "Error", false, false)
		return err
	} else {
		//secret found

		// If the Secret is not controlled by this SecretClaim resource, we should log
		// a warning to the event recorder and return error msg.
		if !metav1.IsControlledBy(secret, secretClaim) {
			c.updateSecretClaimStatus(secretClaim, "Error", false, false)
			msg := fmt.Sprintf("Secret %s/%s already exists and is not managed by SecretClaim",
				secret.Namespace,
				secret.Name,
			)
			c.recorder.Event(
				secretClaim,
				corev1.EventTypeWarning,
				ErrSecretExists,
				msg,
			)
			return fmt.Errorf(msg)
		}

		// Periodical renew secret data
		vaultSecretData, err := c.getVaultSecret(kv, path)
		if err != nil {
			c.updateSecretClaimStatus(secretClaim, "Error", false, false)
			c.recorder.Event(
				secretClaim,
				corev1.EventTypeWarning,
				ErrVaultRetrieve,
				err.Error(),
			)
			//klog.Errorln(err)
			return err
		}
		oldResourceVersion := secret.ResourceVersion
		secret, err := c.kubeclientset.CoreV1().Secrets(secretClaim.Namespace).Update(
			context.TODO(),
			newSecret(secretClaim, vaultSecretData),
			metav1.UpdateOptions{},
		)

		// If an error occurs during Update, we'll requeue the item so we can
		// attempt processing again later. This could have been caused by a
		// temporary network failure, or any other transient reason.
		if err != nil {
			c.updateSecretClaimStatus(secretClaim, "Error", false, false)
			return err
		}

		if secret.ResourceVersion != oldResourceVersion {
			msg := fmt.Sprintf("Secret %s/%s updated",
				secret.Namespace,
				secret.Name,
			)
			c.recorder.Event(
				secretClaim,
				corev1.EventTypeNormal,
				SecretUpdated,
				msg,
			)
		}
		c.updateSecretClaimStatus(secretClaim, "InSync", true, secret.ResourceVersion != oldResourceVersion)

		return nil
	}
}

// enqueueSecretClaim takes a SecretClaim resource and converts it into a namespace/name
// string which is then put onto the work queue. This method should *not* be
// passed resources of any type other than SecretClaim.
func (c *Controller) enqueueSecretClaim(obj interface{}) {
	var key string
	var err error
	if key, err = cache.MetaNamespaceKeyFunc(obj); err != nil {
		utilruntime.HandleError(err)
		return
	}
	c.workqueue.Add(key)
}

// handleObject will take any resource implementing metav1.Object and attempt
// to find the SecretClaim resource that 'owns' it. It does this by looking at the
// objects metadata.ownerReferences field for an appropriate OwnerReference.
// It then enqueues that SecretClaim resource to be processed. If the object does not
// have an appropriate OwnerReference, it will simply be skipped.
func (c *Controller) handleObject(obj interface{}) {
	var object metav1.Object
	var ok bool
	logger := klog.FromContext(context.Background())
	if object, ok = obj.(metav1.Object); !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("error decoding object, invalid type"))
			return
		}
		object, ok = tombstone.Obj.(metav1.Object)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("error decoding object tombstone, invalid type"))
			return
		}
		logger.V(4).Info("Recovered deleted object", "resourceName", object.GetName())
	}
	logger.V(4).Info("Processing object", "object", klog.KObj(object))
	if ownerRef := metav1.GetControllerOf(object); ownerRef != nil {
		// If this object is not owned by a SecretClaim, we should not do anything more
		// with it.
		if ownerRef.Kind != "SecretClaim" {
			return
		}

		secretClaim, err := c.secretClaimsLister.SecretClaims(object.GetNamespace()).Get(ownerRef.Name)
		if err != nil {
			logger.V(4).Info("Ignore orphaned object", "object", klog.KObj(object), "secretClaim", ownerRef.Name)
			return
		}

		c.enqueueSecretClaim(secretClaim)
		return
	}
}

// newSecret creates a new Secret for a SecretClaim resource. It also sets
// the appropriate OwnerReferences on the resource so handleObject can discover
// the SecretClaim resource that 'owns' it.
func newSecret(secretClaim *vaultprojectv1.SecretClaim, vaultSecretData map[string]string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretClaim.Name,
			Namespace: secretClaim.Namespace,
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(secretClaim, vaultprojectv1.SchemeGroupVersion.WithKind("SecretClaim")),
			},
		},
		Type:       "Opaque",
		StringData: vaultSecretData,
	}
}

func (c *Controller) updateSecretClaimStatus(secretClaim *vaultprojectv1.SecretClaim, state string, updateLastSyncTime bool, updateLastChangedTime bool) error {
	secretClaimCopy := secretClaim.DeepCopy()
	secretClaimCopy.Status.State = state
	if updateLastSyncTime {
		secretClaimCopy.Status.LastSyncTime = time.Now().UTC().Format(time.RFC3339)
	}
	if updateLastChangedTime {
		secretClaimCopy.Status.LastChangedTime = time.Now().UTC().Format(time.RFC3339)
	}
	_, err := c.vaultprojectclientset.VaultprojectV1().SecretClaims(secretClaim.Namespace).UpdateStatus(context.TODO(), secretClaimCopy, metav1.UpdateOptions{})
	return err
}
