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
	log := log.FromContext(ctx)

	log.Info("Triggered Reconcile")

	// Find the custom replica set instance
	var crs customreplicasetv1.CustomReplicaSet

	// If we don't find it exit
	if err := r.Get(ctx, req.NamespacedName, &crs); err != nil {
		if errors.IsNotFound(err) {
			log.Error(err, "Unable to fetch the custom replica set object")
			return ctrl.Result{Requeue: false}, nil
		}
		return ctrl.Result{Requeue: false}, err
	}

	// If we do find it, list all pods owned by it
	var childPods corev1.PodList
	if err := r.List(ctx, &childPods, client.InNamespace(req.Namespace)); err != nil {
		log.Error(err, "Failed to list child pods")
		return ctrl.Result{Requeue: false}, err
	}

	// fmt.Println("ISHAAN", crs.Spec.Replicas)

	availablePods := countAvailablePods(childPods.Items)

	if availablePods < int(crs.Spec.Replicas) {
		for availablePods < int(crs.Spec.Replicas) {
			newPod := r.newPodForCR(&crs)
			if err := r.Create(ctx, newPod); err != nil {
				log.Error(err, "Unable to create new pod")
				return ctrl.Result{Requeue: false}, err
			}
			fmt.Println("Created Pod")
			availablePods++
		}
	} else if availablePods > int(crs.Spec.Replicas) {
		for _, pod := range childPods.Items {
			if pod.Status.Phase == corev1.PodRunning || pod.Status.Phase == corev1.PodPending {
				if err := r.Delete(ctx, &pod); err != nil {
					log.Error(err, "Unable to delete pod")
					return ctrl.Result{Requeue: false}, err
				}
				fmt.Println("Delete Pod")
				availablePods--
				if availablePods == int(crs.Spec.Replicas) {
					break
				}
			}
		}
	}

	return ctrl.Result{}, nil
}

func (r *CustomReplicaSetReconciler) updateCRD(ctx context.Context, crs *customreplicasetv1.CustomReplicaSet) {
	// Use the local object crs to update the global customreplicasetstatus
	err := r.Status().Update(ctx, crs)
	if err != nil {
		fmt.Println(err, "Failed to upgrade state of the cluster")
	}
}

func (r *CustomReplicaSetReconciler) newPodForCR(cr *customreplicasetv1.CustomReplicaSet) *corev1.Pod {
	// Create label obj
	labels := map[string]string{
		"custom": cr.Name,
	}

	t := time.Now()
	timestamp := fmt.Sprintf("%d%d%d%d", t.Hour(), t.Minute(), t.Second(), t.Nanosecond())

	// Create pod using the template spec provided in the CustomReplicaSet
	newPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cr.Name + "-pod-" + timestamp,
			Namespace: cr.Namespace,
			Labels:    labels,
		},
		Spec: cr.Spec.Template.Spec,
	}

	// Set the CustomReplicaSet instance as the owner and controller
	if err := ctrl.SetControllerReference(cr, newPod, r.Scheme); err != nil {
		fmt.Println("Could not set this instance to the customreplicaset controller")
	}
	return newPod
}

func countAvailablePods(pods []corev1.Pod) int {
	available := 0
	for _, pod := range pods {
		// Check if pod exists
		if pod.Status.Phase == corev1.PodRunning || pod.Status.Phase == corev1.PodPending {
			available++
		}
	}
	return available
}

// SetupWithManager sets up the controller with the Manager.
func (r *CustomReplicaSetReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&customreplicasetv1.CustomReplicaSet{}).
		Owns(&corev1.Pod{}).
		Complete(r)
}
