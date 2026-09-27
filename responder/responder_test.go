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

func TestUnencodableResponseIsARecorded500(t *testing.T) {
	rr := httptest.NewRecorder()
	New(rr, make(chan int))

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
	var resp ErrorResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil || resp.Success || resp.ErrorMessage != "failed to encode response" {
		t.Errorf("body = %+v (decode err %v); want the JSON error shape", resp, err)
	}
}

func TestSendUpstreamErrorKeepsTheReasonForTheClient(t *testing.T) {
	rr := httptest.NewRecorder()
	SendUpstreamError(rr, http.StatusBadGateway, "Registry connection failed", fmt.Errorf("401 unauthorized"))

	var resp ErrorResponse
	json.NewDecoder(rr.Body).Decode(&resp)
	if rr.Code != http.StatusBadGateway || resp.ErrorMessage != "registry connection failed: 401 unauthorized" || resp.Error != "401 unauthorized" {
		t.Errorf("status=%d body=%+v", rr.Code, resp)
	}
}

// failingWriter accepts the header, then fails every body write, like a client
// that disconnected mid-response.
type failingWriter struct {
	*httptest.ResponseRecorder
	headerCalls int
	failures    int
}

func (f *failingWriter) WriteHeader(code int) {
	f.headerCalls++
	f.ResponseRecorder.WriteHeader(code)
}

func (f *failingWriter) Write([]byte) (int, error) { return 0, fmt.Errorf("write: broken pipe") }

func (f *failingWriter) RecordFailure(string, error, int) { f.failures++ }

func TestWriteFailureAfterHeaderIsNotRecordedAsA500(t *testing.T) {
	tests := []struct {
		name   string
		send   func(http.ResponseWriter)
		status int
	}{
		{"New", func(w http.ResponseWriter) { New(w, map[string]string{"k": "v"}) }, http.StatusOK},
		{"NewCreated", func(w http.ResponseWriter) { NewCreated(w, map[string]string{"k": "v"}) }, http.StatusCreated},
		{"NewWithCount", func(w http.ResponseWriter) { NewWithCount(w, []int{1}, 1, "", "") }, http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fw := &failingWriter{ResponseRecorder: httptest.NewRecorder()}
			tt.send(fw)
			if fw.failures != 0 {
				t.Errorf("recorded %d failures; a client disconnect is not a server error", fw.failures)
			}
			if fw.headerCalls != 1 || fw.Code != tt.status {
				t.Errorf("WriteHeader called %d times, status %d; want once with %d", fw.headerCalls, fw.Code, tt.status)
			}
		})
	}
}

func TestEncodeFailureIsAClean500(t *testing.T) {
	fw := &failingWriter{ResponseRecorder: httptest.NewRecorder()}
	New(fw, map[string]any{"bad": make(chan int)})
	if fw.Code != http.StatusInternalServerError || fw.failures != 1 || fw.headerCalls != 1 {
		t.Errorf("status=%d failures=%d headerCalls=%d; want one recorded 500", fw.Code, fw.failures, fw.headerCalls)
	}
}
