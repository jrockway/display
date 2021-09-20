package main

import (
	"context"
	"net/http"
	"time"

	"github.com/jrockway/display/server/pages"
	"github.com/jrockway/opinionated-server/server"
	"go.uber.org/zap"
)

func main() {
	server.AppName = "display"

	influxConfig := new(pages.InfluxDBConfig)
	outputConfig := new(pages.OutputConfig)
	server.AddFlagGroup("InfluxDB", influxConfig)
	server.AddFlagGroup("Output", outputConfig)
	server.Setup()

	display := pages.New(influxConfig, outputConfig)
	mux := http.NewServeMux()
	server.SetHTTPHandler(mux)
	mux.HandleFunc("/index.json", display.ServeJSON)
	mux.HandleFunc("/index.png", display.ServePNG)
	mux.HandleFunc("/large.png", display.ServeLargePNG)
	go func() {
		interval := 10 * time.Second
		t := time.NewTicker(interval)
		for {
			ctx, c := context.WithTimeout(context.Background(), interval)
			if err := display.UpdateOnce(ctx); err != nil {
				zap.L().Warn("problem updating display", zap.Error(err))
			}
			c()
			<-t.C
		}
	}()

	server.ListenAndServe()
}
