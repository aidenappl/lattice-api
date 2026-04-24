package healthscan

import "testing"

func TestParseContainerNames(t *testing.T) {
	t.Run("valid-payload", func(t *testing.T) {
		payload := map[string]any{
			"container_stats": []any{
				map[string]any{"name": "nginx"},
				map[string]any{"name": "redis"},
				map[string]any{"other": "no-name"},
			},
		}
		names := ParseContainerNames(payload)
		if len(names) != 2 {
			t.Fatalf("got %d names, want 2", len(names))
		}
		if names[0] != "nginx" || names[1] != "redis" {
			t.Errorf("names = %v, want [nginx redis]", names)
		}
	})

	t.Run("missing-key", func(t *testing.T) {
		payload := map[string]any{"other": "data"}
		names := ParseContainerNames(payload)
		if names != nil {
			t.Errorf("expected nil, got %v", names)
		}
	})

	t.Run("wrong-type", func(t *testing.T) {
		payload := map[string]any{"container_stats": "not-a-slice"}
		names := ParseContainerNames(payload)
		if names != nil {
			t.Errorf("expected nil, got %v", names)
		}
	})

	t.Run("empty-slice", func(t *testing.T) {
		payload := map[string]any{"container_stats": []any{}}
		names := ParseContainerNames(payload)
		if len(names) != 0 {
			t.Errorf("expected empty, got %v", names)
		}
	})
}

func TestParseContainerNameFromSync(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		payload := map[string]any{"container_name": "myapp"}
		name := ParseContainerNameFromSync(payload)
		if name != "myapp" {
			t.Errorf("name = %q, want %q", name, "myapp")
		}
	})

	t.Run("missing", func(t *testing.T) {
		payload := map[string]any{"other": "data"}
		name := ParseContainerNameFromSync(payload)
		if name != "" {
			t.Errorf("name = %q, want empty", name)
		}
	})

	t.Run("wrong-type", func(t *testing.T) {
		payload := map[string]any{"container_name": 123}
		name := ParseContainerNameFromSync(payload)
		if name != "" {
			t.Errorf("name = %q, want empty", name)
		}
	})
}
