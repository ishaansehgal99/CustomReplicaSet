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
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/stretchr/testify/assert"

	v1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
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

func TestSearchRevisionHistory(t *testing.T) {
	revision1Data, _ := json.Marshal(map[string]string{"key": "revision1"})
	revision2Data, _ := json.Marshal(map[string]string{"key": "revision2"})

	controllerRevList := &v1.ControllerRevisionList{
		Items: []v1.ControllerRevision{
			{
				Data: runtime.RawExtension{
					Raw: revision1Data,
				},
			},
			{
				Data: runtime.RawExtension{
					Raw: revision2Data,
				},
			},
		},
	}

	// Mock a targetHash to find in the ControllerRevisionList
	targetHash := "6ba0d659ea642d3617a846270bd11cc3fcf56b6cb1db4b7f322fae18f60717eb" // This is the SHA-256 of "revision1"

	t.Run("should find the controller revision with the given hash", func(t *testing.T) {
		revision, err := searchRevisionHistory(controllerRevList, targetHash)
		assert.NoError(t, err)
		assert.NotNil(t, revision)
		assert.Equal(t, string(revision1Data), string(revision.Data.Raw))
	})

	t.Run("should return nil when the controller revision with the given hash does not exist", func(t *testing.T) {
		revision, err := searchRevisionHistory(controllerRevList, "nonexistenthash")
		assert.NoError(t, err)
		assert.Nil(t, revision)
	})
}

type BadJSON struct {
	metav1.TypeMeta // Include this to add the GetObjectKind() method
}

func (b BadJSON) MarshalJSON() ([]byte, error) {
	return nil, errors.New("this is a marshal error")
}

func (b *BadJSON) DeepCopyObject() runtime.Object {
	return &BadJSON{}
}

func TestHashControllerRevisionData(t *testing.T) {
	t.Run("should hash the revision data successfully", func(t *testing.T) {
		// Define ControllerRevision data
		revisionData := runtime.RawExtension{
			Raw: []byte(`{"foo":"bar"}`),
		}

		// Call function to test
		hash, err := hashControllerRevisionData(revisionData)

		// Define the expected result (I've pre-computed the hash for {"foo":"bar"} here)
		expectedHash := "7a38bf81f383f69433ad6e900d35b3e2385593f76a7b7ab5d4355b8ba41ee24b"

		assert.NoError(t, err)
		assert.Equal(t, expectedHash, hash)
	})

	t.Run("should return an error when marshalling fails", func(t *testing.T) {
		// Define ControllerRevision data with unmarshallable data
		revisionData := runtime.RawExtension{
			Object: &BadJSON{}, // This type will fail to marshal
		}

		// Call function to test
		_, err := hashControllerRevisionData(revisionData)

		assert.Error(t, err)
	})
}

func TestConvertCRSpecToJson(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = customreplicasetv1.AddToScheme(scheme)

	t.Run("should convert cr spec to json", func(t *testing.T) {
		// Create test CustomReplicaSetSpec Object
		spec := customreplicasetv1.CustomReplicaSetSpec{
			Replicas:             10,
			Partition:            5,
			RevisionHistoryLimit: 20,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					RestartPolicy: "Never",
					Containers: []corev1.Container{
						{
							Name:    "busybox",
							Image:   "busybox:latest",
							Command: []string{"sleep", "3600"},
						},
					},
				},
			},
		}

		// Convert the spec to JSON
		jsonSpec, err := convertCRSpecToJson(spec)

		// There should be no error
		assert.NoError(t, err)

		// Now unmarshal the jsonSpec back to a CustomReplicaSetSpec object
		var unmarshaledSpec customreplicasetv1.CustomReplicaSetSpec
		err = json.Unmarshal(jsonSpec, &unmarshaledSpec)

		// There should be no error
		assert.NoError(t, err)

		// The unmarshaledSpec should be same as original spec
		assert.Equal(t, spec, unmarshaledSpec)
	})
}

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
