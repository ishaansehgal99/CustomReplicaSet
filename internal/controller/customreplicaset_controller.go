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
<<<<<<< HEAD
	_, err = r.manageControllerRevisionHistory(ctx, &crs)
=======
	latestRevision, err := r.manageControllerRevisionHistory(ctx, &crs)
>>>>>>> d29a12a (Completed adding and removing pods appropriately)
	if err != nil {
		return ctrl.Result{Requeue: true}, err
	}

	podsPerRevision, totalAvailPods, err := countAvailablePods(childPods.Items)
	if err != nil {
		return ctrl.Result{Requeue: true}, err
	}

	if err := r.managePods(ctx, &crs, childPods, podsPerRevision, totalAvailPods, latestRevision); err != nil {
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

func (r *CustomReplicaSetReconciler) manageControllerRevisionHistory(ctx context.Context, cr *customreplicasetv1.CustomReplicaSet) (*v1.ControllerRevision, error) {
	jsonSpec, err := convertCRSpecToJson(cr.Spec)
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

func (r *CustomReplicaSetReconciler) managePods(ctx context.Context, crs *customreplicasetv1.CustomReplicaSet, childPods corev1.PodList, podsPerRevision map[int][]*corev1.Pod, totalAvailPods int, latestRevision *v1.ControllerRevision) error {
	if totalAvailPods < int(crs.Spec.Replicas) {
		if err := r.createPods(ctx, crs, totalAvailPods, podsPerRevision, latestRevision); err != nil {
			fmt.Println("Failed to create pods", err)
			return err
		}
	} else if totalAvailPods > int(crs.Spec.Replicas) {
		if err := r.deletePods(ctx, crs, totalAvailPods, podsPerRevision, latestRevision, childPods); err != nil {
			fmt.Println("Failed to delete pods", err)
			return err
		}
	}
	// } else {
	// 	if err := r.upgradeOrDowngradePods(ctx, crs, totalAvailPods, podsPerRevision, latestRevision, childPods); err != nil {
	// 		fmt.Println("Failed to upgrade or downgrade pods", err)
	// 		return err
	// 	}
	// }
	return nil
}

func (r *CustomReplicaSetReconciler) createPods(ctx context.Context, cr *customreplicasetv1.CustomReplicaSet, totalPods int, podsPerRevision map[int][]*corev1.Pod, latestRevision *v1.ControllerRevision) error {
	for totalPods < int(cr.Spec.Replicas) {
		// If we are short pods, create new pods of latest revision
		newPod, err := r.newPodForCR(cr, latestRevision)
		if err != nil {
			fmt.Println("Unable to initialize new pod for creation", err)
			return err
		}

		if err := r.Create(ctx, newPod); err != nil {
			fmt.Println("Unable to create new pod", err)
			return err
		}
		fmt.Println("Created Pod")
		totalPods++
	}
	return nil
}

func (r *CustomReplicaSetReconciler) deletePods(ctx context.Context, cr *customreplicasetv1.CustomReplicaSet, totalPods int, podsPerRevision map[int][]*corev1.Pod, latestRevision *v1.ControllerRevision, childPods corev1.PodList) error {
	podsToDelete := totalPods - int(cr.Spec.Replicas)
	sortedKeys := sortMap(podsPerRevision)
	finishedDeletion := false

	for _, key := range sortedKeys {
		if finishedDeletion {
			return nil
		}
		for _, pod := range podsPerRevision[key] {
			if pod.Status.Phase == corev1.PodRunning || pod.Status.Phase == corev1.PodPending {
				if pod.DeletionTimestamp != nil {
					// Pod is already being deleted, skip to the next one
					podsToDelete--
					if podsToDelete == 0 {
						finishedDeletion = true
						break
					}
					continue
				}
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

// func (r *CustomReplicaSetReconciler) upgradeOrDowngradePods(ctx context.Context, cr *customreplicasetv1.CustomReplicaSet, totalPods int, podsPerRevision *sortedmap.SortedMap, latestRevision *v1.ControllerRevision, childPods corev1.PodList) error {
// 	// Check if we need to upgrade or downgrade any pods
// 	keys := podsPerRevision.Keys()
// 	latestPods, ok := podsPerRevision.Get(keys[len(keys)-1])
// 	if !ok {
// 		fmt.Println("Could not get pods of latest revision")
// 		return fmt.Errorf("Could not get pods of latest revision")
// 	}
// 	podsToUpgrade := int(cr.Spec.Partition) - len(latestPods)
// 	if podsToUpgrade > 0 {
// 		if err := r.upgradePods(ctx, podsToUpgrade, cr, childPods); err != nil {
// 			fmt.Println("Unable to upgrade pod", err)
// 			return err
// 		}

// 	} else if podsToUpgrade < 0 {
// 		if err := r.downgradePods(ctx, podsToUpgrade, cr, childPods); err != nil {
// 			fmt.Println("Unable to downgrade pod", err)
// 			return err
// 		}
// 	}
// 	return nil
// }

// func (r *CustomReplicaSetReconciler) upgradePods(ctx context.Context, podsToUpgrade int, cr *customreplicasetv1.CustomReplicaSet, childPods corev1.PodList) error {
// 	for _, pod := range childPods.Items {
// 		if pod.Status.Phase == corev1.PodRunning || pod.Status.Phase == corev1.PodPending {
// 			// Upgrade a standard pod
// 			if !strings.Contains(pod.ObjectMeta.Name, "upgraded") {
// 				if err := r.Delete(ctx, &pod); err != nil {
// 					fmt.Println("Unable to delete pod", err)
// 					return err
// 				}

// 				newPod := r.newPodForCR(cr, true)
// 				if err := r.Create(ctx, newPod); err != nil {
// 					fmt.Println("Unable to create new pod", err)
// 					return err
// 				}

// 				fmt.Println("Upgraded Pod")

// 				podsToUpgrade--
// 				if podsToUpgrade == 0 {
// 					break
// 				}
// 			}
// 		}
// 	}
// 	return nil
// }

// func (r *CustomReplicaSetReconciler) downgradePods(ctx context.Context, podsToUpgrade int, cr *customreplicasetv1.CustomReplicaSet, childPods corev1.PodList, log logr.Logger) error {
// 	podsToDowngrade := podsToUpgrade * -1

// 	for _, pod := range childPods.Items {
// 		if pod.Status.Phase == corev1.PodRunning || pod.Status.Phase == corev1.PodPending {
// 			// Downgrade an upgraded pod to std
// 			if strings.Contains(pod.ObjectMeta.Name, "upgraded") {
// 				if err := r.Delete(ctx, &pod); err != nil {
// 					log.Error(err, "Unable to delete pod")
// 					return err
// 				}

// 				newPod := r.newPodForCR(cr, false)
// 				if err := r.Create(ctx, newPod); err != nil {
// 					log.Error(err, "Unable to create new pod")
// 					return err
// 				}

// 				fmt.Println("Downgraded Pod")

// 				podsToDowngrade--
// 				if podsToDowngrade == 0 {
// 					break
// 				}
// 			}
// 		}
// 	}
// 	return nil
// }

func (r *CustomReplicaSetReconciler) newPodForCR(cr *customreplicasetv1.CustomReplicaSet, latestRevision *v1.ControllerRevision) (*corev1.Pod, error) {
	latestRevStr := strconv.Itoa(int(latestRevision.Revision))

	t := time.Now()
	// timestamp := fmt.Sprintf("%d%d%d%d", t.Hour(), t.Minute(), t.Second(), t.Nanosecond())
	timestamp := fmt.Sprintf("%d", t.UnixNano())
	newPodName := fmt.Sprintf("%s-version-%s-%s", cr.Name, latestRevStr, timestamp)

	// Unmarshal the raw JSON into a PodTemplateSpec
	var crsSpec customreplicasetv1.CustomReplicaSetSpec
	err := json.Unmarshal(latestRevision.Data.Raw, &crsSpec)
	if err != nil {
		return nil, err
	}

	// Assign the revision to the template's labels
	crsSpec.Template.Metadata.Labels["revision"] = fmt.Sprintf("%d", latestRevision.Revision)
	crsSpec.Template.Metadata.Labels["owner"] = cr.Name

	// Create a new Pod object from the template
	newPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      newPodName,
			Namespace: cr.Namespace,
			Labels:    crsSpec.Template.Metadata.Labels, // Copy labels from the template to the pod
		},
		Spec: crsSpec.Template.Spec, // Assign the template's spec to the pod
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
			revision, err := strconv.Atoi(pod.Labels["revision"])
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

// SetupWithManager sets up the controller with the Manager.
func (r *CustomReplicaSetReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&customreplicasetv1.CustomReplicaSet{}).
		Owns(&corev1.Pod{}).
		Complete(r)
}
