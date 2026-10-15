package query

import (
	"testing"
	"time"

	"github.com/aidenappl/lattice-api/structs"
)

func TestCurrentContainerSamples(t *testing.T) {
	now := time.Date(2026, 10, 14, 22, 13, 14, 0, time.UTC)
	sample := func(name string, ago time.Duration) structs.ContainerMetrics {
		return structs.ContainerMetrics{ContainerName: name, RecordedAt: now.Add(-ago)}
	}
	rows := []structs.ContainerMetrics{
		sample("amex-atlas-api", 0),
		sample("amex-atlas-api-lattice-updating", 18*time.Hour),
		sample("amex-atlas-web", 20*time.Second),
		sample("johnnies-api", 4*time.Minute),
	}

	got := currentContainerSamples(rows, 5*time.Minute)
	if len(got) != 3 {
		t.Fatalf("got %d containers, want 3: %+v", len(got), got)
	}
	for _, r := range got {
		if r.ContainerName == "amex-atlas-api-lattice-updating" {
			t.Fatal("a container gone for 18h was kept")
		}
	}

	t.Run("a worker that stopped reporting keeps its last state", func(t *testing.T) {
		old := []structs.ContainerMetrics{sample("a", 48*time.Hour), sample("b", 48*time.Hour)}
		if got := currentContainerSamples(old, 5*time.Minute); len(got) != 2 {
			t.Fatalf("got %d, want 2", len(got))
		}
	})

	t.Run("empty", func(t *testing.T) {
		if got := currentContainerSamples(nil, time.Minute); len(got) != 0 {
			t.Fatalf("got %d", len(got))
		}
	})
}
