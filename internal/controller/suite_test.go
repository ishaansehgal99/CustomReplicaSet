/*
Copyright 2023.

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
	"path/filepath"
	"testing"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/stretchr/testify/assert"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	customreplicasetv1 "github.com/ishaansehgal99/CustomReplicaSet/api/v1"
	//+kubebuilder:scaffold:imports
)

// These tests use Ginkgo (BDD-style Go testing framework). Refer to
// http://onsi.github.io/ginkgo/ to learn more about Ginkgo.

var cfg *rest.Config
var k8sClient client.Client
var testEnv *envtest.Environment

func TestControllers(t *testing.T) {
	RegisterFailHandler(Fail)

	RunSpecs(t, "Controller Suite")
}

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	By("bootstrapping test environment")
	testEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}

	var err error
	// cfg is defined in this file globally.
	cfg, err = testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(cfg).NotTo(BeNil())

	err = customreplicasetv1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	//+kubebuilder:scaffold:scheme

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())
	Expect(k8sClient).NotTo(BeNil())

})

func TestFindCustomReplicaSet(t *testing.T) {
	logger := zap.New(zap.UseDevMode(true))
	scheme := runtime.NewScheme()
	_ = customreplicasetv1.AddToScheme(scheme)

	// Create test request
	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      "test-crs",
			Namespace: "default",
		},
	}

	t.Run("should find the custom replica set", func(t *testing.T) {
		// Create test CustomReplicaSet Object
		customReplicaSet := &customreplicasetv1.CustomReplicaSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-crs",
				Namespace: "default",
			},
			Spec: customreplicasetv1.CustomReplicaSetSpec{},
		}

		// Mock Client
		client := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(customReplicaSet).Build()

		// Create test reconciler
		reconciler := &CustomReplicaSetReconciler{
			Client: client,
			Scheme: scheme,
		}

		// Call function to test
		crs, err := reconciler.findCustomReplicaSet(context.Background(), req, logger)

		assert.NoError(t, err)
		assert.Equal(t, "test-crs", crs.Name)
	})

	t.Run("should return an error when the custom replica set does not exist", func(t *testing.T) {
		// Mock Client
		client := fake.NewClientBuilder().WithScheme(scheme).Build()

		// Create test reconciler
		reconciler := &CustomReplicaSetReconciler{
			Client: client,
			Scheme: scheme,
		}

		_, err := reconciler.findCustomReplicaSet(context.Background(), req, logr.Discard())
		assert.Error(t, err)
	})
}

var _ = AfterSuite(func() {
	By("tearing down the test environment")
	err := testEnv.Stop()
	Expect(err).NotTo(HaveOccurred())
})
