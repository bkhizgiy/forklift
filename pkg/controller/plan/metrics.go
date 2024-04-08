package plan

import (
	"context"
	"time"

	api "github.com/konveyor/forklift-controller/pkg/apis/forklift/v1beta1"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	// 'status' - [ idle, executing, succeeded, failed, canceled, deleted, paused, pending, running, blocked ]
	planGauge = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "mtv_workload_plans",
		Help: "VM migration Plans sorted by status and provider type",
	},
		[]string{"status", "provider"},
	)
)

// Calculate Plans metrics every 10 seconds
func recordMetrics(client client.Client) {
	go func() {
		for {
			time.Sleep(10 * time.Second)

			// get all migration objects
			plans := api.PlanList{}
			err := client.List(context.TODO(), &plans)

			// if error occurs, retry 10 seconds later
			if err != nil {
				log.Info("Metrics Plans list error: %v", err)
				continue
			}

			// Holding counter vars used to make gauge update "atomic"
			var succeededRHV, succeededOCP, succeededOVA, succeededVsphere, succeededOpenstack float64
			var failedRHV, failedOCP, failedOVA, failedVsphere, failedOpenstack float64

			for _, m := range plans.Items {
				if m.Status.HasCondition(Succeeded) {
					switch m.Provider.Source.Type() {
					case api.Ova:
						succeededOVA++
						continue
					case api.OVirt:
						succeededRHV++
						continue
					case api.VSphere:
						succeededVsphere++
						continue
					case api.OpenShift:
						succeededOCP++
						continue
					case api.OpenStack:
						succeededOpenstack++
						continue
					}
				}
				if m.Status.HasCondition(Failed) {
					switch m.Provider.Source.Type() {
					case api.Ova:
						failedOVA++
						continue
					case api.OVirt:
						failedRHV++
						continue
					case api.VSphere:
						failedVsphere++
						continue
					case api.OpenShift:
						failedOCP++
						continue
					case api.OpenStack:
						failedOpenstack++
						continue
					}
				}
			}
			planGauge.With(
				prometheus.Labels{"status": Succeeded, "provider": "oVirt"}).Set(succeededRHV)
			planGauge.With(
				prometheus.Labels{"status": Succeeded, "provider": "OpenShift"}).Set(succeededOCP)
			planGauge.With(
				prometheus.Labels{"status": Succeeded, "provider": "OpenStack"}).Set(succeededOpenstack)
			planGauge.With(
				prometheus.Labels{"status": Succeeded, "provider": "OVA"}).Set(succeededOVA)
			planGauge.With(
				prometheus.Labels{"status": Succeeded, "provider": "vSphere"}).Set(succeededVsphere)
			planGauge.With(
				prometheus.Labels{"status": Failed, "provider": "oVirt"}).Set(failedRHV)
			planGauge.With(
				prometheus.Labels{"status": Failed, "provider": "OpenShift"}).Set(failedOCP)
			planGauge.With(
				prometheus.Labels{"status": Failed, "provider": "OpenStack"}).Set(succeededOpenstack)
			planGauge.With(
				prometheus.Labels{"status": Failed, "provider": "OVA"}).Set(succeededOVA)
			planGauge.With(
				prometheus.Labels{"status": Failed, "provider": "vSphere"}).Set(succeededVsphere)
		}
	}()
}
