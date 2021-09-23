package pages

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"math"
	"net/http"
	"path"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/grpc-ecosystem/go-grpc-middleware/logging/zap/ctxzap"
	influxdb2 "github.com/influxdata/influxdb-client-go/v2"
	"github.com/influxdata/influxdb-client-go/v2/api"
	"github.com/jrockway/display/server/mesonet"
	"github.com/jrockway/opinionated-server/client"
	"github.com/mdp/smallfont"
	"go.uber.org/zap"
	"golang.org/x/image/bmp"
)

type InfluxDBConfig struct {
	Address string `long:"influxdb_address" env:"INFLUXDB_ADDRESS" default:"http://localhost:8086" description:"The address of the InfluxDB server to read from."`
	Token   string `long:"influxdb_token" env:"INFLUXDB_TOKEN" description:"An access token to query the provided InfluxDB server."`
	Org     string `long:"influxdb_org" env:"INFLUXDB_ORG" description:"The org that your data is in."`
	//SensorBucket string `long:"influxdb_bucket" env:"INFLUXDB_SENSOR_BUCKET" default:"home-sensors" description:"The bucket to query sensor data from."`
}

type OutputConfig struct {
	Width  int `long:"display_width" env:"DISPLAY_WIDTH" default:"64" description:"The width, in pixels, of the target display."`
	Height int `long:"display_height" env:"DISPLAY_HEIGHT" default:"32" description:"The height, in pixels, of the target display."`
}

type Display struct {
	sync.RWMutex
	influxConfig *InfluxDBConfig
	outputConfig *OutputConfig

	httpClient   *http.Client
	influxClient api.QueryAPI

	Temperature                float64
	LastHourTemperature        float64
	RelativeHumidity           float64
	LastHourRelativeHumidity   float64
	OutdoorTemperature         float64
	LastHourOutdoorTemperature float64
	MesonetLastData            time.Time

	image  *image.RGBA
	screen [][]byte
}

func New(icfg *InfluxDBConfig, ocfg *OutputConfig) *Display {
	cl := &http.Client{
		Transport: client.WrapRoundTripper(http.DefaultTransport),
	}

	opts := influxdb2.DefaultOptions()
	opts.SetHTTPClient(cl)
	client := influxdb2.NewClientWithOptions(icfg.Address, icfg.Token, opts)
	return &Display{
		influxConfig: icfg,
		outputConfig: ocfg,
		influxClient: client.QueryAPI(icfg.Org),
		httpClient:   cl,
	}
}

// queryOneInflux runs the provided influxDB, and assigns the first result value to target.  Target
// must be a non-nil pointer.
func (d *Display) queryOneInflux(ctx context.Context, query string, target interface{}) error {
	tval := reflect.ValueOf(target)
	ttyp := tval.Type()
	if ttyp.Kind() != reflect.Ptr || tval.IsNil() {
		return fmt.Errorf("target must be a non-nil pointer, not %#v", target)
	}
	result, err := d.influxClient.Query(ctx, query)
	if err != nil {
		return fmt.Errorf("query: %w", err)
	}
	defer result.Close()
	if !result.Next() {
		return fmt.Errorf("no rows (possibly caused by: %v)", result.Err())
	}
	rec := result.Record()
	if rec == nil {
		return fmt.Errorf("nil record (possibly caused by: %v)", result.Err())
	}
	raw := result.Record().Value()
	val := reflect.ValueOf(raw)
	if !val.Type().AssignableTo(ttyp.Elem()) {
		return fmt.Errorf("record value %#v not assignable to %#v", val, raw)
	}
	d.Lock()
	defer d.Unlock()
	tval.Elem().Set(val)
	return nil
}

func (d *Display) updateTemperature(ctx context.Context) error {
	query := `from(bucket: "home-sensors")
		  |> range(start: -1h, stop: now())
		  |> filter(fn: (r) => r._measurement == "environment" and r._field == "temperature")
		  |> map(fn: (r) => ({ r with _value: float(v: r._value) / 1000000000.0 - 273.15 }))
		  |> map(fn: (r) => ({ r with _value: r._value * 1.8 + 32.0 }))
		  |> last()`
	if err := d.queryOneInflux(ctx, query, &d.Temperature); err != nil {
		return fmt.Errorf("query: %w", err)
	}
	return nil
}

func (d *Display) updateLastHourTemperature(ctx context.Context) error {
	query := `from(bucket: "home-sensors")
		  |> range(start: -1h, stop: now())
		  |> filter(fn: (r) => r._measurement == "environment" and r._field == "temperature")
		  |> map(fn: (r) => ({ r with _value: float(v: r._value) / 1000000000.0 - 273.15 }))
		  |> map(fn: (r) => ({ r with _value: r._value * 1.8 + 32.0 }))
		  |> mean()
		  |> last()`
	if err := d.queryOneInflux(ctx, query, &d.LastHourTemperature); err != nil {
		return fmt.Errorf("query: %w", err)
	}
	return nil
}

func (d *Display) updateRelativeHumidity(ctx context.Context) error {
	query := `from(bucket: "home-sensors")
		  |> range(start: -1h, stop: now())
		  |> filter(fn: (r) => r._measurement == "environment" and r._field == "relative_humidity")
		  |> map(fn: (r) => ({ r with _value: float(v: r._value) / 100000.0 }))
		  |> last()`
	if err := d.queryOneInflux(ctx, query, &d.RelativeHumidity); err != nil {
		return fmt.Errorf("query: %w", err)
	}
	return nil
}

func (d *Display) updateLastHourRelativeHumidity(ctx context.Context) error {
	query := `from(bucket: "home-sensors")
		  |> range(start: -1h, stop: now())
		  |> filter(fn: (r) => r._measurement == "environment" and r._field == "relative_humidity")
		  |> map(fn: (r) => ({ r with _value: float(v: r._value) / 100000.0 }))
		  |> mean()
		  |> last()`
	if err := d.queryOneInflux(ctx, query, &d.LastHourRelativeHumidity); err != nil {
		return fmt.Errorf("query: %w", err)
	}
	return nil
}

func (d *Display) updateOutdoorTemperature(ctx context.Context) error {
	d.RLock()
	lastPoint := d.MesonetLastData
	d.RUnlock()
	zap.L().Debug("last mesonet data point", zap.Time("last_point", lastPoint))
	if time.Since(lastPoint) < 6*time.Minute {
		// Be nicer to the mesonet server, since they don't get data more frequently than
		// this.
		return nil
	}
	now := time.Now()
	req := &mesonet.Request{
		Endpoint: "https://api.nysmesonet.org/data/dynserv/timeseries2",
		Dataset:  "nysm",
		Start:    now.Add(-time.Hour),
		End:      now,
		Stations: []string{"bkln"},
		Variables: []mesonet.Variable{
			{
				ID:    "tair",
				Units: "degF",
			},
		},
	}
	res, err := mesonet.Do(ctx, d.httpClient, req)
	if err != nil {
		return fmt.Errorf("do: %w", err)
	}
	tair := res.Response.DataVars["tair"].FloatData
	if len(tair) == 0 {
		return fmt.Errorf("no data points returned (datavars: %#v)", res.Response.DataVars)
	}
	var n int
	var sum, last float64
	var lastUpdate time.Time
	for i, t := range tair {
		if t != 0 {
			// Sometimes the most recent data point is returned as 0.
			sum += t
			last = t
			if update := res.Response.Coords["time"].TimeData[i]; !update.IsZero() {
				lastUpdate = update
			}
		}
	}
	d.Lock()
	defer d.Unlock()
	d.OutdoorTemperature = last
	d.LastHourOutdoorTemperature = sum / float64(n)
	d.MesonetLastData = lastUpdate
	return nil
}

func (d *Display) update(ctx context.Context) error {
	doneCh := make(chan error)
	var nq int
	run := func(f func(context.Context) error, op string) {
		nq++
		go func() {
			if err := f(ctx); err != nil {
				doneCh <- fmt.Errorf("%s: %w", op, err)
			}
			doneCh <- nil
		}()
	}
	run(d.updateTemperature, "get temperature")
	run(d.updateLastHourTemperature, "get last hour temperature")
	run(d.updateRelativeHumidity, "get relative humidity")
	run(d.updateLastHourRelativeHumidity, "get last hour relative humidity")
	run(d.updateOutdoorTemperature, "get outdoor temperature")

	var errs []error
	for i := 0; i < nq; i++ {
		select {
		case <-ctx.Done():
			errs = append(errs, ctx.Err())
		case err := <-doneCh:
			if err != nil {
				errs = append(errs, err)
			}
		}
	}
	if len(errs) > 0 {
		var strs []string
		for _, err := range errs {
			strs = append(strs, err.Error())
		}
		return fmt.Errorf("%d errors: %v", len(errs), strings.Join(strs, "\n"))
	}
	return nil
}

func (d *Display) render() error {
	d.Lock()
	defer d.Unlock()

	tempChange := byte(' ')
	if chg := d.Temperature - d.LastHourTemperature; chg < -0.5 {
		tempChange = 25
	} else if chg > 0.5 {
		tempChange = 24
	}
	humChange := byte(' ')
	if chg := d.RelativeHumidity - d.LastHourRelativeHumidity; chg < -0.5 {
		humChange = 25
	} else if chg > 0.5 {
		humChange = 24
	}
	outdoorTempChange := byte(' ')
	if chg := d.OutdoorTemperature - d.LastHourOutdoorTemperature; chg < -1 {
		outdoorTempChange = 25
	} else if chg > 1 {
		outdoorTempChange = 24
	}

	d.screen = make([][]byte, int(math.Floor(float64(d.outputConfig.Height)/8.0)))
	buf := new(bytes.Buffer)
	buf.WriteString(fmt.Sprintf("%.1f", d.Temperature))
	buf.WriteByte(tempChange)
	buf.WriteString(fmt.Sprintf(" %.0f", d.RelativeHumidity))
	buf.WriteByte(humChange)
	d.screen[0] = buf.Bytes()
	buf = new(bytes.Buffer)
	buf.WriteString(fmt.Sprintf("%.1f", d.OutdoorTemperature))
	buf.WriteByte(outdoorTempChange)
	d.screen[1] = buf.Bytes()

	bounds := image.Rect(0, 0, d.outputConfig.Width, d.outputConfig.Height)
	img := image.NewRGBA(bounds)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			img.SetRGBA(x, y, color.RGBA{R: 0, G: 0, B: 0, A: 255})
		}
	}
	sf := smallfont.Context{
		Dst:    img,
		StartX: 0,
		StartY: 0,
		Font:   smallfont.Font5x8,
		Color:  image.White,
	}

	for i := 0; i < d.outputConfig.Height; i += 8 {
		line := i / 8
		if err := sf.Draw([]byte(d.screen[line]), 0, i); err != nil {
			return fmt.Errorf("draw line %d: %w", line, err)
		}
	}
	d.image = img
	return nil
}

func (d *Display) UpdateOnce(ctx context.Context) error {
	if err := d.update(ctx); err != nil {
		return fmt.Errorf("update: %w", err)
	}
	if err := d.render(); err != nil {
		return fmt.Errorf("render: %w", err)
	}
	return nil
}

type displayAsJSON struct {
	Screen []string `json:"screen"`
}

func (d *Display) ServeJSON(w http.ResponseWriter, req *http.Request) {
	disp := new(displayAsJSON)
	d.RLock()
	for _, l := range d.screen {
		disp.Screen = append(disp.Screen, string(l))
	}
	d.RUnlock()
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(&disp); err != nil {
		l := ctxzap.Extract(req.Context())
		l.Info("error sending json to client", zap.Error(err))
	}
}

func (d *Display) enlargedImage(enlarge, space int) *image.RGBA {
	d.RLock()
	defer d.RUnlock()
	src := d.image
	if src == nil {
		src = image.NewRGBA(image.Rect(0, 0, 1, 1))
	}
	img := image.NewRGBA(image.Rect(0, 0, enlarge*src.Bounds().Dx(), enlarge*src.Bounds().Dy()))
	for x := 0; x < src.Bounds().Dx(); x++ {
		for y := 0; y < src.Bounds().Dy(); y++ {
			val := src.At(x, y)
			for i := space; i < enlarge-space; i++ {
				for j := space; j < enlarge-space; j++ {
					img.Set(x*enlarge+i, y*enlarge+j, val)
				}
			}
		}
	}
	return img
}

func (d *Display) ServeLargePNG(w http.ResponseWriter, req *http.Request) {
	ctx := req.Context()
	img := d.enlargedImage(16, 2)
	w.Header().Add("content-type", "image/png")
	w.WriteHeader(http.StatusOK)
	if err := png.Encode(w, img); err != nil {
		l := ctxzap.Extract(ctx)
		l.Error("problem encoding png", zap.Error(err))
	}
}

func (d *Display) ServeImage(w http.ResponseWriter, req *http.Request) {
	ctx := req.Context()

	encoder := png.Encode
	ct := "image/png"
	switch path.Ext(req.URL.Path) {
	case ".bmp":
		encoder = bmp.Encode
		ct = "image/bmp"
	case ".txt":
		encoder = func(w io.Writer, m image.Image) error {
			for x := m.Bounds().Min.X; x < m.Bounds().Max.X; x++ {
				for y := m.Bounds().Min.Y; y < m.Bounds().Max.Y; y++ {
					r, g, b, _ := m.At(x, y).RGBA()
					if r != 0 || g != 0 || b != 0 {
						if _, err := w.Write([]byte(fmt.Sprintf("%v %v\n", x, y))); err != nil {
							return fmt.Errorf("at %v, %v: %v", x, y, err)
						}
					}
				}
			}
			return nil
		}
		ct = "text/plain"
	}

	buf := new(bytes.Buffer)
	if err := func() error {
		d.Lock()
		defer d.Unlock()
		if d.image == nil {
			return errors.New("image has not been rendered yet")
		}
		return encoder(buf, d.image)
	}(); err != nil {
		l := ctxzap.Extract(ctx)
		l.Error("problem encoding image", zap.Error(err))
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Add("content-type", ct)
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, buf); err != nil {
		l := ctxzap.Extract(ctx)
		l.Error("problem copying image to client", zap.Error(err))
	}
}
