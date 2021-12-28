package promwrite_test

import (
	"context"
	"errors"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gogo/protobuf/proto"
	"github.com/golang/snappy"
	"github.com/stretchr/testify/require"

	"github.com/castai/promwrite"
	"github.com/castai/promwrite/prompb"
)

func TestClient(t *testing.T) {
	t.Run("write with default options", func(t *testing.T) {
		r := require.New(t)

		receivedWriteRequest := make(chan *prompb.WriteRequest, 1)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			b, _ := ioutil.ReadAll(req.Body)
			parsed, err := parseWriteRequest(b)
			r.NoError(err)
			receivedWriteRequest <- parsed
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		client := promwrite.NewClient(srv.URL)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		now := time.Now().UTC()
		req := &promwrite.WriteRequest{
			TimeSeries: []promwrite.TimeSeries{
				{
					Labels: []promwrite.Label{
						{
							Name:  "__name__",
							Value: "metric_a",
						},
						{
							Name:  "custom_label_a",
							Value: "custom_value_a",
						},
					},
					Sample: promwrite.Sample{
						Time:  now,
						Value: 123,
					},
				},
				{
					Labels: []promwrite.Label{
						{
							Name:  "__name__",
							Value: "metric_b",
						},
					},
					Sample: promwrite.Sample{
						Time:  now,
						Value: 456,
					},
				},
			},
		}
		_, err := client.Write(ctx, req)
		r.NoError(err)

		res := <-receivedWriteRequest
		r.Len(res.Timeseries, 2)
		r.Equal(prompb.TimeSeries{
			Labels: []prompb.Label{
				{
					Name:  "__name__",
					Value: "metric_a",
				},
				{
					Name:  "custom_label_a",
					Value: "custom_value_a",
				},
			},
			Samples: []prompb.Sample{
				{
					Timestamp: now.UnixNano() / int64(time.Millisecond),
					Value:     123,
				},
			},
		}, res.Timeseries[0])
		r.Equal(prompb.TimeSeries{
			Labels: []prompb.Label{
				{
					Name:  "__name__",
					Value: "metric_b",
				},
			},
			Samples: []prompb.Sample{
				{
					Timestamp: now.UnixNano() / int64(time.Millisecond),
					Value:     456,
				},
			},
		}, res.Timeseries[1])
	})

	t.Run("write with custom options", func(t *testing.T) {
		r := require.New(t)

		receivedWriteRequest := make(chan *prompb.WriteRequest, 1)
		receivedHeaders := make(chan http.Header, 1)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			b, _ := ioutil.ReadAll(req.Body)
			parsed, err := parseWriteRequest(b)
			r.NoError(err)
			receivedWriteRequest <- parsed
			receivedHeaders <- req.Header
			w.WriteHeader(http.StatusAccepted)
		}))
		defer srv.Close()

		sentRequest := make(chan *http.Request, 1)
		client := promwrite.NewClient(
			srv.URL,
			promwrite.HttpClient(&http.Client{
				Timeout: 5 * time.Second,
				Transport: &customTestHttpClientTransport{
					reqChan: sentRequest,
					next:    http.DefaultTransport,
				},
			}),
		)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		now := time.Now().UTC()
		req := &promwrite.WriteRequest{
			TimeSeries: []promwrite.TimeSeries{
				{
					Labels: []promwrite.Label{
						{
							Name:  "__name__",
							Value: "metric_a",
						},
					},
					Sample: promwrite.Sample{
						Time:  now,
						Value: 123,
					},
				},
			},
		}
		_, err := client.Write(ctx, req, promwrite.WriteHeaders(map[string]string{"X-Scope-OrgID": "tenant1"}))
		r.NoError(err)

		res := <-receivedWriteRequest
		r.Len(res.Timeseries, 1)
		r.Equal(prompb.TimeSeries{
			Labels: []prompb.Label{
				{
					Name:  "__name__",
					Value: "metric_a",
				},
			},
			Samples: []prompb.Sample{
				{
					Timestamp: now.UnixNano() / int64(time.Millisecond),
					Value:     123,
				},
			},
		}, res.Timeseries[0])

		sentReq := <-sentRequest
		reqHeaders := sentReq.Header
		recvHeaders := <-receivedHeaders
		r.Equal("tenant1", reqHeaders.Get("X-Scope-OrgID"))
		r.Equal("tenant1", recvHeaders.Get("X-Scope-OrgID"))
	})

	t.Run("write error", func(t *testing.T) {
		r := require.New(t)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte("ups"))
		}))
		defer srv.Close()

		client := promwrite.NewClient(srv.URL)

		_, err := client.Write(context.Background(), &promwrite.WriteRequest{})
		r.EqualError(err, "promwrite: expected status 200, got 400: ups")
		var writeErr *promwrite.WriteError
		r.True(errors.As(err, &writeErr))
		r.Equal(http.StatusBadRequest, writeErr.StatusCode())
	})
}

func parseWriteRequest(src []byte) (*prompb.WriteRequest, error) {
	decompressed, err := snappy.Decode(nil, src)
	if err != nil {
		return nil, err
	}
	var res prompb.WriteRequest
	if err := proto.Unmarshal(decompressed, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

type customTestHttpClientTransport struct {
	reqChan chan *http.Request
	next    http.RoundTripper
}

func (t *customTestHttpClientTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.reqChan <- req
	return t.next.RoundTrip(req)
}
