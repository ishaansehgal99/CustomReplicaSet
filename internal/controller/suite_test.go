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
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/stretchr/testify/assert"

	v1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

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

func TestManageControllerHistory(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = customreplicasetv1.AddToScheme(scheme)
	_ = v1.AddToScheme(scheme)

	ctx := context.Background()

	cr := &customreplicasetv1.CustomReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-crs",
			Namespace: "default",
		},
		Spec: customreplicasetv1.CustomReplicaSetSpec{
			RevisionHistoryLimit: 3,
		},
	}

	t.Run("should create a new revision history entry", func(t *testing.T) {
		cl := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(cr).Build()

		reconciler := &CustomReplicaSetReconciler{
			Client: cl,
			Scheme: scheme,
		}

		_, err := reconciler.manageControllerRevisionHistory(ctx, cr)
		assert.NoError(t, err)
		assert.True(t, strings.HasPrefix(cr.Labels["latestRevisionName"], fmt.Sprintf("%s-controller-revision-", cr.Name)))
		assert.Equal(t, cr.Labels["latestRevisionNumber"], "1")

		revisionList := v1.ControllerRevisionList{}
		err = reconciler.Client.List(ctx, &revisionList)
		assert.NoError(t, err)
		assert.Equal(t, len(revisionList.Items), 1)

		err = reconciler.Client.Get(ctx, client.ObjectKey{
			Namespace: cr.Namespace,
			Name:      cr.Name,
		}, cr)

		assert.NoError(t, err)

		jsonSpec, err := convertCRSpecToJson(cr.Spec)
		assert.NoError(t, err)

		revisionData := runtime.RawExtension{Raw: jsonSpec}
		newRevision := revisionList.Items[0]
		assert.Equal(t, newRevision.Data, revisionData)
		assert.Equal(t, newRevision.ObjectMeta.Labels["owner"], cr.Name)
		assert.Equal(t, newRevision.Revision, int64(1))
	})

	t.Run("should use cached revision history", func(t *testing.T) {
		jsonSpec, err := convertCRSpecToJson(cr.Spec)
		assert.NoError(t, err)

		currRevisionData := runtime.RawExtension{Raw: jsonSpec}
		revision := &v1.ControllerRevision{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("%s-controller-revision-1", cr.Name),
				Namespace: cr.Namespace,
			},
			Data:     currRevisionData,
			Revision: 1,
		}
		cr.Labels["latestRevisionName"] = revision.Name

		cl := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(cr, revision).Build()

		reconciler := &CustomReplicaSetReconciler{
			Client: cl,
			Scheme: scheme,
		}

		_, err = reconciler.manageControllerRevisionHistory(ctx, cr)
		assert.NoError(t, err)

		revisionList := v1.ControllerRevisionList{}
		err = reconciler.Client.List(ctx, &revisionList)
		assert.NoError(t, err)
		assert.Equal(t, len(revisionList.Items), 1)

		newRevision := revisionList.Items[0]
		assert.Equal(t, newRevision.Data, revision.Data)
		assert.Equal(t, newRevision.Revision, int64(1))
	})
}
func TestUpdateRevisionToLatest(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = customreplicasetv1.AddToScheme(scheme)
	_ = v1.AddToScheme(scheme)

	cr := &customreplicasetv1.CustomReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-crs",
			Namespace: "default",
		},
	}

	existingRev := &v1.ControllerRevision{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "revision",
			Namespace: "default",
		},
		Revision: 1,
	}

	t.Run("update revision to latest", func(t *testing.T) {
		cl := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(cr, existingRev).Build()
		reconciler := &CustomReplicaSetReconciler{
			Client: cl,
			Scheme: scheme,
		}

		latestRevisionNumber := int64(2)
		err := reconciler.updateRevisionToLatest(context.Background(), cr, existingRev, latestRevisionNumber)

		assert.NoError(t, err)

		updatedRev := &v1.ControllerRevision{}
		err = cl.Get(context.Background(), client.ObjectKey{Namespace: existingRev.Namespace, Name: existingRev.Name}, updatedRev)

		assert.NoError(t, err)
		assert.Equal(t, latestRevisionNumber+1, updatedRev.Revision)

		// Fetch updated CustomReplicaSet
		updatedCR := &customreplicasetv1.CustomReplicaSet{}
		err = cl.Get(context.Background(), client.ObjectKey{Namespace: cr.Namespace, Name: cr.Name}, updatedCR)

		assert.NoError(t, err)

		// Check that the labels were updated correctly
		expectedLabels := map[string]string{
			"latestRevisionName":   existingRev.Name,
			"latestRevisionNumber": strconv.FormatInt(existingRev.Revision, 10),
		}
		assert.Equal(t, expectedLabels, updatedCR.Labels)
	})
}

func TestGetLatestRevision(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = customreplicasetv1.AddToScheme(scheme)
	_ = v1.AddToScheme(scheme)

	cr := &customreplicasetv1.CustomReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-crs",
			Namespace: "default",
		},
	}

	t.Run("should get latest revision", func(t *testing.T) {
		// Create client and reconciler with only one revision
		client := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(cr).Build()

		reconciler := &CustomReplicaSetReconciler{
			Client: client,
			Scheme: scheme,
		}

		controllerRevList := &v1.ControllerRevisionList{}

		rev, revNumber, err := reconciler.getLatestRevision(context.Background(), cr, controllerRevList)

		assert.NoError(t, err)
		assert.Equal(t, int64(0), revNumber)
		assert.Equal(t, (*v1.ControllerRevision)(nil), rev)

		revision1 := &v1.ControllerRevision{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "revision1",
				Namespace: "default",
			},
			Revision: 1,
		}

		controllerRevList.Items = append(controllerRevList.Items, *revision1)
		rev, revNumber, err = reconciler.getLatestRevision(context.Background(), cr, controllerRevList)

		// Assert that with one revision, the latest is revision1
		assert.NoError(t, err)
		assert.Equal(t, int64(1), revNumber)
		assert.Equal(t, "revision1", rev.Name)

		// Add another revision
		revision2 := &v1.ControllerRevision{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "revision2",
				Namespace: "default",
			},
			Revision: 2,
		}

		controllerRevList.Items = append(controllerRevList.Items, *revision2)

		rev, revNumber, err = reconciler.getLatestRevision(context.Background(), cr, controllerRevList)

		// Assert that with two revisions, the latest is revision2
		assert.NoError(t, err)
		assert.Equal(t, int64(2), revNumber)
		assert.Equal(t, "revision2", rev.Name)
	})
}

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

	t.Run("should update the CR revision label", func(t *testing.T) {
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
	})

	t.Run("should not update the CR revision label", func(t *testing.T) {
		err := reconciler.updateCRRevisionLabel(context.Background(), cr, nil)
		assert.Error(t, err)
		assert.Equal(t, "invalid controller revision to update CR label with", err.Error())
	})

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
		_, err := reconciler.createControllerRevision(ctx, cr, controllerRevList, revisionData, int64(latestRevisionNumber))

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
		_, err := reconciler.createControllerRevision(ctx, cr, controllerRevList, revisionData, int64(revisionNum))
		assert.NoError(t, err)
		assert.True(t, strings.HasPrefix(cr.Labels["latestRevisionName"], fmt.Sprintf("%s-controller-revision-", cr.Name)))
		assert.Equal(t, cr.Labels["latestRevisionNumber"], "6")

		revisionList := &v1.ControllerRevisionList{}
		err = reconciler.Client.List(ctx, revisionList)
		assert.NoError(t, err)
		assert.Equal(t, len(revisionList.Items), 1)

		revisionNum = 6
		_, err = reconciler.createControllerRevision(ctx, cr, revisionList, revisionData, int64(revisionNum))
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

var _ = AfterSuite(func() {
	By("tearing down the test environment")
	err := testEnv.Stop()
	Expect(err).NotTo(HaveOccurred())
})
