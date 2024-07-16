/*
Copyright (C) 2022-2024 ApeCloud Co., Ltd

This file is part of KubeBlocks project

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program.  If not, see <http://www.gnu.org/licenses/>.
*/

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"

	fasthttprouter "github.com/fasthttp/router"
	"github.com/valyala/fasthttp"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/apecloud/kubeblocks/pkg/kbagent/service"
)

const (
	defaultMaxConcurrency = 8
	jsonContentTypeHeader = "application/json"
)

var (
	logger = ctrl.Log.WithName("HTTP")
)

type server struct {
	config   Config
	services []service.Service
	servers  []*fasthttp.Server
}

var _ Server = &server{}

// StartNonBlocking starts a new server in a goroutine.
func (s *server) StartNonBlocking() error {
	logger.Info("Starting HTTP Server")
	handler := s.router()

	APILogging := s.config.Logging
	if APILogging {
		handler = s.apiLogger(handler)
	}

	var listeners []net.Listener
	if s.config.UnixDomainSocket != "" {
		socket := fmt.Sprintf("%s/kb_agent.socket", s.config.UnixDomainSocket)
		l, err := net.Listen("unix", socket)
		if err != nil {
			return err
		}
		listeners = append(listeners, l)
	} else {
		l, err := net.Listen("tcp", fmt.Sprintf("%s:%v", s.config.Address, s.config.Port))
		if err != nil {
			logger.Error(err, "listen address", s.config.Address, "port", s.config.Port)
		} else {
			listeners = append(listeners, l)
		}
	}

	if len(listeners) == 0 {
		return errors.New("no endpoint to listen on")
	}

	for _, listener := range listeners {
		// customServer is created in a loop because each instance
		// has a handle on the underlying listener.
		customServer := &fasthttp.Server{
			Handler: handler,
		}

		if s.config.Concurrency > 0 {
			customServer.Concurrency = s.config.Concurrency
		} else {
			customServer.Concurrency = defaultMaxConcurrency
		}

		s.servers = append(s.servers, customServer)
		go func(l net.Listener) {
			if err := customServer.Serve(l); err != nil {
				panic(err)
			}
		}(listener)
	}

	return nil
}

func (s *server) Close() error {
	errs := make([]error, len(s.servers))

	for i, ln := range s.servers {
		// This calls `Close()` on the underlying listener.
		if err := ln.Shutdown(); err != nil {
			logger.Error(err, "server close failed")
			errs[i] = err
		}
	}

	return errors.Join()
}

func (s *server) apiLogger(next fasthttp.RequestHandler) fasthttp.RequestHandler {
	return func(ctx *fasthttp.RequestCtx) {
		reqLogger := logger
		if userAgent := string(ctx.Request.Header.Peek("User-Agent")); userAgent != "" {
			reqLogger = logger.WithValues("useragent", userAgent)
		}
		start := time.Now()
		path := string(ctx.Path())
		reqLogger.Info("HTTP API Called", "method", string(ctx.Method()), "path", path)
		next(ctx)
		elapsed := float64(time.Since(start) / time.Millisecond)
		reqLogger.Info("HTTP API Called", "status code", ctx.Response.StatusCode(), "cost", elapsed)
	}
}

func (s *server) router() fasthttp.RequestHandler {
	router := fasthttprouter.New()
	for i := range s.services {
		s.registerService(router, s.services[i])
	}
	return router.Handler
}

func (s *server) registerService(router *fasthttprouter.Router, svc service.Service) {
	path := fmt.Sprintf("/%s/%s", svc.Version(), svc.URI())
	router.Handle(fasthttp.MethodPost, path, s.dispatcher(svc))
	logger.Info("service route", "method", fasthttp.MethodPost, "path", path)
}

func (s *server) dispatcher(svc service.Service) func(*fasthttp.RequestCtx) {
	return func(reqCtx *fasthttp.RequestCtx) {
		ctx := context.Background()
		body := reqCtx.PostBody()

		req, err := svc.Decode(body)
		if err != nil {
			msg := newErrorResponse("ERR_MALFORMED_REQUEST", fmt.Sprintf("unmarshal HTTP body failed: %v", err))
			respond(reqCtx, withError(fasthttp.StatusBadRequest, msg))
			return
		}

		rsp, err := svc.Call(ctx, req)
		statusCode := fasthttp.StatusOK
		if err != nil {
			if errors.Is(err, service.ErrNotImplemented) {
				statusCode = fasthttp.StatusNotImplemented
			} else {
				statusCode = fasthttp.StatusInternalServerError
			}

			logger.Info("service call failed", "service", svc.Kind(), "error", err.Error())

			msg := newErrorResponse("ERR_SERVICE_FAILED", fmt.Sprintf("service call failed: %s", err.Error()))
			respond(reqCtx, withError(statusCode, msg))
			return
		}

		if rsp == nil {
			respond(reqCtx, withEmpty())
		} else {
			respond(reqCtx, withJSON(statusCode, rsp))
		}
	}
}

type errorResponse struct {
	ErrorCode string `json:"errorCode"`
	Message   string `json:"message"`
}

func newErrorResponse(errorCode, message string) errorResponse {
	return errorResponse{
		ErrorCode: errorCode,
		Message:   message,
	}
}

type option = func(ctx *fasthttp.RequestCtx)

func withJSON(code int, obj []byte) option {
	return func(ctx *fasthttp.RequestCtx) {
		ctx.Response.SetStatusCode(code)
		ctx.Response.SetBody(obj)
		ctx.Response.Header.SetContentType(jsonContentTypeHeader)
	}
}

func withError(code int, resp errorResponse) option {
	b, _ := json.Marshal(&resp)
	return withJSON(code, b)
}

func withEmpty() option {
	return func(ctx *fasthttp.RequestCtx) {
		ctx.Response.SetBody(nil)
		ctx.Response.SetStatusCode(fasthttp.StatusNoContent)
	}
}

func respond(ctx *fasthttp.RequestCtx, options ...option) {
	for _, option := range options {
		option(ctx)
	}
}
