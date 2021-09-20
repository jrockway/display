// Package mesonet retrieves weather from the New York State Mesonet.
//
// See http://www.nysmesonet.org/about/data to determine whether or not you're allowed to use this
// library.
package mesonet

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

type Variable struct {
	ID    string `json:"id"`
	Units string `json:"units"`
}

type Request struct {
	Endpoint  string     `json:"-"`
	Dataset   string     `json:"dataset"`
	Start     time.Time  `json:"start"`
	End       time.Time  `json:"end"`
	Stations  []string   `json:"stations"`
	Variables []Variable `json:"variables"`
}

type Result struct {
	Response Response `json:"response"`
	Success  bool     `json:"success"`
}

type Response struct {
	Attrs      Attr               `json:"attrs"`
	Coords     map[string]DataVar `json:"coords"`
	Dimensions map[string]int     `json:"dims"`
	DataVars   map[string]DataVar `json:"data_vars"`
}

type Attr struct {
	LongName    string  `json:"long_name"`
	ScaleFactor float64 `json:"scale_factor"`
	Units       string  `json:"units"`
}

type RawDataVar struct {
	Attrs      Attr          `json:"attrs"`
	Data       []interface{} `json:"data"`
	Dimensions []string      `json:"dims"`
}

type DataVar struct {
	FloatData  []float64
	StringData []string
	TimeData   []time.Time
	RawData    []interface{}
	Attrs      Attr
	Dimensions []string
}

func (v *DataVar) UnmarshalJSON(text []byte) error {
	var raw RawDataVar
	if err := json.Unmarshal(text, &raw); err != nil {
		return fmt.Errorf("unmarshal raw datavar: %w", err)
	}
	v.Attrs = raw.Attrs
	v.Dimensions = raw.Dimensions
	v.RawData = raw.Data
	if len(raw.Data) == 0 {
		return nil
	}
	if v.Attrs.LongName == "time" {
		for _, tv := range raw.Data {
			timeString, ok := tv.(string)
			if !ok {
				return fmt.Errorf("invalid time %#v: not a string", tv)
			}
			t, err := time.Parse("20060102T1504", timeString)
			if err != nil {
				return fmt.Errorf("invalid time %v: %w", timeString, err)
			}
			v.TimeData = append(v.TimeData, t)
		}
		return nil
	}
	if first, ok := raw.Data[0].([]interface{}); ok {
		if _, ok := first[0].(float64); ok {
			scale := raw.Attrs.ScaleFactor
			for _, rval := range first {
				if val, ok := rval.(float64); ok {
					v.FloatData = append(v.FloatData, val*scale)
				} else {
					v.FloatData = append(v.FloatData, 0)
				}
			}
			return nil
		}
	}
	return nil
}

func Do(ctx context.Context, client *http.Client, req *Request) (*Result, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	hreq, err := http.NewRequestWithContext(ctx, "POST", req.Endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	hreq.Header.Add("content-type", "application/json")
	hres, err := client.Do(hreq)
	if err != nil {
		return nil, fmt.Errorf("do request: %w", err)
	}
	defer hres.Body.Close()
	rbody, err := io.ReadAll(hres.Body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}
	if hres.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("do request: non-OK status: %v; body: %s", hres.Status, rbody)
	}
	var result Result
	if err := json.Unmarshal(rbody, &result); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}
	if !result.Success {
		return &result, errors.New("request marked as unsuccessful by server")
	}
	return &result, nil
}
