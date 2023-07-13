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
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	customreplicasetv1 "github.com/ishaansehgal99/CustomReplicaSet/api/v1"
)

// CustomReplicaSetReconciler reconciles a CustomReplicaSet object
type CustomReplicaSetReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=customreplicaset.ishaan.microsoft,resources=customreplicasets,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=customreplicaset.ishaan.microsoft,resources=customreplicasets/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=customreplicaset.ishaan.microsoft,resources=customreplicasets/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the CustomReplicaSet object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.15.0/pkg/reconcile
func (r *CustomReplicaSetReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	_ = log.FromContext(ctx)

	// Find the custom replica set instance
	var crs customreplicasetv1.CustomReplicaSet

	// If we don't find it exit
	if err := r.Get(ctx, req.NamespacedName, &crs); err != nil {
		if errors.IsNotFound(err) {
			fmt.Println(err, "Unable to fetch the custom replica set object")
			return ctrl.Result{Requeue: false}, nil
		}
		return ctrl.Result{Requeue: false}, err
	}

	// If we do find it, list all pods owned by it
	var childPods corev1.PodList
	if err := r.List(ctx, &childPods, client.InNamespace(req.Namespace)); err != nil {
		fmt.Println(err, "Failed to list child pods")
		return ctrl.Result{Requeue: false}, err
	}

	// Clean up stale pods - this updates the state of CRD to be most up to date
	// if cleanedPod := cleanupStalePods(crs, childPods.Items); cleanedPod {
	// 	// If we had to remove pods update the CRD
	// 	r.updateCRD(ctx, crs)
	// }
	cleanedPod := cleanupStalePods(crs, childPods.Items)

	// Add new pods - this adds any pods to our local podinfo we had missed to add earlier
	// if addedPod := addNewPods(crs, childPods.Items); addedPod {
	// 	r.updateCRD(ctx, crs)
	// }
	addedPod := addNewPods(crs, childPods.Items)

	if cleanedPod || addedPod {
		r.updateCRD(ctx, crs)
	}

	for crs.Status.CurrentReplicas < crs.Spec.Replicas {
		fmt.Println("Number of available pods is less than the replicas, need to create new pods")
		fmt.Println("AvailablePods: ", crs.Status.CurrentReplicas, "Needed Pods", int(crs.Spec.Replicas))

		newPod := newPodForCR(&crs)
		if err := r.Create(ctx, newPod); err != nil {
			fmt.Println("Unable to create new pod", err)
			return ctrl.Result{Requeue: false}, err
		}
	}

	// r.updateCRD(ctx, crs)

	return ctrl.Result{}, nil
}

func (r *CustomReplicaSetReconciler) updateCRD(ctx context.Context, crs customreplicasetv1.CustomReplicaSet) {
	// Use the local object crs to update the global customreplicasetstatus
	err := r.Status().Update(ctx, &crs)
	if err != nil {
		fmt.Println(err, "Failed to upgrade state of the cluster")
	}
}

func addNewPods(crs customreplicasetv1.CustomReplicaSet, pods []corev1.Pod) bool {
	addedPod := false
	for _, pod := range pods {
		// Check if pod exists
		if pod.Status.Phase == corev1.PodRunning || pod.Status.Phase == corev1.PodPending || pod.Status.Phase == corev1.PodSucceeded {
			// Add this pod to CurrentReplicas and PodStatus
			// Search through our local copy of pods to make sure it doesn't already exist
			foundPod := false
			for _, podStatus := range crs.Status.PodStatus {
				if podStatus.Name == pod.Name {
					foundPod = true
				}
			}
			if !foundPod {
				var podStatusInfo customreplicasetv1.PodStatusInfo
				podStatusInfo.Name = pod.Name
				podStatusInfo.Status = string(pod.Status.Phase)
				// podStatusInfo.RestartCount = podStatus.Status.ContainerStatuses[0].RestartCount

				crs.Status.PodStatus = append(crs.Status.PodStatus, podStatusInfo)
				crs.Status.CurrentReplicas++
				addedPod = true
			}
		}
	}
	return addedPod
}

func cleanupStalePods(crs customreplicasetv1.CustomReplicaSet, pods []corev1.Pod) bool {
	removedPod := false
	for _, pod := range pods {
		// Check if pod exists
		if pod.Status.Phase == corev1.PodFailed || pod.Status.Phase == corev1.PodUnknown {
			// Remove this pod from CurrentReplicas and PodStatus
			// Search through our local copy of pods to remove the faulty pod
			for j, podStatus := range crs.Status.PodStatus {
				if podStatus.Name == pod.Name {
					// Replace pod status with last element
					crs.Status.PodStatus[j] = crs.Status.PodStatus[len(crs.Status.PodStatus)-1]
					// Remove last element
					crs.Status.PodStatus = crs.Status.PodStatus[:len(crs.Status.PodStatus)-1]
					crs.Status.CurrentReplicas--
					removedPod = true
					break
				}
			}
		}
	}
	return removedPod
}

func newPodForCR(cr *customreplicasetv1.CustomReplicaSet) *corev1.Pod {
	// Create label obj
	labels := map[string]string{
		"customreplicaset": cr.Name,
	}

	t := time.Now()
	timestamp := fmt.Sprintf("%d%d%d%d", t.Hour(), t.Minute(), t.Second(), t.Nanosecond())

	// Create pod using K8 API
	return &corev1.Pod{
		//Define Pod Metadata
		ObjectMeta: metav1.ObjectMeta{
			Name:      cr.Name + "-pod-" + timestamp,
			Namespace: cr.Namespace,
			Labels:    labels,
		},
		// Define Pod Spec
		Spec: corev1.PodSpec{
			// Define Container
			Containers: []corev1.Container{{
				Name:    "busybox",
				Image:   "busybox",
				Command: []string{"sleep", "3600"},
			}},
		},
	}
}

// func countAvailablePods(pods []corev1.Pod) int {
// 	available := 0
// 	for _, pod := range pods {
// 		// Check if pod exists
// 		if pod.Status.Phase == corev1.PodRunning || pod.Status.Phase == corev1.PodPending {
// 			available++
// 		}
// 	}
// 	return available
// }

// SetupWithManager sets up the controller with the Manager.
func (r *CustomReplicaSetReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&customreplicasetv1.CustomReplicaSet{}).
		Complete(r)
}
