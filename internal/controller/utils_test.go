/*
2024 NVIDIA CORPORATION & AFFILIATES
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

package controller

import (
	"context"
	"sync"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
)

func createManager() manager.Manager {
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:     scheme.Scheme,
		Metrics:    metricsserver.Options{BindAddress: "0"},
		Controller: config.Controller{SkipNameValidation: ptr.To(true)},
	})
	Expect(err).NotTo(HaveOccurred())
	Expect(mgr).NotTo(BeNil())

	err = mgr.GetCache().IndexField(context.Background(), &v1alpha1.NicDevice{}, "status.node", func(o client.Object) []string {
		return []string{o.(*v1alpha1.NicDevice).Status.Node}
	})
	Expect(err).NotTo(HaveOccurred())

	return mgr
}

// returns namespace name
func createNodeAndRandomNamespace(ctx context.Context, client client.Client) string {
	node := &v1.Node{ObjectMeta: metav1.ObjectMeta{Name: nodeName}}
	Expect(client.Create(ctx, node)).To(Succeed())

	namespaceName := "nic-configuration-operator-" + rand.String(6)
	ns := &v1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name: namespaceName,
	}}
	Expect(client.Create(context.Background(), ns)).To(Succeed())

	return namespaceName
}

func startManager(manager manager.Manager, ctx context.Context, wg *sync.WaitGroup) {
	wg.Add(1)
	go func() {
		log.Log.Info("starting manager for test", "test", GinkgoT().Name())
		defer GinkgoRecover()
		defer wg.Done()
		Expect(manager.Start(ctx)).To(Succeed())
		log.Log.Info("started manager for test", "test", GinkgoT().Name())
	}()

	if !manager.GetCache().WaitForCacheSync(ctx) {
		log.Log.Error(nil, "caches did not sync")
	}

	log.Log.Info("synced manager cache for test", "test", GinkgoT().Name())
}
