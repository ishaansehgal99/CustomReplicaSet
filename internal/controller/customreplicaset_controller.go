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
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	v1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/go-logr/logr"
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
	crs, err := r.findCustomReplicaSet(ctx, req, log)
	if err != nil {
		return ctrl.Result{Requeue: false}, err
	}

	// If we do find it, list all pods owned by it
	childPods, err := r.findChildPods(ctx, req, log)
	if err != nil {
		return ctrl.Result{Requeue: false}, err
	}

	availableStdPods, availableUpgPods := countAvailablePods(childPods.Items)
	totalAvailablePods := availableStdPods + availableUpgPods

	// Load latest controller revision
	var latestRevision *v1.ControllerRevision
	if latestRevision, err = r.getLatestControllerRevision(ctx, &crs); err != nil {
		return ctrl.Result{Requeue: true}, err
	}

	// Apply any necessary updates to controller revision history
	if err := r.manageControllerRevisionHistory(ctx, &crs, latestRevision); err != nil {
		return ctrl.Result{Requeue: true}, err
	}

	if err := r.managePods(ctx, totalAvailablePods, availableStdPods, availableUpgPods, &crs, childPods, log); err != nil {
		return ctrl.Result{Requeue: true}, err
	}

	return ctrl.Result{Requeue: false}, err
}

// Create a new ControllerRevision
func (r *CustomReplicaSetReconciler) createControllerRevision(ctx context.Context, cr *customreplicasetv1.CustomReplicaSet, latestRevision *v1.ControllerRevision, jsonSpec []byte) error {
	var revisionNumber int64
	if latestRevision == nil {
		revisionNumber = 0
	} else {
		revisionNumber = latestRevision.Revision + 1
	}

	t := time.Now()
	timestamp := fmt.Sprintf("%d%d%d%d", t.Hour(), t.Minute(), t.Second(), t.Nanosecond())

	newRevision := v1.ControllerRevision{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cr.Name + "-controller-revision-" + timestamp,
			Namespace: cr.Namespace,
			Labels:    cr.Labels,
		},
		Data:     runtime.RawExtension{Raw: jsonSpec},
		Revision: revisionNumber,
	}
	if err := controllerutil.SetControllerReference(cr, &newRevision, r.Scheme); err != nil {
		return err
	}
	if err := r.Create(ctx, &newRevision); err != nil {
		return err
	}

	// Update 'revisionNumber' label of the CustomReplicaSet
	if cr.Labels == nil {
		cr.Labels = map[string]string{}
	}
	cr.Labels["revisionNumber"] = strconv.FormatInt(newRevision.Revision, 10)
	if err := r.Update(ctx, cr); err != nil {
		return err
	}

	return nil
}

func (r *CustomReplicaSetReconciler) manageControllerRevisionHistory(ctx context.Context, cr *customreplicasetv1.CustomReplicaSet, latestRevision *v1.ControllerRevision) error {
	if latestRevision == nil {
		jsonSpec, err := convertSpecToJson(cr)
		if err != nil {
			return err
		}

		if err := r.createControllerRevision(ctx, cr, latestRevision, jsonSpec); err != nil {
			fmt.Println("Failed to create first controller revision")
			return err
		}
		return nil
	}
	var latestSpec customreplicasetv1.CustomReplicaSetSpec

	if err := json.Unmarshal(latestRevision.Data.Raw, &latestSpec); err != nil {
		fmt.Println("Failed to unmarshal latest controller spec to json")
		return err
	}

	// Compare the PodTemplates
	if !reflect.DeepEqual(cr.Spec.Template, latestSpec.Template) {
		jsonSpec, err := convertSpecToJson(cr)
		if err != nil {
			return err
		}

		if err := r.createControllerRevision(ctx, cr, latestRevision, jsonSpec); err != nil {
			fmt.Println("Failed to create new controller revision")
			return err
		}
	}
	return nil
}

func convertSpecToJson(cr *customreplicasetv1.CustomReplicaSet) ([]byte, error) {
	jsonSpec, err := json.Marshal(cr.Spec)
	if err != nil {
		fmt.Println("Failed to convert CustomReplicaSet spec to json")
		return nil, err
	}
	return jsonSpec, nil
}

func (r *CustomReplicaSetReconciler) getLatestControllerRevision(ctx context.Context, cr *customreplicasetv1.CustomReplicaSet) (*v1.ControllerRevision, error) {
	controllerRevisions := &v1.ControllerRevisionList{}
	if err := r.List(ctx, controllerRevisions, client.InNamespace(cr.Namespace)); err != nil {
		return nil, err
	}

	if len(controllerRevisions.Items) == 0 {
		fmt.Println("No controller revisions created yet")
		return nil, nil
		// return nil, errors.NewNotFound(schema.GroupResource{Group: v1.SchemeGroupVersion.Group, Resource: "controllerrevisions"}, cr.Name)
	}

	// Sort the ControllerRevisions by Revision
	sort.Slice(controllerRevisions.Items, func(i, j int) bool {
		return controllerRevisions.Items[i].Revision < controllerRevisions.Items[j].Revision
	})

	// Return the ControllerRevision with the highest Revision
	latestRevision := &controllerRevisions.Items[len(controllerRevisions.Items)-1]
	return latestRevision, nil
}

func (r *CustomReplicaSetReconciler) findCustomReplicaSet(ctx context.Context, req ctrl.Request, log logr.Logger) (customreplicasetv1.CustomReplicaSet, error) {
	var crs customreplicasetv1.CustomReplicaSet
	if err := r.Get(ctx, req.NamespacedName, &crs); err != nil {
		if errors.IsNotFound(err) {
			log.Error(err, "Unable to fetch the custom replica set object")
		}
		return crs, err
	}
	return crs, nil
}

func (r *CustomReplicaSetReconciler) findChildPods(ctx context.Context, req ctrl.Request, log logr.Logger) (corev1.PodList, error) {
	var childPods corev1.PodList
	if err := r.List(ctx, &childPods, client.InNamespace(req.Namespace)); err != nil {
		log.Error(err, "Failed to list child pods")
		return childPods, err
	}
	return childPods, nil
}

func (r *CustomReplicaSetReconciler) managePods(ctx context.Context, totalAvailablePods, availableStdPods, availableUpgPods int, crs *customreplicasetv1.CustomReplicaSet, childPods corev1.PodList, log logr.Logger) error {
	if totalAvailablePods < int(crs.Spec.Replicas) {
		if err := r.createPods(ctx, totalAvailablePods, availableStdPods, availableUpgPods, crs, log); err != nil {
			log.Error(err, "Failed to create pods")
			return err
		}
	} else if totalAvailablePods > int(crs.Spec.Replicas) {
		if err := r.deletePods(ctx, totalAvailablePods, availableStdPods, availableUpgPods, crs, childPods, log); err != nil {
			log.Error(err, "Failed to delete pods")
			return err
		}
	} else {
		if err := r.upgradeOrDowngradePods(ctx, availableStdPods, availableUpgPods, crs, childPods, log); err != nil {
			log.Error(err, "Failed to upgrade or downgrade pods")
			return err
		}
	}
	return nil
}

func (r *CustomReplicaSetReconciler) createPods(ctx context.Context, totalPods, stdPods, upgPods int, cr *customreplicasetv1.CustomReplicaSet, log logr.Logger) error {
	for totalPods < int(cr.Spec.Replicas) {
		var newPod *corev1.Pod
		// Create any missing upgraded pods first
		if upgPods < int(cr.Spec.Partition) {
			newPod = r.newPodForCR(cr, true)
			upgPods++
		} else {
			newPod = r.newPodForCR(cr, false)
			stdPods++
		}

		if err := r.Create(ctx, newPod); err != nil {
			log.Error(err, "Unable to create new pod")
			return err
		}
		fmt.Println("Created Pod")
		totalPods++
	}
	return nil
}

func (r *CustomReplicaSetReconciler) deletePods(ctx context.Context, totalPods, stdPods, upgPods int, cr *customreplicasetv1.CustomReplicaSet, childPods corev1.PodList, log logr.Logger) error {
	podsToDelete := totalPods - int(cr.Spec.Replicas)
	upgPodsToDelete := upgPods - int(cr.Spec.Partition)
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
				return err
			}
			fmt.Println("Delete Pod")
			totalPods--
			if totalPods == int(cr.Spec.Replicas) {
				break
			}
		}
	}
	return nil
}

func (r *CustomReplicaSetReconciler) upgradeOrDowngradePods(ctx context.Context, stdPods, upgPods int, cr *customreplicasetv1.CustomReplicaSet, childPods corev1.PodList, log logr.Logger) error {
	// Check if we need to upgrade or downgrade any pods
	podsToUpgrade := int(cr.Spec.Partition) - upgPods
	if podsToUpgrade > 0 {
		if err := r.upgradePods(ctx, podsToUpgrade, cr, childPods, log); err != nil {
			log.Error(err, "Unable to upgrade pod")
			return err
		}

	} else if podsToUpgrade < 0 {
		if err := r.downgradePods(ctx, podsToUpgrade, cr, childPods, log); err != nil {
			log.Error(err, "Unable to downgrade pod")
			return err
		}
	}
	return nil
}

func (r *CustomReplicaSetReconciler) upgradePods(ctx context.Context, podsToUpgrade int, cr *customreplicasetv1.CustomReplicaSet, childPods corev1.PodList, log logr.Logger) error {
	for _, pod := range childPods.Items {
		if pod.Status.Phase == corev1.PodRunning || pod.Status.Phase == corev1.PodPending {
			// Upgrade a standard pod
			if !strings.Contains(pod.ObjectMeta.Name, "upgraded") {
				if err := r.Delete(ctx, &pod); err != nil {
					log.Error(err, "Unable to delete pod")
					return err
				}

				newPod := r.newPodForCR(cr, true)
				if err := r.Create(ctx, newPod); err != nil {
					log.Error(err, "Unable to create new pod")
					return err
				}

				fmt.Println("Upgraded Pod")

				podsToUpgrade--
				if podsToUpgrade == 0 {
					break
				}
			}
		}
	}
	return nil
}

func (r *CustomReplicaSetReconciler) downgradePods(ctx context.Context, podsToUpgrade int, cr *customreplicasetv1.CustomReplicaSet, childPods corev1.PodList, log logr.Logger) error {
	podsToDowngrade := podsToUpgrade * -1

	for _, pod := range childPods.Items {
		if pod.Status.Phase == corev1.PodRunning || pod.Status.Phase == corev1.PodPending {
			// Downgrade an upgraded pod to std
			if strings.Contains(pod.ObjectMeta.Name, "upgraded") {
				if err := r.Delete(ctx, &pod); err != nil {
					log.Error(err, "Unable to delete pod")
					return err
				}

				newPod := r.newPodForCR(cr, false)
				if err := r.Create(ctx, newPod); err != nil {
					log.Error(err, "Unable to create new pod")
					return err
				}

				fmt.Println("Downgraded Pod")

				podsToDowngrade--
				if podsToDowngrade == 0 {
					break
				}
			}
		}
	}
	return nil
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
