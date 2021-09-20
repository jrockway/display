package mesonet

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"testing"
	"time"
)

type testTransport struct {
	body []byte
}

func (t *testTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return &http.Response{
		Status:     "200 OK",
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body:       io.NopCloser(bytes.NewReader(t.body)),
	}, nil
}

func TestRequest(t *testing.T) {
	testdata, err := os.ReadFile("testdata/brooklyn.json")
	if err != nil {
		t.Fatal(err)
	}
	tr := new(testTransport)
	tr.body = testdata
	client := &http.Client{
		Transport: tr,
	}
	res, err := Do(context.Background(), client, &Request{})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := res.Success, true; got != want {
		t.Errorf("success:\n  got: %v\n want: %v", got, want)
	}
	// Some minor sanity checks on the data parsing.
	if got, want := res.Response.Coords["time"].TimeData[0], time.Date(2021, 9, 19, 2, 30, 0, 0, time.UTC); got != want {
		t.Errorf("time:\n  got: %v\n want: %v", got, want)
	}
	if got, want := res.Response.DataVars["tair"].FloatData[0], 78.4; got != want {
		t.Errorf("tair (first):\n  got: %v\n want: %v", got, want)
	}
	if got, want := res.Response.DataVars["tair"].FloatData[287], 68.8; got != want {
		t.Errorf("tair (last):\n  got: %v\n want: %v", got, want)
	}
}
