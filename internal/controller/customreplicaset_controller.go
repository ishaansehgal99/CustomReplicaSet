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
	"strings"
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

	availableStdPods, availableUpgPods := countAvailablePods(childPods.Items)
	totalAvailablePods := availableStdPods + availableUpgPods

	if totalAvailablePods < int(crs.Spec.Replicas) {
		for totalAvailablePods < int(crs.Spec.Replicas) {
			var newPod *corev1.Pod
			// Create any missing upgraded pods first
			if availableUpgPods < int(crs.Spec.UpgradedReplicas) {
				newPod = r.newPodForCR(&crs, true)
				availableUpgPods++
			} else {
				newPod = r.newPodForCR(&crs, false)
				availableStdPods++
			}

			if err := r.Create(ctx, newPod); err != nil {
				log.Error(err, "Unable to create new pod")
				return ctrl.Result{Requeue: true}, err
			}
			fmt.Println("Created Pod")
			totalAvailablePods++
		}
	} else if totalAvailablePods > int(crs.Spec.Replicas) {
		podsToDelete := totalAvailablePods - int(crs.Spec.Replicas)
		upgPodsToDelete := availableUpgPods - int(crs.Spec.UpgradedReplicas)
		stdPodsToDelete := podsToDelete - upgPodsToDelete

		for _, pod := range childPods.Items {
			if pod.Status.Phase == corev1.PodRunning || pod.Status.Phase == corev1.PodPending {
				if strings.Contains(pod.ObjectMeta.Name, "upgraded") {
					if upgPodsToDelete > 0 {
						upgPodsToDelete--
					} else {
						continue
					}
				} else {
					if stdPodsToDelete > 0 {
						stdPodsToDelete--
					} else {
						continue
					}
				}

				if err := r.Delete(ctx, &pod); err != nil {
					log.Error(err, "Unable to delete pod")
					return ctrl.Result{Requeue: true}, err
				}
				fmt.Println("Delete Pod")
				totalAvailablePods--
				if totalAvailablePods == int(crs.Spec.Replicas) {
					break
				}
			}
		}
	} else {
		// Check if we need to upgrade or downgrade any pods
		podsToUpgrade := int(crs.Spec.UpgradedReplicas) - availableUpgPods
		if podsToUpgrade > 0 {
			for _, pod := range childPods.Items {
				if pod.Status.Phase == corev1.PodRunning || pod.Status.Phase == corev1.PodPending {
					// Upgrade a standard pod
					if !strings.Contains(pod.ObjectMeta.Name, "upgraded") {
						if err := r.Delete(ctx, &pod); err != nil {
							log.Error(err, "Unable to delete pod")
							return ctrl.Result{Requeue: true}, err
						}

						newPod := r.newPodForCR(&crs, true)
						if err := r.Create(ctx, newPod); err != nil {
							log.Error(err, "Unable to create new pod")
							return ctrl.Result{Requeue: true}, err
						}

						fmt.Println("Upgraded Pod")

						podsToUpgrade--
						if podsToUpgrade == 0 {
							break
						}
					}
				}
			}
		} else if podsToUpgrade < 0 {
			podsToDowngrade := podsToUpgrade * -1

			for _, pod := range childPods.Items {
				if pod.Status.Phase == corev1.PodRunning || pod.Status.Phase == corev1.PodPending {
					// Downgrade an upgraded pod to std
					if strings.Contains(pod.ObjectMeta.Name, "upgraded") {
						if err := r.Delete(ctx, &pod); err != nil {
							log.Error(err, "Unable to delete pod")
							return ctrl.Result{Requeue: true}, err
						}

						newPod := r.newPodForCR(&crs, false)
						if err := r.Create(ctx, newPod); err != nil {
							log.Error(err, "Unable to create new pod")
							return ctrl.Result{Requeue: true}, err
						}

						fmt.Println("Downgraded Pod")

						podsToDowngrade--
						if podsToDowngrade == 0 {
							break
						}
					}
				}
			}

		}

	}

	return ctrl.Result{}, nil
}

func (r *CustomReplicaSetReconciler) newPodForCR(cr *customreplicasetv1.CustomReplicaSet, upgraded bool) *corev1.Pod {
	// Create label obj
	labels := map[string]string{
		"custom": cr.Name,
	}

	t := time.Now()
	timestamp := fmt.Sprintf("%d%d%d%d", t.Hour(), t.Minute(), t.Second(), t.Nanosecond())

	var newPodName string
	if upgraded {
		newPodName = cr.Name + "-pod-upgraded-" + timestamp
	} else {
		newPodName = cr.Name + "-pod-" + timestamp
	}

	// Create pod using the template spec provided in the CustomReplicaSet
	newPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      newPodName,
			Namespace: cr.Namespace,
			Labels:    labels,
		},
		Spec: cr.Spec.Template.Spec,
	}

	for i := range newPod.Spec.Containers {
		if upgraded {
			newPod.Spec.Containers[i].Image = "busybox:latest"
		} else {
			newPod.Spec.Containers[i].Image = "busybox"
		}
	}

	// Set the CustomReplicaSet instance as the owner and controller
	if err := ctrl.SetControllerReference(cr, newPod, r.Scheme); err != nil {
		fmt.Println("Could not set this instance to the customreplicaset controller")
	}
	return newPod
}

func countAvailablePods(pods []corev1.Pod) (int, int) {
	availableStdPods, availableUpgPods := 0, 0
	for _, pod := range pods {
		// Check if pod exists
		if pod.Status.Phase == corev1.PodRunning || pod.Status.Phase == corev1.PodPending {
			container := pod.Spec.Containers[0]
			if strings.HasSuffix(container.Image, ":latest") {
				availableUpgPods++
			} else {
				availableStdPods++
			}
		}
	}
	fmt.Println("Current available standard pods", availableStdPods, "upgraded pods", availableUpgPods)
	return availableStdPods, availableUpgPods
}

// SetupWithManager sets up the controller with the Manager.
func (r *CustomReplicaSetReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&customreplicasetv1.CustomReplicaSet{}).
		Owns(&corev1.Pod{}).
		Complete(r)
}
