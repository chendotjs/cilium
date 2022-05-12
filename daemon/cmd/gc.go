package cmd

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/cilium/cilium/operator/identity"
	v2 "github.com/cilium/cilium/pkg/k8s/apis/cilium.io/v2"
	ciliumcs "github.com/cilium/cilium/pkg/k8s/client/clientset/versioned"
	"github.com/sirupsen/logrus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/apimachinery/pkg/util/wait"
	k8scs "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
)

const logFieldCilium = "cilium"
const logFieldIdentity = "identity"

type Cilium struct {
	Client       k8scs.Interface
	CiliumClient ciliumcs.Interface
	GCInterval   time.Duration
}

// NewCiliumService NewCiliumService
func NewCiliumService(client k8scs.Interface, ciliumClient ciliumcs.Interface) *Cilium {
	return &Cilium{
		Client:       client,
		CiliumClient: ciliumClient,
		GCInterval:   30 * time.Minute,
	}
}

// Run Run
func (c *Cilium) Run() {
	daemonNamespace := os.Getenv("POD_NAMESPACE")
	if len(daemonNamespace) == 0 {
		daemonNamespace = "kube-system"
	}
	id := fmt.Sprintf("%s_%s_%s", daemonNamespace, os.Getenv("NODENAME"), uuid.NewUUID())

	lock := &resourcelock.LeaseLock{
		LeaseMeta: metav1.ObjectMeta{
			Name:      "terway-cilium-lock",
			Namespace: daemonNamespace,
		},
		Client: c.Client.CoordinationV1(),
		LockConfig: resourcelock.ResourceLockConfig{
			Identity: id,
		},
	}
	// start the leader election code loop
	go func() {
		for {
			leaderelection.RunOrDie(context.TODO(), leaderelection.LeaderElectionConfig{
				Lock:            lock,
				ReleaseOnCancel: true,
				LeaseDuration:   170 * time.Second,
				RenewDeadline:   80 * time.Second,
				RetryPeriod:     60 * time.Second,
				Callbacks: leaderelection.LeaderCallbacks{
					OnStartedLeading: func(ctx context.Context) {
						c.GC(ctx)
					},
					OnStoppedLeading: func() {
						logrus.Infof("leader lost")
					},
					OnNewLeader: func(identity string) {
						if identity == id {
							// I just got the lock
							return
						}
						logrus.Infof("new leader elected: %s", identity)
					},
				},
			})
			time.Sleep(10. * time.Second)
		}
	}()
}

func (c *Cilium) GC(ctx context.Context) {
	identityHeartbeat := identity.NewIdentityHeartbeatStore(2 * c.GCInterval)

	wait.JitterUntil(func() {
		identities, err := c.CiliumClient.CiliumV2().CiliumIdentities().List(context.TODO(), v1.ListOptions{ResourceVersion: "0", TimeoutSeconds: func(t int64) *int64 { return &t }(60)})
		if err != nil {
			logrus.WithError(err).Error("Unable to list cilium identities")
			return
		}
		eps, err := c.CiliumClient.CiliumV2().CiliumEndpoints("").List(context.TODO(), v1.ListOptions{ResourceVersion: "0", TimeoutSeconds: func(t int64) *int64 { return &t }(60)})
		if err != nil {
			logrus.WithError(err).Error("Unable to list cilium endpoints")
			return
		}

		timeNow := time.Now()
		for _, ciliumIdentity := range identities.Items {
			for _, ep := range eps.Items {
				if ep.Status.Identity != nil && fmt.Sprintf("%d", ep.Status.Identity.ID) == ciliumIdentity.Name {
					// If the ciliumIdentity is alive then mark it as alive
					identityHeartbeat.MarkAlive(ciliumIdentity.Name, timeNow)
					logrus.WithFields(logrus.Fields{
						logFieldIdentity: ciliumIdentity.Name,
					}).Debugf("Mark identity in use %s", ciliumIdentity.Name)
					continue
				}
			}

			if !identityHeartbeat.IsAlive(ciliumIdentity.Name) {
				logrus.WithFields(logrus.Fields{
					logFieldIdentity: ciliumIdentity.Name,
				}).Debug("Deleting unused identity")
				if err := c.deleteIdentity(ctx, &ciliumIdentity); err != nil {
					logrus.WithError(err).WithFields(logrus.Fields{
						logFieldIdentity: ciliumIdentity.Name,
					}).Error("Deleting unused identity")
					// If Context was canceled we should break
					if ctx.Err() != nil {
						break
					}
				}
			}
		}

		identityHeartbeat.GC()
	}, c.GCInterval, 1.1, false, ctx.Done())
	logrus.WithField(logFieldCilium, "cilium").Debugf("GC loop end")
}

// deleteIdentity deletes an identity. It includes the resource version and
// will error if the object has since been changed.
func (c *Cilium) deleteIdentity(ctx context.Context, identity *v2.CiliumIdentity) error {
	err := c.CiliumClient.CiliumV2().CiliumIdentities().Delete(
		ctx,
		identity.Name,
		metav1.DeleteOptions{
			Preconditions: &metav1.Preconditions{
				UID:             &identity.UID,
				ResourceVersion: &identity.ResourceVersion,
			},
		})
	if err != nil {
		logrus.WithError(err).Error("Unable to delete identity")
	} else {
		logrus.WithField(logFieldIdentity, identity.GetName()).Info("Garbage collected identity")
	}

	return err
}
