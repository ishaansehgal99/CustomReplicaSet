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
	"crypto/sha256"
	"encoding/json"
	"fmt"
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

	// Apply any necessary updates to controller revision history
	if err := r.manageControllerRevisionHistory(ctx, &crs); err != nil {
		return ctrl.Result{Requeue: true}, err
	}

	if err := r.managePods(ctx, totalAvailablePods, availableStdPods, availableUpgPods, &crs, childPods, log); err != nil {
		return ctrl.Result{Requeue: true}, err
	}

	return ctrl.Result{Requeue: false}, err
}

// Create a new ControllerRevision and add it to history
func (r *CustomReplicaSetReconciler) createControllerRevision(ctx context.Context, cr *customreplicasetv1.CustomReplicaSet, controllerRevisions *v1.ControllerRevisionList, revisionData runtime.RawExtension, latestRevisionNumber int64) error {
	t := time.Now()
	newRevision := v1.ControllerRevision{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-controller-revision-%d", cr.Name, t.UnixNano()),
			Namespace: cr.Namespace,
			Labels: map[string]string{
				"owner": cr.Name,
			},
		},
		Data:     revisionData,
		Revision: latestRevisionNumber + 1,
	}

	// Delete oldest revision if over revision limit
	if len(controllerRevisions.Items) >= int(cr.Spec.RevisionHistoryLimit) {
		oldestRevision, err := r.getControllerRevisionAtIndex(ctx, cr, controllerRevisions, 0)
		if err != nil {
			return err
		}

		if err := r.Delete(ctx, oldestRevision); err != nil {
			return err
		}
	}

	// Create Revision
	if err := r.Create(ctx, &newRevision); err != nil {
		return err
	}
	// Set CR as its owner
	if err := controllerutil.SetControllerReference(cr, &newRevision, r.Scheme); err != nil {
		return err
	}

	// Add latest revision number to CR Labels
	if err := r.updateCRRevisionLabel(ctx, cr, &newRevision); err != nil {
		return err
	}

	return nil
}

// Update 'revisionNumber' label of the CustomReplicaSet
func (r *CustomReplicaSetReconciler) updateCRRevisionLabel(ctx context.Context, cr *customreplicasetv1.CustomReplicaSet, latestRevision *v1.ControllerRevision) error {
	if latestRevision == nil {
		return fmt.Errorf("invalid controller revision to update CR label with")
	}
	if cr.Labels == nil {
		cr.Labels = map[string]string{}
	}

	cr.Labels["latestRevisionName"] = latestRevision.Name
	cr.Labels["latestRevisionNumber"] = strconv.FormatInt(latestRevision.Revision, 10)
	if err := r.Update(ctx, cr); err != nil {
		return err
	}

	return nil
}

func hashControllerRevisionData(revisionData runtime.RawExtension) (string, error) {
	jsonData, err := json.Marshal(revisionData)
	if err != nil {
		return "", err
	}

	// Compute SHA256 hash of PodTemplate
	hash := sha256.Sum256(jsonData)

	// Convert the hash to hexadecimal string
	hashString := fmt.Sprintf("%x", hash)

	return hashString, nil
}

func (r *CustomReplicaSetReconciler) getAllControllerRevisions(ctx context.Context, cr *customreplicasetv1.CustomReplicaSet, sorted bool) (*v1.ControllerRevisionList, error) {
	controllerRevisions := &v1.ControllerRevisionList{}
	if err := r.List(ctx, controllerRevisions, client.InNamespace(cr.Namespace), client.MatchingLabels{"owner": cr.Name}); err != nil {
		return nil, err
	}

	// Sort the ControllerRevisions by Revision
	if sorted {
		sort.Slice(controllerRevisions.Items, func(i, j int) bool {
			return controllerRevisions.Items[i].Revision < controllerRevisions.Items[j].Revision
		})
	}

	return controllerRevisions, nil
}

func (r *CustomReplicaSetReconciler) manageControllerRevisionHistory(ctx context.Context, cr *customreplicasetv1.CustomReplicaSet) error {
	jsonSpec, err := convertCRSpecToJson(cr.Spec)
	if err != nil {
		return err
	}

	newRevisionData := runtime.RawExtension{Raw: jsonSpec}
	newRevisionHash, err := hashControllerRevisionData(newRevisionData)
	if err != nil {
		return err
	}

	cachedRevision := &v1.ControllerRevision{}
	cachedRevisionHash := ""
	if cachedRevisionName, ok := cr.Labels["latestRevisionName"]; ok {
		if err := r.Get(ctx, client.ObjectKey{Namespace: cr.Namespace, Name: cachedRevisionName}, cachedRevision); err != nil {
			fmt.Println("Cached label for revision name is invalid, need to perform search for latest revision")
		}

		cachedRevisionHash, err = hashControllerRevisionData(cachedRevision.Data)
		if err != nil {
			fmt.Println("Failed to generate hash for CR labeled controller revision data")
			return err
		}
	}

	// Search if the revision we are trying to create has been cached in the cr, or already exists
	// in the revision history, if so update that revision history entry to be the latest,
	// if not create and append a new revision entry
	if cachedRevisionHash != newRevisionHash {
		controllerRevisions, err := r.getAllControllerRevisions(ctx, cr, true)
		if err != nil {
			return err
		}

		_, latestRevisionNumber, err := r.getLatestRevision(ctx, cr, controllerRevisions)
		if err != nil {
			return err
		}

		if existingRev, err := searchRevisionHistory(controllerRevisions, newRevisionHash); err != nil {
			return err
		} else if existingRev != nil {
			// Update the found matching revision to be the latest
			err := r.updateRevisionToLatest(ctx, cr, existingRev, latestRevisionNumber)
			if err != nil {
				fmt.Println("Failed to update existing found revision to be the latest")
			}
		} else {
			if err := r.createControllerRevision(ctx, cr, controllerRevisions, newRevisionData, latestRevisionNumber); err != nil {
				fmt.Println("Failed to create new controller revision")
				return err
			}
		}
	}

	return nil
}

func (r *CustomReplicaSetReconciler) updateRevisionToLatest(ctx context.Context, cr *customreplicasetv1.CustomReplicaSet, existingRev *v1.ControllerRevision, latestRevisionNumber int64) error {
	if existingRev.Revision != latestRevisionNumber {
		existingRev.Revision = latestRevisionNumber + 1
		if err := r.Update(ctx, existingRev); err != nil {
			return err
		}
		if err := r.updateCRRevisionLabel(ctx, cr, existingRev); err != nil {
			return err
		}
	}
	return nil
}

func searchRevisionHistory(controllerRevList *v1.ControllerRevisionList, targetHash string) (*v1.ControllerRevision, error) {
	for _, revision := range controllerRevList.Items {
		revisionHash, err := hashControllerRevisionData(revision.Data)
		if err != nil {
			return nil, err
		}
		if revisionHash == targetHash {
			return &revision, nil
		}
	}
	return nil, nil
}

func (r *CustomReplicaSetReconciler) getLatestRevision(ctx context.Context, cr *customreplicasetv1.CustomReplicaSet, controllerRevisions *v1.ControllerRevisionList) (*v1.ControllerRevision, int64, error) {
	latestRevision, err := r.getControllerRevisionAtIndex(ctx, cr, controllerRevisions, -1)
	if err != nil {
		return nil, -1, err
	}

	if latestRevision == nil {
		return nil, 0, nil
	}
	return latestRevision, latestRevision.Revision, nil
}

func convertCRSpecToJson(spec customreplicasetv1.CustomReplicaSetSpec) ([]byte, error) {
	jsonSpec, err := json.Marshal(spec)
	if err != nil {
		fmt.Println("Failed to convert CustomReplicaSet spec to json")
		return nil, err
	}
	return jsonSpec, nil
}

// Get the x-indexed oldest ControllerRevisionHistory Obj
func (r *CustomReplicaSetReconciler) getControllerRevisionAtIndex(ctx context.Context, cr *customreplicasetv1.CustomReplicaSet, controllerRevisions *v1.ControllerRevisionList, index int) (*v1.ControllerRevision, error) {
	if len(controllerRevisions.Items) == 0 {
		fmt.Println("No controller revisions created yet")
		return nil, nil
	} else if index < -1 || index >= len(controllerRevisions.Items) {
		fmt.Println("Invalid revision history index requested")
		return nil, fmt.Errorf("invalid index requested, out of revision history range")
	}

	// Return the latest ControllerRevision Object
	if index == -1 {
		latestRevision := &controllerRevisions.Items[len(controllerRevisions.Items)-1]
		return latestRevision, nil
	}

	// Return the ControllerRevision at the specified index
	revision := &controllerRevisions.Items[index]
	return revision, nil
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

func printMap(podsPerRevision map[int][]*corev1.Pod) {
	if len(podsPerRevision) == 0 {
		fmt.Println("Empty map")
		return
	}

	for key, value := range podsPerRevision {
		fmt.Printf("Key: %d, Length of Value: %d\n", key, len(value))
		// fmt.Printf("Key: %d, Value: %v, Length of Value: %d\n", key, value, len(value))
	}
}

func sortKeys(podsPerRevision map[int][]*corev1.Pod, reverse bool) []int {
	keys := make([]int, 0, len(podsPerRevision))
	for k := range podsPerRevision {
		keys = append(keys, k)
	}

	if reverse {
		sort.Slice(keys, func(i, j int) bool {
			return keys[i] > keys[j]
		})
	} else {
		sort.Ints(keys)
	}
	return keys
}

// SetupWithManager sets up the controller with the Manager.
func (r *CustomReplicaSetReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&customreplicasetv1.CustomReplicaSet{}).
		Owns(&corev1.Pod{}).
		Complete(r)
}
