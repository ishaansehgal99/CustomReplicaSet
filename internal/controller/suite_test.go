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
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
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

func TestUpdateRevisionLabel(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = customreplicasetv1.AddToScheme(scheme)
	_ = v1.AddToScheme(scheme)

	// Create a test CustomReplicaSet and ControllerRevision
	cr := &customreplicasetv1.CustomReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-crs",
			Namespace: "default",
			Labels:    map[string]string{},
		},
		Spec: customreplicasetv1.CustomReplicaSetSpec{},
	}

	rev := &v1.ControllerRevision{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-revision",
			Namespace: "default",
		},
		Revision: 1,
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(cr, rev).Build()

	// Create the test reconciler
	reconciler := &CustomReplicaSetReconciler{
		Client: cl,
		Scheme: scheme,
	}

	// Call the function to test
	err := reconciler.updateCRRevisionLabel(context.Background(), cr, rev)
	assert.NoError(t, err)

	// Check the updated CustomReplicaSet
	updatedCR := &customreplicasetv1.CustomReplicaSet{}
	err = cl.Get(context.Background(), client.ObjectKey{Name: cr.Name, Namespace: cr.Namespace}, updatedCR)
	assert.NoError(t, err)

	// Assert that the labels were updated correctly
	assert.Equal(t, rev.Name, updatedCR.Labels["latestRevisionName"])
	assert.Equal(t, strconv.FormatInt(rev.Revision, 10), updatedCR.Labels["latestRevisionNumber"])
}

func TestCreateControllerRevision(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = customreplicasetv1.AddToScheme(scheme)
	_ = v1.AddToScheme(scheme)

	cr := &customreplicasetv1.CustomReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-crs",
			Namespace: "default",
		},
		Spec: customreplicasetv1.CustomReplicaSetSpec{
			RevisionHistoryLimit: 1,
		},
	}
	t.Run("should create a controller revision", func(t *testing.T) {
		cl := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(cr).Build()

		reconciler := &CustomReplicaSetReconciler{
			Client: cl,
			Scheme: scheme,
		}

		ctx := context.Background()

		controllerRevList := &v1.ControllerRevisionList{}

		revisionData := runtime.RawExtension{
			Raw: []byte(`{"foo":"bar"}`),
		}

		latestRevisionNumber := 1
		err := reconciler.createControllerRevision(ctx, cr, controllerRevList, revisionData, int64(latestRevisionNumber))

		assert.NoError(t, err)
		assert.True(t, strings.HasPrefix(cr.Labels["latestRevisionName"], fmt.Sprintf("%s-controller-revision-", cr.Name)))
		assert.Equal(t, cr.Labels["latestRevisionNumber"], "2")

		revisionList := v1.ControllerRevisionList{}
		err = reconciler.Client.List(ctx, &revisionList)
		assert.NoError(t, err)
		assert.Equal(t, len(revisionList.Items), 1)

		err = reconciler.Client.Get(ctx, client.ObjectKey{
			Namespace: cr.Namespace,
			Name:      cr.Name,
		}, cr)

		assert.NoError(t, err)

		newRevision := revisionList.Items[0]
		assert.Equal(t, newRevision.Data, revisionData)
		assert.Equal(t, newRevision.ObjectMeta.Labels["owner"], cr.Name)
		assert.Equal(t, newRevision.Revision, int64(2))
	})

	t.Run("should maintain revisionHistoryLimit number of controller revisions", func(t *testing.T) {
		cl := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(cr).Build()

		reconciler := &CustomReplicaSetReconciler{
			Client: cl,
			Scheme: scheme,
		}

		ctx := context.Background()

		controllerRevList := &v1.ControllerRevisionList{}

		revisionData := runtime.RawExtension{
			Raw: []byte(`{"foo":"bar"}`),
		}

		revisionNum := 5
		err := reconciler.createControllerRevision(ctx, cr, controllerRevList, revisionData, int64(revisionNum))
		assert.NoError(t, err)
		assert.True(t, strings.HasPrefix(cr.Labels["latestRevisionName"], fmt.Sprintf("%s-controller-revision-", cr.Name)))
		assert.Equal(t, cr.Labels["latestRevisionNumber"], "6")

		revisionList := &v1.ControllerRevisionList{}
		err = reconciler.Client.List(ctx, revisionList)
		assert.NoError(t, err)
		assert.Equal(t, len(revisionList.Items), 1)

		revisionNum = 6
		err = reconciler.createControllerRevision(ctx, cr, revisionList, revisionData, int64(revisionNum))
		assert.NoError(t, err)
		assert.True(t, strings.HasPrefix(cr.Labels["latestRevisionName"], fmt.Sprintf("%s-controller-revision-", cr.Name)))
		assert.Equal(t, cr.Labels["latestRevisionNumber"], "7")

		revisionList = &v1.ControllerRevisionList{}
		err = reconciler.Client.List(ctx, revisionList)
		assert.NoError(t, err)
		// RevisionHistoryLimit should maintain only one controller revision history item
		assert.Equal(t, len(revisionList.Items), 1)

		err = reconciler.Client.Get(ctx, client.ObjectKey{
			Namespace: cr.Namespace,
			Name:      cr.Name,
		}, cr)

		assert.NoError(t, err)

		revision := revisionList.Items[0]
		assert.Equal(t, revision.Data, revisionData)
		assert.Equal(t, revision.ObjectMeta.Labels["owner"], cr.Name)
		assert.Equal(t, revision.Revision, int64(7))
	})
}

func TestGetAllControllerRevisions(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = customreplicasetv1.AddToScheme(scheme)
	_ = v1.AddToScheme(scheme)

	// Create test CustomReplicaSet
	cr := &customreplicasetv1.CustomReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-crs",
			Namespace: "default",
		},
	}

	// Create two ControllerRevisions with different Revisions
	revision2 := &v1.ControllerRevision{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "revision2",
			Namespace: "default",
			Labels:    map[string]string{"owner": "test-crs"},
		},
		Revision: 2,
	}
	revision1 := &v1.ControllerRevision{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "revision1",
			Namespace: "default",
			Labels:    map[string]string{"owner": "test-crs"},
		},
		Revision: 1,
	}
	revision3 := &v1.ControllerRevision{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "revision3",
			Namespace: "default",
			Labels:    map[string]string{"owner": "test-crs"},
		},
		Revision: 3,
	}

	// Mock Client
	client := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(cr, revision2, revision1, revision3).Build()

	// Create test reconciler
	reconciler := &CustomReplicaSetReconciler{
		Client: client,
		Scheme: scheme,
	}

	ctx := context.Background()

	t.Run("should return all controller revisions sorted by revision", func(t *testing.T) {
		revisions, err := reconciler.getAllControllerRevisions(ctx, cr, true)
		assert.NoError(t, err)
		assert.Len(t, revisions.Items, 3)
		assert.Equal(t, "revision1", revisions.Items[0].Name)
		assert.Equal(t, "revision2", revisions.Items[1].Name)
		assert.Equal(t, "revision3", revisions.Items[2].Name)
	})
}

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

func TestFindChildPods(t *testing.T) {
	logger := zap.New(zap.UseDevMode(true))
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	// Create test request
	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      "test-crs",
			Namespace: "default",
		},
	}
	t.Run("should find the child pods", func(t *testing.T) {
		// Create test Pod Objects
		pod1 := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pod1",
				Namespace: "default",
			},
			Spec: corev1.PodSpec{},
		}

		pod2 := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pod2",
				Namespace: "default",
			},
			Spec: corev1.PodSpec{},
		}

		// Mock Client
		client := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(pod1, pod2).Build()

		// Create test reconciler
		reconciler := &CustomReplicaSetReconciler{
			Client: client,
			Scheme: scheme,
		}

		// Call function to test
		pods, err := reconciler.findChildPods(context.Background(), req, logger)

		assert.NoError(t, err)
		assert.Equal(t, 2, len(pods.Items))
		assert.Equal(t, "test-pod1", pods.Items[0].Name)
		assert.Equal(t, "test-pod2", pods.Items[1].Name)
	})

}

var _ = AfterSuite(func() {
	By("tearing down the test environment")
	err := testEnv.Stop()
	Expect(err).NotTo(HaveOccurred())
})
