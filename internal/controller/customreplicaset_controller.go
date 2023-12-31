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

	// Apply any necessary updates to controller revision history
	latestRevision, err := r.manageControllerRevisionHistory(ctx, &crs)
	if err != nil {
		return ctrl.Result{Requeue: true}, err
	}

	podsPerRevision, totalAvailPods, err := countAvailablePods(childPods.Items)
	if err != nil {
		return ctrl.Result{Requeue: true}, err
	}

	if err := r.managePods(ctx, &crs, podsPerRevision, totalAvailPods, latestRevision); err != nil {
		return ctrl.Result{Requeue: true}, err
	}

	return ctrl.Result{Requeue: false}, err
}

// Create a new ControllerRevision and add it to history
func (r *CustomReplicaSetReconciler) createControllerRevision(ctx context.Context, cr *customreplicasetv1.CustomReplicaSet, controllerRevisions *v1.ControllerRevisionList, revisionData runtime.RawExtension, latestRevisionNumber int64) (*v1.ControllerRevision, error) {
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
			return nil, err
		}

		if err := r.Delete(ctx, oldestRevision); err != nil {
			return nil, err
		}
	}

	// Create Revision
	if err := r.Create(ctx, &newRevision); err != nil {
		return nil, err
	}
	// Set CR as its owner
	if err := controllerutil.SetControllerReference(cr, &newRevision, r.Scheme); err != nil {
		return nil, err
	}

	// Add latest revision number to CR Labels
	if err := r.updateCRRevisionLabel(ctx, cr, &newRevision); err != nil {
		return nil, err
	}

	return &newRevision, nil
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

func (r *CustomReplicaSetReconciler) getControllerRevisionExcludingRevision(ctx context.Context, cr *customreplicasetv1.CustomReplicaSet, excludeRevision int64) (*v1.ControllerRevision, error) {
	controllerRevisions, err := r.getAllControllerRevisions(ctx, cr, false)
	if err != nil {
		return nil, fmt.Errorf("could not get all the controller revisions")
	}

	// If there is only one controller revision so far
	// (e.g. cluster startup) then that is the only
	// viable revision, return it
	if len(controllerRevisions.Items) == 1 {
		return &controllerRevisions.Items[0], nil
	}

	for _, revision := range controllerRevisions.Items {
		if revision.Revision != excludeRevision {
			return &revision, nil
		}
	}

	return nil, fmt.Errorf("no matching ControllerRevision found for %s excluding revision %d", cr.Name, excludeRevision)
}

func (r *CustomReplicaSetReconciler) manageControllerRevisionHistory(ctx context.Context, cr *customreplicasetv1.CustomReplicaSet) (*v1.ControllerRevision, error) {
	jsonSpec, err := convertPodTemplateToJson(cr.Spec.Template)
	if err != nil {
		return nil, err
	}

	newRevisionData := runtime.RawExtension{Raw: jsonSpec}
	newRevisionHash, err := hashControllerRevisionData(newRevisionData)
	if err != nil {
		return nil, err
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
			return nil, err
		}
	}

	// Search if the revision we are trying to create has been cached in the cr, or already exists
	// in the revision history, if so update that revision history entry to be the latest,
	// if not create and append a new revision entry
	if cachedRevisionHash != newRevisionHash {
		controllerRevisions, err := r.getAllControllerRevisions(ctx, cr, true)
		if err != nil {
			return nil, err
		}

		_, latestRevisionNumber, err := r.getLatestRevision(ctx, cr, controllerRevisions)
		if err != nil {
			return nil, err
		}

		if existingRev, err := searchRevisionHistory(controllerRevisions, newRevisionHash); err != nil {
			return nil, err
		} else if existingRev != nil {
			// Update the found matching revision to be the latest
			err := r.updateRevisionToLatest(ctx, cr, existingRev, latestRevisionNumber)
			if err != nil {
				fmt.Println("Failed to update existing found revision to be the latest")
			}
			return existingRev, nil
		} else {
			latestRevision, err := r.createControllerRevision(ctx, cr, controllerRevisions, newRevisionData, latestRevisionNumber)
			if err != nil {
				fmt.Println("Failed to create new controller revision")
				return nil, err
			}
			return latestRevision, nil
		}
	}

	return cachedRevision, nil
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

func convertPodTemplateToJson(template corev1.PodTemplateSpec) ([]byte, error) {
	jsonSpec, err := json.Marshal(template)
	if err != nil {
		fmt.Println("Failed to convert Pod Template to json")
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

func (r *CustomReplicaSetReconciler) managePods(ctx context.Context, crs *customreplicasetv1.CustomReplicaSet, podsPerRevision map[int][]*corev1.Pod, totalAvailPods int, latestRevision *v1.ControllerRevision) error {
	partitionNum := min(int(crs.Spec.Partition), int(crs.Spec.Replicas)) // Cap the partition upgrade amount by the # of replicas
	latestPods, ok := podsPerRevision[int(latestRevision.Revision)]
	if !ok {
		fmt.Println("No pods found of latest revision")
		latestPods = []*corev1.Pod{}
	}
	if totalAvailPods < int(crs.Spec.Replicas) {
		podsToCreate := int(crs.Spec.Replicas) - totalAvailPods
		newPodsToCreate := max(partitionNum-len(latestPods), 0)
		oldPodsToCreate := podsToCreate - newPodsToCreate
		if newPodsToCreate > 0 {
			if err := r.createPods(ctx, crs, newPodsToCreate, latestRevision); err != nil {
				fmt.Println("Failed to create pods of latest version", err)
				return err
			}
		}

		if oldPodsToCreate > 0 {
			oldRevision, err := r.getControllerRevisionExcludingRevision(ctx, crs, latestRevision.Revision)
			if err != nil {
				fmt.Println("Failed to get a controller revision excluding the latest one")
				return err
			}
			if err := r.createPods(ctx, crs, oldPodsToCreate, oldRevision); err != nil {
				fmt.Println("Failed to create pods", err)
				return err
			}
		}
	} else if totalAvailPods > int(crs.Spec.Replicas) {
		podsToDelete := totalAvailPods - int(crs.Spec.Replicas)
		var newPodsToDelete, oldPodsToDelete int
		if len(podsPerRevision) == 1 {
			newPodsToDelete = 0
			oldPodsToDelete = podsToDelete
		} else {
			newPodsToDelete = max(len(latestPods)-partitionNum, 0)
			oldPodsToDelete = podsToDelete - newPodsToDelete
		}
		if newPodsToDelete > 0 {
			if err := r.deletePods(ctx, crs, newPodsToDelete, podsPerRevision, true); err != nil {
				fmt.Println("Failed to delete pods", err)
				return err
			}
		}
		if oldPodsToDelete > 0 {
			if err := r.deletePods(ctx, crs, oldPodsToDelete, podsPerRevision, false); err != nil {
				fmt.Println("Failed to delete pods", err)
				return err
			}
		}
	} else {
		podsToUpgrade := partitionNum - len(latestPods)
		if err := r.upgradeOrDowngradePods(ctx, crs, podsPerRevision, latestRevision, podsToUpgrade); err != nil {
			fmt.Println("Failed to upgrade or downgrade pods", err)
			return err
		}
	}
	return nil
}

func (r *CustomReplicaSetReconciler) createPods(ctx context.Context, cr *customreplicasetv1.CustomReplicaSet, podsToCreate int, revision *v1.ControllerRevision) error {
	for podsToCreate > 0 {
		// If we are short pods, create new pods of latest revision
		newPod, err := r.newPodForCR(cr, revision)
		if err != nil {
			fmt.Println("Unable to initialize new pod for creation", err)
			return err
		}

		if err := r.Create(ctx, newPod); err != nil {
			fmt.Println("Unable to create new pod", err)
			return err
		}
		fmt.Println("Created Pod")
		podsToCreate--
	}
	return nil
}

func (r *CustomReplicaSetReconciler) deletePods(ctx context.Context, cr *customreplicasetv1.CustomReplicaSet, podsToDelete int, podsPerRevision map[int][]*corev1.Pod, reverse bool) error {
	sortedKeys := sortKeys(podsPerRevision, reverse)
	finishedDeletion := false

	for _, key := range sortedKeys {
		if finishedDeletion {
			return nil
		}
		for _, pod := range podsPerRevision[key] {
			if pod.Status.Phase == corev1.PodRunning || pod.Status.Phase == corev1.PodPending {
				if err := r.Delete(ctx, pod); err != nil {
					fmt.Println("Unable to delete pod", err)
					return err
				}
				fmt.Println("Delete Pod")

				podsToDelete--
				if podsToDelete == 0 {
					finishedDeletion = true
					break
				}
			}
		}
	}

	if podsToDelete > 0 {
		return fmt.Errorf("did not finish deleting all pods required for deletion")
	}

	return nil
}

func (r *CustomReplicaSetReconciler) upgradeOrDowngradePods(ctx context.Context, cr *customreplicasetv1.CustomReplicaSet, podsPerRevision map[int][]*corev1.Pod, latestRevision *v1.ControllerRevision, podsToUpgrade int) error {
	// First check if we even have multiple versions
	revList, err := r.getAllControllerRevisions(ctx, cr, false)
	if err != nil {
		return err
	}
	if len(revList.Items) > 1 {
		if podsToUpgrade > 0 {
			if err := r.deletePods(ctx, cr, podsToUpgrade, podsPerRevision, false); err != nil {
				fmt.Println("Unable to delete old pods", err)
				return err
			}

			// Create new ones
			if err := r.createPods(ctx, cr, podsToUpgrade, latestRevision); err != nil {
				fmt.Println("Failed to create pods", err)
				return err
			}

			fmt.Printf("Upgraded %d Pods\n", podsToUpgrade)
		} else if podsToUpgrade < 0 {
			podsToDowngrade := podsToUpgrade * -1
			if err := r.deletePods(ctx, cr, podsToDowngrade, podsPerRevision, true); err != nil {
				fmt.Println("Unable to delete new pods", err)
				return err
			}

			olderRev, err := r.getControllerRevisionExcludingRevision(ctx, cr, latestRevision.Revision)
			if err != nil {
				fmt.Println("Failed to get a controller revision excluding the latest one")
				return err
			}

			if err := r.createPods(ctx, cr, podsToDowngrade, olderRev); err != nil {
				fmt.Println("Failed to create pods", err)
				return err
			}

			fmt.Printf("Downgraded %d Pods\n", podsToDowngrade)
		}
	}
	return nil
}

func (r *CustomReplicaSetReconciler) newPodForCR(cr *customreplicasetv1.CustomReplicaSet, revision *v1.ControllerRevision) (*corev1.Pod, error) {
	latestRevStr := strconv.Itoa(int(revision.Revision))

	t := time.Now()
	// timestamp := fmt.Sprintf("%d%d%d%d", t.Hour(), t.Minute(), t.Second(), t.Nanosecond())
	timestamp := fmt.Sprintf("%d", t.UnixNano())
	newPodName := fmt.Sprintf("%s-version-%s-%s", cr.Name, latestRevStr, timestamp)

	// Unmarshal the raw JSON into a PodTemplateSpec
	var podTemplateSpec corev1.PodTemplateSpec
	err := json.Unmarshal(revision.Data.Raw, &podTemplateSpec)
	if err != nil {
		return nil, err
	}

	// Create a new Pod object from the template
	newPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      newPodName,
			Namespace: cr.Namespace,
			Labels: map[string]string{
				"owner":    cr.Name,
				"revision": fmt.Sprintf("%d", revision.Revision),
			},
		},
		Spec: podTemplateSpec.Spec, // Assign the template's spec to the pod
	}

	// Set the CustomReplicaSet instance as the owner and controller
	if err := ctrl.SetControllerReference(cr, newPod, r.Scheme); err != nil {
		fmt.Println("Could not set this instance to the customreplicaset controller")
	}
	return newPod, nil
}

// Return map of revision # -> pod reference
func countAvailablePods(pods []corev1.Pod) (map[int][]*corev1.Pod, int, error) {
	podsPerRevision := make(map[int][]*corev1.Pod)
	// make(map[int][]*corev1.Pod)
	totalAvailPods := 0
	for _, pod := range pods {
		// Check if pod exists
		if pod.Status.Phase == corev1.PodRunning || pod.Status.Phase == corev1.PodPending {
			if pod.DeletionTimestamp != nil {
				// Pod is already being deleted, skip to the next one
				continue
			}

			revisionStr, ok := pod.Labels["revision"]
			if !ok {
				continue
			}

			revision, err := strconv.Atoi(revisionStr)
			if err != nil {
				return nil, 0, err
			}
			if podSlice, ok := podsPerRevision[revision]; ok {
				// Revision already exists in map, append to slice
				podSlice = append(podSlice, &pod)
				podsPerRevision[revision] = podSlice
			} else {
				// Revision does not exist in map, create a new slice and add it to map
				podSlice := []*corev1.Pod{&pod}
				podsPerRevision[revision] = podSlice
			}
			totalAvailPods++
		}
	}
	// fmt.Println("Current pods per revision:", podsPerRevision)
	printMap(podsPerRevision)
	// printSortedMap(podsPerRevision)

	return podsPerRevision, totalAvailPods, nil
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

func max(x, y int) int {
	if x > y {
		return x
	}
	return y
}

func min(x, y int) int {
	if x > y {
		return y
	}
	return x
}

// SetupWithManager sets up the controller with the Manager.
func (r *CustomReplicaSetReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&customreplicasetv1.CustomReplicaSet{}).
		Owns(&corev1.Pod{}).
		Complete(r)
}
