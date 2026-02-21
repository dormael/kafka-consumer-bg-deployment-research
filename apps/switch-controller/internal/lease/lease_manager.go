package lease

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/ptr"
)

const (
	// DefaultLeaseDuration is the default lease duration.
	DefaultLeaseDuration = 30 * time.Second
	// DefaultRenewInterval is the default lease renewal interval.
	DefaultRenewInterval = 10 * time.Second
)

// LeaseManager manages a Kubernetes Lease for mutual exclusion during
// blue/green switch operations.
type LeaseManager struct {
	client    kubernetes.Interface
	namespace string
	leaseName string
	logger    *slog.Logger
}

// NewLeaseManager creates a new LeaseManager.
func NewLeaseManager(client kubernetes.Interface, namespace, leaseName string, logger *slog.Logger) *LeaseManager {
	return &LeaseManager{
		client:    client,
		namespace: namespace,
		leaseName: leaseName,
		logger:    logger.With("component", "lease-manager", "lease", leaseName),
	}
}

// AcquireLease attempts to acquire or update the lease for the given holder.
// It creates the lease if it does not exist. If the lease is held by another
// holder and has not expired, it returns an error.
func (lm *LeaseManager) AcquireLease(ctx context.Context, holder string) error {
	lm.logger.Info("acquiring lease", "holder", holder)

	now := metav1.NewMicroTime(time.Now())
	leaseDurationSec := int32(DefaultLeaseDuration / time.Second)

	existing, err := lm.client.CoordinationV1().Leases(lm.namespace).Get(ctx, lm.leaseName, metav1.GetOptions{})
	if err != nil {
		// Lease does not exist, create it.
		lease := &coordinationv1.Lease{
			ObjectMeta: metav1.ObjectMeta{
				Name:      lm.leaseName,
				Namespace: lm.namespace,
			},
			Spec: coordinationv1.LeaseSpec{
				HolderIdentity:       ptr.To(holder),
				LeaseDurationSeconds: ptr.To(leaseDurationSec),
				AcquireTime:          &now,
				RenewTime:            &now,
			},
		}
		_, createErr := lm.client.CoordinationV1().Leases(lm.namespace).Create(ctx, lease, metav1.CreateOptions{})
		if createErr != nil {
			return fmt.Errorf("failed to create lease %s: %w", lm.leaseName, createErr)
		}
		lm.logger.Info("lease created and acquired", "holder", holder)
		return nil
	}

	// Lease exists. Check if it is expired or held by the same holder.
	if existing.Spec.HolderIdentity != nil && *existing.Spec.HolderIdentity != holder {
		// Check if the existing lease has expired.
		if existing.Spec.RenewTime != nil && existing.Spec.LeaseDurationSeconds != nil {
			expiry := existing.Spec.RenewTime.Time.Add(time.Duration(*existing.Spec.LeaseDurationSeconds) * time.Second)
			if time.Now().Before(expiry) {
				return fmt.Errorf("lease %s is held by %s (expires at %s)", lm.leaseName, *existing.Spec.HolderIdentity, expiry.Format(time.RFC3339))
			}
			lm.logger.Warn("lease expired, taking over", "previous_holder", *existing.Spec.HolderIdentity)
		}
	}

	// Update the lease.
	existing.Spec.HolderIdentity = ptr.To(holder)
	existing.Spec.LeaseDurationSeconds = ptr.To(leaseDurationSec)
	existing.Spec.AcquireTime = &now
	existing.Spec.RenewTime = &now

	_, updateErr := lm.client.CoordinationV1().Leases(lm.namespace).Update(ctx, existing, metav1.UpdateOptions{})
	if updateErr != nil {
		return fmt.Errorf("failed to update lease %s: %w", lm.leaseName, updateErr)
	}

	lm.logger.Info("lease acquired", "holder", holder)
	return nil
}

// RenewLease renews the lease by updating the renew time.
func (lm *LeaseManager) RenewLease(ctx context.Context) error {
	existing, err := lm.client.CoordinationV1().Leases(lm.namespace).Get(ctx, lm.leaseName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get lease %s for renewal: %w", lm.leaseName, err)
	}

	now := metav1.NewMicroTime(time.Now())
	existing.Spec.RenewTime = &now

	_, updateErr := lm.client.CoordinationV1().Leases(lm.namespace).Update(ctx, existing, metav1.UpdateOptions{})
	if updateErr != nil {
		return fmt.Errorf("failed to renew lease %s: %w", lm.leaseName, updateErr)
	}

	lm.logger.Debug("lease renewed")
	return nil
}

// ReleaseLease releases the lease by clearing the holder identity.
func (lm *LeaseManager) ReleaseLease(ctx context.Context) error {
	lm.logger.Info("releasing lease")

	existing, err := lm.client.CoordinationV1().Leases(lm.namespace).Get(ctx, lm.leaseName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get lease %s for release: %w", lm.leaseName, err)
	}

	existing.Spec.HolderIdentity = ptr.To("")

	_, updateErr := lm.client.CoordinationV1().Leases(lm.namespace).Update(ctx, existing, metav1.UpdateOptions{})
	if updateErr != nil {
		return fmt.Errorf("failed to release lease %s: %w", lm.leaseName, updateErr)
	}

	lm.logger.Info("lease released")
	return nil
}

// GetHolder returns the current holder of the lease.
func (lm *LeaseManager) GetHolder(ctx context.Context) (string, error) {
	existing, err := lm.client.CoordinationV1().Leases(lm.namespace).Get(ctx, lm.leaseName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to get lease %s: %w", lm.leaseName, err)
	}

	if existing.Spec.HolderIdentity == nil {
		return "", nil
	}
	return *existing.Spec.HolderIdentity, nil
}
