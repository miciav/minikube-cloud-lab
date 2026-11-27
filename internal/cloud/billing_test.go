package cloud_test

import (
	"testing"

	"github.com/miciav/minikube-cloud-lab/internal/cloud"
)

func TestMachineTypePrices(t *testing.T) {
	for name, want := range map[string]float64{"lab-small": 0.10, "lab-medium": 0.40} {
		kind, err := cloud.LookupMachineType(name)
		if err != nil || kind.PricePerHour != want {
			t.Fatalf("%s: %+v %v", name, kind, err)
		}
	}
}
