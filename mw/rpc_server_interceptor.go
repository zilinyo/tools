// Copyright © 2023 OpenIM. All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package mw

import (
	"context"
	"fmt"
	"github.com/pkg/errors"
	"github.com/zilinyo/tools/checker"
	"math"
	"runtime"
	"strings"

	"github.com/openimsdk/protocol/constant"
	"github.com/openimsdk/protocol/errinfo"
	"github.com/zilinyo/tools/errs"
	"github.com/zilinyo/tools/log"
	"github.com/zilinyo/tools/mw/specialerror"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func rpcString(v any) string {
	if s, ok := v.(interface{ String() string }); ok {
		return s.String()
	}
	return fmt.Sprintf("%+v", v)
}

func RpcServerInterceptor(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	funcName := info.FullMethod
	md, err := validateMetadata(ctx)
	if err != nil {
		return nil, err
	}
	ctx, err = enrichContextWithMetadata(ctx, md)
	if err != nil {
		return nil, err
	}
	log.ZInfo(ctx, fmt.Sprintf("RPC Server Request - %s", extractFunctionName(funcName)), "funcName", funcName, "req", rpcString(req))
	if err := checker.Validate(req); err != nil {
		return nil, err
	}

	resp, err := handler(ctx, req)
	if err != nil {
		return nil, handleError(ctx, funcName, req, err)
	}
	log.ZInfo(ctx, fmt.Sprintf("RPC Server Response Success - %s", extractFunctionName(funcName)), "funcName", funcName, "resp", rpcString(resp))
	return resp, nil
}

func validateMetadata(ctx context.Context) (metadata.MD, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return nil, status.New(codes.InvalidArgument, "missing metadata").Err()
	}
	if len(md.Get(constant.OperationID)) != 1 {
		return nil, status.New(codes.InvalidArgument, "operationID error").Err()
	}
	return md, nil
}

func enrichContextWithMetadata(ctx context.Context, md metadata.MD) (context.Context, error) {
	if keys := md.Get(constant.RpcCustomHeader); len(keys) > 0 {
		ctx = context.WithValue(ctx, constant.RpcCustomHeader, keys)
		for _, key := range keys {
			values := md.Get(key)
			if len(values) == 0 {
				return nil, status.New(codes.InvalidArgument, fmt.Sprintf("missing metadata key %s", key)).Err()
			}
			ctx = context.WithValue(ctx, key, values)
		}
	}
	ctx = context.WithValue(ctx, constant.OperationID, md.Get(constant.OperationID)[0])
	if opts := md.Get(constant.OpUserID); len(opts) == 1 {
		ctx = context.WithValue(ctx, constant.OpUserID, opts[0])
	}
	if opts := md.Get(constant.OpUserPlatform); len(opts) == 1 {
		ctx = context.WithValue(ctx, constant.OpUserPlatform, opts[0])
	}
	if opts := md.Get(constant.ConnID); len(opts) == 1 {
		ctx = context.WithValue(ctx, constant.ConnID, opts[0])
	}
	return ctx, nil
}

func handleError(ctx context.Context, funcName string, req any, err error) error {
	log.ZWarn(ctx, "rpc server resp WithDetails error", formatError(err), "funcName", funcName)
	unwrap := errs.Unwrap(err)
	codeErr := specialerror.ErrCode(unwrap)
	if codeErr == nil {
		log.ZError(ctx, "rpc InternalServer error", formatError(err), "funcName", funcName, "req", req)
		codeErr = errs.ErrInternalServer
	}
	code := codeErr.Code()
	if code <= 0 || int64(code) > int64(math.MaxUint32) {
		log.ZError(ctx, "rpc UnknownError", formatError(err), "funcName", funcName, "rpc UnknownCode:", int64(code))
		code = errs.ServerInternalError
	}
	grpcStatus := status.New(codes.Code(code), err.Error())
	errInfo := &errinfo.ErrorInfo{Cause: err.Error()}
	details, err := grpcStatus.WithDetails(errInfo)
	if err != nil {
		log.ZWarn(ctx, "rpc server resp WithDetails error", formatError(err), "funcName", funcName)
		return errs.WrapMsg(err, "rpc server resp WithDetails error", "err", err)
	}
	log.ZWarn(ctx, fmt.Sprintf("RPC Server Response Error - %s", extractFunctionName(funcName)), formatError(details.Err()), "funcName", funcName, "req", req, "err", err)
	return details.Err()
}

func GrpcServer() grpc.ServerOption {
	return grpc.ChainUnaryInterceptor(RpcServerInterceptor)
}
func formatError(err error) error {
	type stackTracer interface {
		StackTrace() errors.StackTrace
	}
	if e, ok := err.(stackTracer); ok {
		st := e.StackTrace()
		var sb strings.Builder
		sb.WriteString("Error: ")
		sb.WriteString(err.Error())
		sb.WriteString(" | Error trace: ")

		var callPath []string
		for _, f := range st {
			pc := uintptr(f) - 1
			fn := runtime.FuncForPC(pc)
			if fn == nil {
				continue
			}
			if strings.Contains(fn.Name(), "runtime.") {
				continue
			}
			file, line := fn.FileLine(pc)
			funcName := simplifyFuncName(fn.Name())
			callPath = append(callPath, fmt.Sprintf("%s (%s:%d)", funcName, file, line))
		}
		for i := len(callPath) - 1; i >= 0; i-- {
			if i != len(callPath)-1 {
				sb.WriteString(" -> ")
			}
			sb.WriteString(callPath[i])
		}
		return errors.New(sb.String())
	}
	return err
}
func simplifyFuncName(fullFuncName string) string {
	parts := strings.Split(fullFuncName, "/")
	lastPart := parts[len(parts)-1]
	parts = strings.Split(lastPart, ".")
	if len(parts) > 1 {
		return parts[len(parts)-1]
	}
	return lastPart
}
