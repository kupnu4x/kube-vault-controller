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
	"flag"
	"os"
	"strings"
	"time"

	vaultapi "github.com/hashicorp/vault/api"

	kubeinformers "k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
	// Uncomment the following line to load the gcp plugin (only required to authenticate against GKE clusters).
	// _ "k8s.io/client-go/plugin/pkg/client/auth/gcp"

	clientset "kube-vault-controller/pkg/generated/clientset/versioned"
	informers "kube-vault-controller/pkg/generated/informers/externalversions"
	"kube-vault-controller/pkg/signals"
)

var (
	masterURL  string
	kubeconfig string
)

func periodicRenewToken(vaultclient *vaultapi.Client) {
	for {
		ret, err := vaultclient.Auth().Token().RenewSelf(0)
		if err != nil {
			klog.Infoln("token renew failed")
		} else {
			if len(ret.Warnings) > 0 {
				klog.Infof("token renew success (%s)", strings.Join(ret.Warnings, "\n"))
			} else {
				klog.Infoln("token renew success")
			}
		}

		time.Sleep(time.Hour)
	}
}

func main() {
	klog.InitFlags(nil)
	flag.Parse()

	ctx := signals.SetupSignalHandler()
	logger := klog.FromContext(ctx)

	var token = os.Getenv("VAULT_TOKEN")
	var vaultAddr = os.Getenv("VAULT_ADDR")
	if token == "" || vaultAddr == "" {
		klog.Fatalln("vault credentials not set")
	}

	vaultclient, err := vaultapi.NewClient(&vaultapi.Config{
		Address: vaultAddr,
	})
	if err != nil {
		logger.Error(err, "Error creating vault client")
		//klog.Fatalln(err)
	}
	vaultclient.SetToken(token)

	health, err := vaultclient.Sys().Health()
	if err != nil {
		logger.Error(err, "Error checking vault health")
		//klog.Fatalln(err)
	} else if !health.Initialized || health.Sealed {
		logger.Info("Vault not ready(not initialized or sealed)")
		//klog.Fatalln("vault not ready")
	}

	go periodicRenewToken(vaultclient)

	///
	cfg, err := clientcmd.BuildConfigFromFlags(masterURL, kubeconfig)
	if err != nil {
		klog.Fatalf("Error building kubeconfig: %s", err.Error())
	}

	kubeClientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		klog.Fatalf("Error building kubernetes clientset: %s", err.Error())
	}

	vaultprojectClientset, err := clientset.NewForConfig(cfg)
	if err != nil {
		klog.Fatalf("Error building vaultproject.io clientset: %s", err.Error())
	}

	kubeInformerFactory := kubeinformers.NewSharedInformerFactory(kubeClientset, time.Second*60)
	vaultprojectInformerFactory := informers.NewSharedInformerFactory(vaultprojectClientset, time.Second*60)

	controller := NewController(ctx, kubeClientset, vaultprojectClientset,
		kubeInformerFactory.Core().V1().Secrets(),
		vaultprojectInformerFactory.Vaultproject().V1().SecretClaims(),
		vaultclient,
	)

	// notice that there is no need to run Start methods in a separate goroutine. (i.e. go kubeInformerFactory.Start(stopCh)
	// Start method is non-blocking and runs all registered informers in a dedicated goroutine.
	kubeInformerFactory.Start(ctx.Done())
	vaultprojectInformerFactory.Start(ctx.Done())

	if err = controller.Run(ctx, 2); err != nil {
		klog.Fatalf("Error running controller: %s", err.Error())
	}
}

func init() {
	flag.StringVar(&kubeconfig, "kubeconfig", "", "Path to a kubeconfig. Only required if out-of-cluster.")
	flag.StringVar(&masterURL, "master", "", "The address of the Kubernetes API server. Overrides any value in kubeconfig. Only required if out-of-cluster.")
}
