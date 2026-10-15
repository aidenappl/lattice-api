package routers

import "testing"

func TestComposeNetworkRequests(t *testing.T) {
	yml := `
services:
  api:
    image: x
networks:
  keyring-network: {}
  backend:
    driver: overlay
    name: shared-backend
`
	got, err := composeNetworkRequests(7, yml)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d networks, want 2: %+v", len(got), got)
	}
	if got[0].Name != "keyring-network" || got[0].Driver != "bridge" || got[0].StackID != 7 {
		t.Errorf("default network wrong: %+v", got[0])
	}
	if got[1].Name != "shared-backend" || got[1].Driver != "overlay" {
		t.Errorf("named network wrong: %+v", got[1])
	}

	if got, err := composeNetworkRequests(7, ""); err != nil || len(got) != 0 {
		t.Errorf("empty compose: got %v, %v", got, err)
	}
	if _, err := composeNetworkRequests(7, "services: [unclosed"); err == nil {
		t.Error("expected an error for invalid yaml")
	}
}
