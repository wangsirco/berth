package errs

import (
	"errors"
	"net/http"
	"testing"
)

func TestNewAndAs(t *testing.T) {
	err := error(New(http.StatusConflict, "container.not_running", "name", "n8n"))
	var e *E
	if !errors.As(err, &e) {
		t.Fatal("not *E")
	}
	if e.Status != 409 || e.Code != "container.not_running" || e.Params["name"] != "n8n" {
		t.Fatalf("got %+v", e)
	}
	if e.Error() != "container.not_running" {
		t.Fatalf("Error()=%q", e.Error())
	}
}

func TestWrapUnknown(t *testing.T) {
	e := From(errors.New("boom"))
	if e.Status != 500 || e.Code != "internal" || e.Params["detail"] != "boom" {
		t.Fatalf("got %+v", e)
	}
}
