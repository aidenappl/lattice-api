package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aidenappl/lattice-api/structs"
)

func TestExtractBearerToken(t *testing.T) {
	tests := []struct {
		name   string
		header string
		want   string
	}{
		{"valid", "Bearer my-jwt-token", "my-jwt-token"},
		{"empty", "", ""},
		{"basic-auth", "Basic dXNlcjpwYXNz", ""},
		{"no-space", "Bearertoken", ""},
		{"lowercase", "bearer my-token", "my-token"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			if tt.header != "" {
				req.Header.Set("Authorization", tt.header)
			}
			got := extractBearerToken(req)
			if got != tt.want {
				t.Errorf("extractBearerToken(%q) = %q, want %q", tt.header, got, tt.want)
			}
		})
	}
}

func TestGetUserFromContext(t *testing.T) {
	t.Run("with-user", func(t *testing.T) {
		user := &structs.User{ID: 1, Email: "test@example.com", Role: "admin"}
		ctx := context.WithValue(context.Background(), UserContextKey, user)
		got, ok := GetUserFromContext(ctx)
		if !ok {
			t.Fatal("expected ok=true")
		}
		if got.ID != 1 {
			t.Errorf("user ID = %d, want 1", got.ID)
		}
	})

	t.Run("without-user", func(t *testing.T) {
		_, ok := GetUserFromContext(context.Background())
		if ok {
			t.Error("expected ok=false with no user in context")
		}
	})
}

func TestRequireAdmin(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	t.Run("no-user", func(t *testing.T) {
		handler := RequireAdmin(inner)
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d", rr.Code)
		}
	})

	t.Run("viewer-rejected", func(t *testing.T) {
		handler := RequireAdmin(inner)
		user := &structs.User{ID: 1, Role: "viewer"}
		ctx := context.WithValue(context.Background(), UserContextKey, user)
		req := httptest.NewRequest(http.MethodGet, "/test", nil).WithContext(ctx)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Errorf("expected 403, got %d", rr.Code)
		}
	})

	t.Run("editor-rejected", func(t *testing.T) {
		handler := RequireAdmin(inner)
		user := &structs.User{ID: 1, Role: "editor"}
		ctx := context.WithValue(context.Background(), UserContextKey, user)
		req := httptest.NewRequest(http.MethodGet, "/test", nil).WithContext(ctx)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Errorf("expected 403, got %d", rr.Code)
		}
	})

	t.Run("admin-passes", func(t *testing.T) {
		handler := RequireAdmin(inner)
		user := &structs.User{ID: 1, Role: "admin"}
		ctx := context.WithValue(context.Background(), UserContextKey, user)
		req := httptest.NewRequest(http.MethodGet, "/test", nil).WithContext(ctx)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Errorf("expected 200, got %d", rr.Code)
		}
	})
}

func TestRequireEditor(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	t.Run("no-user", func(t *testing.T) {
		handler := RequireEditor(inner)
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d", rr.Code)
		}
	})

	t.Run("viewer-rejected", func(t *testing.T) {
		handler := RequireEditor(inner)
		user := &structs.User{ID: 1, Role: "viewer"}
		ctx := context.WithValue(context.Background(), UserContextKey, user)
		req := httptest.NewRequest(http.MethodGet, "/test", nil).WithContext(ctx)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Errorf("expected 403, got %d", rr.Code)
		}
	})

	t.Run("editor-passes", func(t *testing.T) {
		handler := RequireEditor(inner)
		user := &structs.User{ID: 1, Role: "editor"}
		ctx := context.WithValue(context.Background(), UserContextKey, user)
		req := httptest.NewRequest(http.MethodGet, "/test", nil).WithContext(ctx)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Errorf("expected 200, got %d", rr.Code)
		}
	})

	t.Run("admin-passes", func(t *testing.T) {
		handler := RequireEditor(inner)
		user := &structs.User{ID: 1, Role: "admin"}
		ctx := context.WithValue(context.Background(), UserContextKey, user)
		req := httptest.NewRequest(http.MethodGet, "/test", nil).WithContext(ctx)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Errorf("expected 200, got %d", rr.Code)
		}
	})
}

func TestRejectPending(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	t.Run("pending-blocked", func(t *testing.T) {
		handler := RejectPending(inner)
		user := &structs.User{ID: 1, Role: "pending"}
		ctx := context.WithValue(context.Background(), UserContextKey, user)
		req := httptest.NewRequest(http.MethodGet, "/test", nil).WithContext(ctx)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Errorf("expected 403, got %d", rr.Code)
		}
		var resp map[string]any
		json.NewDecoder(rr.Body).Decode(&resp)
		if code, _ := resp["error_code"].(float64); int(code) != 4004 {
			t.Errorf("error_code = %v, want 4004", resp["error_code"])
		}
	})

	t.Run("viewer-passes", func(t *testing.T) {
		handler := RejectPending(inner)
		user := &structs.User{ID: 1, Role: "viewer"}
		ctx := context.WithValue(context.Background(), UserContextKey, user)
		req := httptest.NewRequest(http.MethodGet, "/test", nil).WithContext(ctx)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Errorf("expected 200, got %d", rr.Code)
		}
	})

	t.Run("no-user-passes", func(t *testing.T) {
		handler := RejectPending(inner)
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Errorf("expected 200 (no user = not pending), got %d", rr.Code)
		}
	})
}
