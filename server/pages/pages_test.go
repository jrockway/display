package pages

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/grpc-ecosystem/go-grpc-middleware/logging/zap/ctxzap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest"
)

func TestHandlers(t *testing.T) {
	l := zaptest.NewLogger(t, zaptest.Level(zapcore.DebugLevel))
	d := New(&InfluxDBConfig{}, &OutputConfig{
		Width:  64,
		Height: 32,
	})
	d.Temperature = 69
	d.LastHourTemperature = 65
	d.RelativeHumidity = 42
	d.LastHourRelativeHumidity = 50
	d.OutdoorTemperature = 90
	d.LastHourOutdoorTemperature = 90
	if err := d.render(); err != nil {
		t.Fatalf("render: %v", err)
	}
	testData := []struct {
		url     string
		handler http.HandlerFunc
	}{
		{
			url:     "/index.json",
			handler: d.ServeJSON,
		},
		{
			url:     "/index.png",
			handler: d.ServeImage,
		},
		{
			url:     "/large.png",
			handler: d.ServeLargePNG,
		},
		{
			url:     "/index.bmp",
			handler: d.ServeImage,
		},
		{
			url:     "/index.txt",
			handler: d.ServeImage,
		},
	}

	for _, test := range testData {
		t.Run(test.url[1:], func(t *testing.T) {
			ctx := context.Background()
			ctx = ctxzap.ToContext(ctx, l)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest("GET", test.url, nil)
			req = req.WithContext(ctx)
			test.handler(rec, req)
			if got, want := rec.Code, http.StatusOK; got != want {
				t.Errorf("status:\n  got: %v\n want: %v", got, want)
			}
		})
	}
}
