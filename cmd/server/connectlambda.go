// Package connectlambda allows writing GRPC servers which either be run as a connect_go grpc
// server, or via AWS Lambda. When running on AWS Lambda only unary RPC calls are supported.
package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/aws/aws-lambda-go/lambda"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

type Config struct {
	Mode     Mode
	HttpAddr string
}

type Mode int

const (
	HttpMode Mode = iota
	LambdaMode
)

// Start either a connect HTTP server or an AWS Lambda handler depending on the mode set via the
// environment variable 'CONNECT_SERVER_MODE'. Valid values are 'lambda', or 'http'. If the mode
// is 'http', then 'CONNECT_SERVER_ADDR' must also be set. This is the address the server needs
// to listen on.
func Start(mux http.Handler, errorLogger func(string)) error {

	config, err := ConfigFromEnv()
	if err != nil {
		return fmt.Errorf("could not start handler : %w", err)
	}

	switch config.Mode {
	case LambdaMode:
		h := &handler{mux: mux, log: errorLogger}
		lambda.Start(h.handleLambdaReq)

	case HttpMode:
		err := http.ListenAndServe(
			config.HttpAddr,
			// Use h2c so we can serve HTTP/2 without TLS.
			h2c.NewHandler(mux, &http2.Server{}))
		if err != nil {
			return fmt.Errorf("failed to start http server : %w", err)
		}

	default:
		panic("unexpected config mode")
	}

	return nil
}

func ConfigFromEnv() (Config, error) {

	config := Config{}

	mode := strings.ToLower(strings.TrimSpace(os.Getenv("CONNECT_SERVER_MODE")))
	switch mode {
	case "lambda":
		config.Mode = LambdaMode
	case "", "http":
		config.Mode = HttpMode
	default:
		return Config{}, fmt.Errorf(`incorrect CONNECT_SERVER_MODE environment value %q, expected "lambda" or "http"`, mode)
	}

	if config.Mode == HttpMode {
		addr, ok := os.LookupEnv("CONNECT_SERVER_ADDR")
		if !ok {
			return Config{}, errors.New("missing CONNECT_SERVER_ADDR environment value")
		}
		// An empty string defaults to ":http" in net.http
		config.HttpAddr = strings.TrimSpace(addr)
	}

	return config, nil
}

type ProxyRequest struct {
	Resource                        string              `json:"resource"`
	Path                            string              `json:"path"`
	HttpMethod                      string              `json:"httpMethod"`
	Headers                         map[string]string   `json:"headers"`
	MultiValueHeaders               map[string][]string `json:"multiValueHeaders"`
	QueryStringParameters           map[string]string   `json:"queryStringParameters"`
	MultiValueQueryStringParameters map[string][]string `json:"multiValueQueryStringParameters"`

	PathParameters any `json:"pathParameters"`
	StageVariables any `json:"stageVariables"`

	Body   string `json:"body"`
	Base64 bool   `json:"isBase64Encoded"`

	RequestContext map[string]any `json:"requestContext"`
}

type ProxyResponse struct {
	Base64 bool                `json:"isBase64Encoded"`
	Status int                 `json:"statusCode"`
	Header map[string][]string `json:"multiValueHeaders"`
	Body   string              `json:"body"`
}

type handler struct {
	mux http.Handler
	log func(msg string)
}

func (h *handler) handleLambdaReq(ctx context.Context, preq ProxyRequest) (ProxyResponse, error) {

	url := preq.Path

	var bs []byte
	if preq.Base64 {
		bs2, err := base64.StdEncoding.DecodeString(preq.Body)
		if err != nil {
			h.log("could not decode body as base64")
			return errorResponse(http.StatusBadRequest, "could not decode body"), nil
		}
		bs = bs2
	} else {
		bs = []byte(preq.Body)
	}

	req, err := http.NewRequestWithContext(ctx, preq.HttpMethod, url, bytes.NewReader(bs))
	if err != nil {
		h.log(fmt.Sprintf("could not create request : %v", err))
		return errorResponse(
			http.StatusBadRequest,
			"could not create request, check url and http method"), nil
	}

	// Copy Http Headers
	for k, v := range preq.Headers {
		req.Header.Add(k, v)
	}
	for k, s := range preq.MultiValueHeaders {
		for _, v := range s {
			req.Header.Add(k, v)
		}
	}

	// TODO consider passing in preq.RequestContext via ctx.
	buf := &ResponseBuffer{}
	h.mux.ServeHTTP(buf, req)

	if !buf.wroteHeader {
		h.log("HTTP handler returned without writing a header or body")
		return errorResponse(http.StatusInternalServerError, "internal server error"), nil
	}

	return buf.ToLambdaProxyResponse(), nil
}

func errorResponse(status int, msg string) ProxyResponse {
	return ProxyResponse{
		Status: status,
		Header: map[string][]string{"Content-Type": {"text/plain"}},
		Body:   msg,
	}
}

type ResponseBuffer struct {
	statusCode  int
	header      http.Header
	buf         bytes.Buffer
	wroteHeader bool
}

func (o *ResponseBuffer) Header() http.Header {
	m := o.header
	if o.header == nil {
		m = make(http.Header)
		o.header = m
	}
	return m
}

func (o *ResponseBuffer) Write(bytes []byte) (int, error) {
	if !o.wroteHeader {
		o.WriteHeader(http.StatusOK)
	}
	if o.statusCode == 0 {
		o.statusCode = http.StatusOK
	}
	_, err := o.buf.Write(bytes)
	if err != nil {
		return 0, err
	}
	return len(bytes), nil
}

func (o *ResponseBuffer) WriteHeader(statusCode int) {
	if o.wroteHeader {
		return
	}
	o.statusCode = statusCode
	o.wroteHeader = true
}

func (o *ResponseBuffer) ToLambdaProxyResponse() ProxyResponse {

	// Leave application/json as plain text for easily readable logs.
	useBase64 := o.header.Get("Content-Type") != "application/json"

	body := ""
	if useBase64 {
		body = base64.StdEncoding.EncodeToString(o.buf.Bytes())
	} else {
		body = o.buf.String()
	}

	return ProxyResponse{
		Base64: useBase64,
		Status: o.statusCode,
		Header: o.header,
		Body:   body,
	}
}
