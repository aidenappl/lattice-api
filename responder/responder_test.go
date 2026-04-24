package responder

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNew(t *testing.T) {
	rr := httptest.NewRecorder()
	New(rr, map[string]string{"key": "value"})

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var resp ResponseStructure
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if !resp.Success {
		t.Error("success = false, want true")
	}
	if resp.Message != "request was successful" {
		t.Errorf("message = %q, want %q", resp.Message, "request was successful")
	}
	if resp.Pagination != nil {
		t.Error("pagination should be nil")
	}
}

func TestNewWithCustomMessage(t *testing.T) {
	rr := httptest.NewRecorder()
	New(rr, nil, "Created Successfully")

	var resp ResponseStructure
	json.NewDecoder(rr.Body).Decode(&resp)
	if resp.Message != "created successfully" {
		t.Errorf("message = %q, want %q (lowercased)", resp.Message, "created successfully")
	}
}

func TestNewCreated(t *testing.T) {
	rr := httptest.NewRecorder()
	NewCreated(rr, map[string]int{"id": 1})

	if rr.Code != http.StatusCreated {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusCreated)
	}

	var resp ResponseStructure
	json.NewDecoder(rr.Body).Decode(&resp)
	if !resp.Success {
		t.Error("success = false, want true")
	}
}

func TestNewWithCount(t *testing.T) {
	rr := httptest.NewRecorder()
	NewWithCount(rr, []string{"a", "b"}, 10, "/next", "/prev")

	var resp ResponseStructure
	json.NewDecoder(rr.Body).Decode(&resp)
	if !resp.Success {
		t.Error("success = false, want true")
	}
	if resp.Pagination == nil {
		t.Fatal("pagination should not be nil")
	}
	if resp.Pagination.Count != 10 {
		t.Errorf("pagination.count = %d, want 10", resp.Pagination.Count)
	}
	if resp.Pagination.Next != "/next" {
		t.Errorf("pagination.next = %q, want %q", resp.Pagination.Next, "/next")
	}
	if resp.Pagination.Previous != "/prev" {
		t.Errorf("pagination.previous = %q, want %q", resp.Pagination.Previous, "/prev")
	}
}

func TestSendError(t *testing.T) {
	rr := httptest.NewRecorder()
	SendError(rr, http.StatusBadRequest, "invalid input")

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}

	var resp ErrorResponse
	json.NewDecoder(rr.Body).Decode(&resp)
	if resp.Success {
		t.Error("success = true, want false")
	}
	if resp.ErrorMessage != "invalid input" {
		t.Errorf("error_message = %q, want %q", resp.ErrorMessage, "invalid input")
	}
}

func TestSendErrorWithCode(t *testing.T) {
	rr := httptest.NewRecorder()
	SendErrorWithCode(rr, http.StatusForbidden, "access denied", 4003)

	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusForbidden)
	}

	var resp ErrorResponse
	json.NewDecoder(rr.Body).Decode(&resp)
	if resp.ErrorCode != 4003 {
		t.Errorf("error_code = %d, want 4003", resp.ErrorCode)
	}
}

func TestSendError5xxHidesInternalError(t *testing.T) {
	rr := httptest.NewRecorder()
	SendError(rr, http.StatusInternalServerError, "something broke", fmt.Errorf("sql: connection refused"))

	var resp ErrorResponse
	json.NewDecoder(rr.Body).Decode(&resp)
	if resp.Error != "internal server error" {
		t.Errorf("error = %v, want %q (should hide internal detail)", resp.Error, "internal server error")
	}
}

func TestSendError4xxExposesError(t *testing.T) {
	rr := httptest.NewRecorder()
	SendError(rr, http.StatusBadRequest, "validation failed", fmt.Errorf("name is required"))

	var resp ErrorResponse
	json.NewDecoder(rr.Body).Decode(&resp)
	if resp.Error != "name is required" {
		t.Errorf("error = %v, want %q (4xx should expose error)", resp.Error, "name is required")
	}
}

func TestBadBody(t *testing.T) {
	rr := httptest.NewRecorder()
	BadBody(rr, fmt.Errorf("json: cannot unmarshal"))

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
}

func TestBadBodyNilError(t *testing.T) {
	rr := httptest.NewRecorder()
	BadBody(rr, nil)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
}

func TestMissingBodyFields(t *testing.T) {
	rr := httptest.NewRecorder()
	MissingBodyFields(rr, "name, email")

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
}

func TestNotFound(t *testing.T) {
	rr := httptest.NewRecorder()
	NotFound(rr)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusNotFound)
	}
}

func TestQueryError(t *testing.T) {
	rr := httptest.NewRecorder()
	QueryError(rr, fmt.Errorf("sql: no rows"), "failed to get worker")

	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusInternalServerError)
	}
}
